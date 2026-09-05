package middleware

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/devstroop/notalk/internal/database"
	"github.com/golang-jwt/jwt/v5"
)

// ─── Sentinel errors ──────────────────────────────────────────────

// ErrNoCredentials means no recognized credential was present on the request.
// For Public routes this is success (no principal); for User/Service it becomes 401.
var ErrNoCredentials = errors.New("no credentials")

// ErrInvalidCredentials means a recognized credential was present but failed validation.
// Must never fall back to another mechanism — return 401 immediately.
var ErrInvalidCredentials = errors.New("invalid credentials")

// ─── Authenticator abstraction ────────────────────────────────────

// Authenticator authenticates a request and returns (Principal, recognized, error).
//
// Semantics (must be preserved by composite):
//   - recognized==false, err==ErrNoCredentials → no credential of this type present → try next
//   - recognized==true, err==ErrInvalidCredentials → credential present but invalid → 401, STOP
//   - recognized==true, err==nil, principal!=nil → valid → STOP
//   - Never: invalid credential silently falling back to another mechanism
//
// Example trap that must 401: `Authorization: Bearer garbage` + valid `Cookie: notalk_session`
// must not authenticate via session.
type Authenticator interface {
	Authenticate(r *http.Request) (*Principal, bool, error)
}

// ─── Secret (admin bearer) ───────────────────────────────────────

type SecretAuthenticator struct {
	Secret string // AdminSecret (NOTALK_ADMIN_SECRET, fallback NOTALK_AUTH_SECRET_KEY)
}

func (a *SecretAuthenticator) Authenticate(r *http.Request) (*Principal, bool, error) {
	token, ok := bearerToken(r)
	if !ok {
		return nil, false, ErrNoCredentials
	}
	// Only recognize exact secret match; non-match is not "this mechanism's failure"
	// but we must distinguish: if token is present but != secret, we cannot yet say
	// ErrInvalidCredentials — another mechanism (JWT/API key) may own it. So:
	// - if token == secret → valid, recognized
	// - else → not recognized by this authenticator, let next try
	if subtle.ConstantTimeCompare([]byte(token), []byte(a.Secret)) != 1 {
		return nil, false, ErrNoCredentials
	}
	return &Principal{
		UserID:   "system",
		Username: "system",
		RoleName: "admin",
		Roles:    []string{"admin"},
		Scopes:   []string{"*"},
		Authn:    "secret",
	}, true, nil
}

// ─── JWT ──────────────────────────────────────────────────────────

type JWTAuthenticator struct {
	Secret string // JWTSecret (NOTALK_JWT_SECRET, fallback NOTALK_AUTH_SECRET_KEY)
	DB     *database.DB
}

func (a *JWTAuthenticator) Authenticate(r *http.Request) (*Principal, bool, error) {
	token, ok := bearerToken(r)
	if !ok {
		return nil, false, ErrNoCredentials
	}
	// JWT recognized iff token looks like JWT (2 dots) — otherwise defer to API key/secret
	if strings.Count(token, ".") != 2 {
		return nil, false, ErrNoCredentials
	}

	claims := &jwt.RegisteredClaims{}
	parsed, err := jwt.ParseWithClaims(token, claims, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, jwt.ErrSignatureInvalid
		}
		return []byte(a.Secret), nil
	})
	if err != nil || !parsed.Valid {
		return nil, true, ErrInvalidCredentials
	}
	userID := claims.Subject
	if userID == "" {
		return nil, true, ErrInvalidCredentials
	}
	user, err := a.DB.GetUser(userID)
	if err != nil || user == nil {
		return nil, true, ErrInvalidCredentials
	}
	if !user.Enabled {
		return nil, true, ErrInvalidCredentials
	}
	perms, err := a.DB.GetRolePermissions(user.RoleID)
	if err != nil {
		return nil, true, ErrInvalidCredentials
	}
	scoped := GetScopedAccountID(r.Context())
	var scopedPtr *string
	if scoped != "" {
		scopedPtr = &scoped
	}
	return &Principal{
		UserID:          user.ID,
		Username:        user.Username,
		RoleName:        user.RoleName,
		Roles:           []string{user.RoleName},
		Scopes:          perms,
		Authn:           "jwt",
		ScopedAccountID: scopedPtr,
	}, true, nil
}

// ─── API Key (notalk_*) ──────────────────────────────────────────

type APIKeyAuthenticator struct {
	DB *database.DB
}

