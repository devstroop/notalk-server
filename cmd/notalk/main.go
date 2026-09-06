package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/devstroop/notalk/internal/config"
	"github.com/devstroop/notalk/internal/database"
	"github.com/devstroop/notalk/internal/handler"
	"github.com/devstroop/notalk/internal/mcpserver"
	"github.com/devstroop/notalk/internal/middleware"
	"github.com/devstroop/notalk/internal/service"
	smtpclient "github.com/devstroop/notalk/internal/smtp"
	"github.com/lib/pq"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"go.mau.fi/whatsmeow/proto/waCompanionReg"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"google.golang.org/protobuf/proto"

	mcphttp "github.com/mark3labs/mcp-go/server"
)

// version is set via -ldflags "-X main.version=..." at build time.
var version = "dev"

func main() {
	// Load configuration
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load config: %v\n", err)
		os.Exit(1)
	}

	// Setup logging
	setupLogging(cfg.Logging.Level)

	log.Info().Str("version", version).Msg("starting NoTalk")

	// Set the device identity shown in WhatsApp's "Linked Devices" on the phone.
	// Protocol only has os + platformType — no free-text device name field.
	// Browser types (CHROME) auto-label without prompting; DESKTOP always prompts.
	// Result: phone shows "Chrome (NoTalk)" with no naming dialog.
	store.DeviceProps.Os = proto.String("NoTalk")
	store.DeviceProps.PlatformType = waCompanionReg.DeviceProps_CHROME.Enum()
	// Send push name in the handshake payload itself (fastest possible propagation).
	store.BaseClientPayload.PushName = proto.String("NoTalk")

	// Set whatsmeow PostgreSQL array wrapper for session stores
	sqlstore.PostgresArrayWrapper = pq.Array

	if cfg.Auth.EffectiveJWTSecret() == "" || cfg.Auth.EffectiveJWTSecret() == "changeme" || cfg.Auth.EffectiveAdminSecret() == "" || cfg.Auth.EffectiveAdminSecret() == "changeme" {
		log.Warn().Msg("using default auth secret key — set NOTALK_JWT_SECRET and NOTALK_ADMIN_SECRET for production (NOTALK_AUTH_SECRET_KEY fallback is deprecated)")
	} else if cfg.Auth.UsesLegacySecret() {
		log.Warn().Msg("using NOTALK_AUTH_SECRET_KEY fallback — set NOTALK_JWT_SECRET and NOTALK_ADMIN_SECRET to silence and to split JWT/admin boundaries")
	}

	// Open database
	db, err := database.Open(cfg.Database.DSN)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to open database")
	}
	defer func() { _ = db.Close() }()

	// Create account manager
	mgr, err := service.NewAccountManager(cfg, db)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to create account manager")
	}

	// Discover existing accounts from DB
	if err := mgr.DiscoverAccounts(context.Background()); err != nil {
		log.Error().Err(err).Msg("failed to discover accounts")
	}

	// Build HTTP router
	mux := http.NewServeMux()

	// Health endpoint (no auth)
	mux.HandleFunc("GET /api/health", handler.Health)

	// SMTP client (for password reset emails)
	mailer := smtpclient.New(cfg.SMTP)
	if mailer.Enabled() {
		log.Info().Str("host", cfg.SMTP.Host).Int("port", cfg.SMTP.Port).Msg("SMTP configured")
	} else {
		log.Warn().Msg("SMTP not configured — forgot-password tokens will be logged to console")
	}

	// Public auth endpoints (no auth middleware)
	authH := handler.NewAuthHandler(db, cfg.Auth.SecretKey, cfg.Auth.RegistrationEnabled, mailer)
	mux.HandleFunc("POST /api/v1/auth/login", authH.Login)
	mux.HandleFunc("POST /api/v1/auth/register", authH.Register)
	mux.HandleFunc("POST /api/v1/auth/forgot-password", authH.ForgotPassword)
	mux.HandleFunc("POST /api/v1/auth/reset-password", authH.ResetPassword)

	// API v1 — all routes require auth
	api := handler.NewAPI(mgr, db)
	apiMux := http.NewServeMux()
	api.RegisterRoutes(apiMux)
	handler.RegisterRBACRoutes(apiMux, db)

	// Wrap API routes with auth middleware, then rate limit
	authed := middleware.Auth(cfg.Auth.SecretKey, db, apiMux)
	limited := middleware.RateLimit(cfg.Limits.MaxConcurrentRequests, authed)
	mux.Handle("/api/v1/", limited)

	// MCP (Model Context Protocol) endpoint — auth + account scoping at fixed path /mcp
	// Seed MCP enabled default from config into DB settings (only if not already set)
	if db.GetSetting("mcp.enabled", "") == "" {
		val := "true"
		if !cfg.MCP.Enabled {
			val = "false"
		}
		_ = db.SetSetting("mcp.enabled", val)
	}
	mcpSrv := mcpserver.New(mgr, db, version)
	mcpTransport := mcphttp.NewStreamableHTTPServer(mcpSrv,
		mcphttp.WithStateful(true),
		mcphttp.WithSessionIdleTTL(10*time.Minute),
	)
	mcpInner := middleware.Auth(cfg.Auth.SecretKey, db, middleware.MCPScope(db, mcpTransport))
	// Gate: check DB setting at runtime so admin can toggle without restart
	mcpHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !db.GetSettingBool("mcp.enabled", true) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":"MCP server is disabled by administrator"}`))
			return
		}
		rc := http.NewResponseController(w)
		_ = rc.SetReadDeadline(time.Time{})
		_ = rc.SetWriteDeadline(time.Time{})
		mcpInner.ServeHTTP(w, r)
	})
	mux.Handle("/mcp", mcpHandler)
	log.Info().Str("path", "/mcp").Msg("MCP endpoint registered")

	// Swagger UI (no auth)
	if cfg.Swagger.Enabled {
		swaggerPath := cfg.Swagger.Path
		mux.Handle(swaggerPath+"/", handler.SwaggerUI(swaggerPath))
		mux.Handle(swaggerPath, handler.SwaggerUI(swaggerPath))
		log.Info().Str("path", swaggerPath).Msg("swagger UI enabled")
	}

	// Wrap everything with CORS
	root := middleware.CORS(cfg.CORS, mux)

	addr := fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)
	srv := &http.Server{
		Addr:    addr,
		Handler: root,
		// ReadHeaderTimeout guards against slowloris attacks (slow header sending).
		// Do NOT use ReadTimeout here — it applies to the entire request lifecycle
		// including handler execution, which kills long-lived SSE streams and
		// slow WhatsApp operations (group creation can take up to 75s).
		ReadHeaderTimeout: 30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	// Graceful shutdown
	done := make(chan os.Signal, 1)
	signal.Notify(done, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		log.Info().Str("addr", addr).Msg("server listening")
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal().Err(err).Msg("server error")
		}
	}()

	<-done
	log.Info().Msg("shutting down…")

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	mgr.ShutdownAll()
	if err := srv.Shutdown(ctx); err != nil {
		log.Error().Err(err).Msg("server shutdown error")
	}
	log.Info().Msg("goodbye")
}

func setupLogging(level string) {
	zerolog.TimeFieldFormat = time.RFC3339
	log.Logger = zerolog.New(zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: "15:04:05"}).
		With().Timestamp().Caller().Logger()

	switch level {
	case "trace":
		zerolog.SetGlobalLevel(zerolog.TraceLevel)
	case "debug":
		zerolog.SetGlobalLevel(zerolog.DebugLevel)
	case "warn":
		zerolog.SetGlobalLevel(zerolog.WarnLevel)
	case "error":
		zerolog.SetGlobalLevel(zerolog.ErrorLevel)
	default:
		zerolog.SetGlobalLevel(zerolog.InfoLevel)
	}
}