func (a *APIKeyAuthenticator) Authenticate(r *http.Request) (*Principal, bool, error) {
	token, ok := bearerToken(r)
	if !ok {
		return nil, false, ErrNoCredentials
	}
	if !strings.HasPrefix(token, "notalk_") {
		return nil, false, ErrNoCredentials
	}
	hash := sha256.Sum256([]byte(token))
	keyHash := hex.EncodeToString(hash[:])

	rec, err := a.DB.GetAPIKeyByHash(keyHash)
	if err != nil || rec == nil || !rec.Enabled {
		return nil, true, ErrInvalidCredentials
	}
	if rec.ExpiresAt != nil {
		if exp, err := time.Parse(time.RFC3339, *rec.ExpiresAt); err == nil && exp.Before(time.Now()) {
			return nil, true, ErrInvalidCredentials
		}
	}
	user, err := a.DB.GetUser(rec.UserID)
	if err != nil || user == nil {
		return nil, true, ErrInvalidCredentials
	}
	if !user.Enabled {
		return nil, true, ErrInvalidCredentials
	}
	perms, err := a.DB.GetRolePermissions(user.RoleID)
	if err != nil {
		return nil, true, ErrInvalidCredentials
	}
	go func() { _ = a.DB.UpdateAPIKeyLastUsed(rec.ID) }()

	p := &Principal{
		UserID:   user.ID,
		Username: user.Username,
		RoleName: user.RoleName,
		Roles:    []string{user.RoleName},
		Scopes:   perms,
		Authn:    "api_key",
	}
	if rec.AccountID != nil && *rec.AccountID != "" {
		p.ScopedAccountID = rec.AccountID
	}
	return p, true, nil
}

// ─── Session (cookie notalk_session) ─────────────────────────────

type SessionAuthenticator struct {
	Secret string // NOTALK_JWT_SECRET (same signing key) or dedicated session secret
	DB     *database.DB
}

func (a *SessionAuthenticator) Authenticate(r *http.Request) (*Principal, bool, error) {
	c, err := r.Cookie("notalk_session")
	if err != nil || c.Value == "" {
		return nil, false, ErrNoCredentials
	}
	token := c.Value
	claims := &jwt.RegisteredClaims{}
	parsed, perr := jwt.ParseWithClaims(token, claims, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, jwt.ErrSignatureInvalid
		}
		return []byte(a.Secret), nil
	})
	if perr != nil || !parsed.Valid {
		return nil, true, ErrInvalidCredentials
	}
	userID := claims.Subject
	if userID == "" {
		return nil, true, ErrInvalidCredentials
	}
	user, uerr := a.DB.GetUser(userID)
	if uerr != nil || user == nil {
		return nil, true, ErrInvalidCredentials
	}
	if !user.Enabled {
		return nil, true, ErrInvalidCredentials
	}
	perms, err := a.DB.GetRolePermissions(user.RoleID)
	if err != nil {
		return nil, true, ErrInvalidCredentials
	}
	return &Principal{
		UserID:   user.ID,
		Username: user.Username,
		RoleName: user.RoleName,
		Roles:    []string{user.RoleName},
		Scopes:   perms,
		Authn:    "session",
	}, true, nil
}

// ─── Service (internal) ──────────────────────────────────────────

type ServiceAuthenticator struct {
	Secret string // NOTALK_SERVICE_SECRET (future); not wired until /internal consumer exists
}

func (a *ServiceAuthenticator) Authenticate(r *http.Request) (*Principal, bool, error) {
	token, ok := bearerToken(r)
	if !ok {
		return nil, false, ErrNoCredentials
	}
	if subtle.ConstantTimeCompare([]byte(token), []byte(a.Secret)) != 1 {
		return nil, false, ErrNoCredentials
	}
	return &Principal{
		UserID:   "service",
		Username: "service",
		Roles:    []string{"service"},
		Scopes:   []string{"*"},
		Authn:    "service",
	}, true, nil
}

// ─── Composite ────────────────────────────────────────────────────

type CompositeAuthenticator []Authenticator

// Authenticate tries each authenticator in order.
// - recognized+valid → principal (stop)
// - recognized+invalid → ErrInvalidCredentials (stop, no fallback)
// - not recognized → try next
// - none recognized → ErrNoCredentials
func (c CompositeAuthenticator) Authenticate(r *http.Request) (*Principal, bool, error) {
	for _, a := range c {
		p, recognized, err := a.Authenticate(r)
		if !recognized {
			continue
		}
		if err != nil {
			return nil, true, ErrInvalidCredentials
		}
		if p != nil {
			return p, true, nil
		}
		return nil, true, ErrInvalidCredentials
	}
	return nil, false, ErrNoCredentials
}

// ─── Helpers ──────────────────────────────────────────────────────

func bearerToken(r *http.Request) (string, bool) {
	header := r.Header.Get("Authorization")
	if header == "" {
		return "", false
	}
	parts := strings.SplitN(header, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return "", false
	}
	if parts[1] == "" {
		return "", false
	}
	return parts[1], true
}
