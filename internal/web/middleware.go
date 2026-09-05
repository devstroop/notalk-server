package web

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"strings"
	"time"

	"github.com/devstroop/notalk/internal/database"
	"github.com/devstroop/notalk/internal/middleware"
	"github.com/golang-jwt/jwt/v5"
)

const sessionCookie = "notalk_session"

// WebAuth is cookie-based auth middleware for the web UI.
// It reads the JWT from an httpOnly cookie instead of the Authorization header.
// Public paths (login, register, logout) are passed through without auth.
func WebAuth(secret string, db *database.DB, regEnabled bool, next http.Handler) http.Handler {
	publicPaths := map[string]bool{
		"/":               true,
		"/login":           true,
		"/logout":          true,
		"/forgot-password": true,
		"/reset-password":  true,
		"/about":           true,
		"/terms":           true,
		"/privacy":         true,
		"/pricing":         true,
	}
	if regEnabled {
		publicPaths["/register"] = true
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Skip auth for public pages
		if publicPaths[r.URL.Path] {
			next.ServeHTTP(w, r)
			return
		}

		cookie, err := r.Cookie(sessionCookie)
		if err != nil || cookie.Value == "" {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		token := cookie.Value

		// Path 1: static secret_key
		if subtle.ConstantTimeCompare([]byte(token), []byte(secret)) == 1 {
			id := &middleware.Identity{
				UserID:      "system",
				Username:    "system",
				RoleName:    "admin",
				Permissions: []string{"*"},
			}
			ctx := context.WithValue(r.Context(), webIdentityKey, id)
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}

		// Path 2: JWT
		claims := &jwt.RegisteredClaims{}
		parsed, err := jwt.ParseWithClaims(token, claims, func(t *jwt.Token) (any, error) {
			if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, jwt.ErrSignatureInvalid
			}
			return []byte(secret), nil
		})
		if err == nil && parsed.Valid {
			userID := claims.Subject
			if userID == "" {
				clearSession(w)
				http.Redirect(w, r, "/login", http.StatusSeeOther)
				return
			}
			user, err := db.GetUser(userID)
			if err != nil || user == nil || !user.Enabled {
				clearSession(w)
				http.Redirect(w, r, "/login", http.StatusSeeOther)
				return
			}
			perms, _ := db.GetRolePermissions(user.RoleID)
			id := &middleware.Identity{
				UserID:      user.ID,
				Username:    user.Username,
				RoleName:    user.RoleName,
				Permissions: perms,
			}
			ctx := context.WithValue(r.Context(), webIdentityKey, id)
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}

		// Path 3: API key (notalk_ prefix)
		if strings.HasPrefix(token, "notalk_") {
			hash := sha256.Sum256([]byte(token))
			keyHash := hex.EncodeToString(hash[:])
			rec, err := db.GetAPIKeyByHash(keyHash)
			if err != nil || rec == nil || !rec.Enabled {
				clearSession(w)
				http.Redirect(w, r, "/login", http.StatusSeeOther)
				return
			}
			if rec.ExpiresAt != nil {
				if exp, err := time.Parse(time.RFC3339, *rec.ExpiresAt); err == nil && exp.Before(time.Now()) {
					clearSession(w)
					http.Redirect(w, r, "/login", http.StatusSeeOther)
					return
				}
			}
			user, err := db.GetUser(rec.UserID)
			if err != nil || user == nil || !user.Enabled {
				clearSession(w)
				http.Redirect(w, r, "/login", http.StatusSeeOther)
				return
			}
			perms, _ := db.GetRolePermissions(user.RoleID)
			id := &middleware.Identity{
				UserID:      user.ID,
				Username:    user.Username,
				RoleName:    user.RoleName,
				Permissions: perms,
			}
			ctx := context.WithValue(r.Context(), webIdentityKey, id)
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}

		clearSession(w)
		http.Redirect(w, r, "/login", http.StatusSeeOther)
	})
}

type ctxKey string

const webIdentityKey ctxKey = "web_identity"

// getIdentity extracts the web identity from the request context.
func getIdentity(r *http.Request) *middleware.Identity {
	v, _ := r.Context().Value(webIdentityKey).(*middleware.Identity)
	return v
}

// setSession sets the session cookie with the JWT token.
func setSession(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   86400, // 24h
	})
}

// clearSession removes the session cookie.
func clearSession(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

// setFlash stores a flash message in a cookie for the next page load.
func setFlash(w http.ResponseWriter, typ, message string) {
	http.SetCookie(w, &http.Cookie{
		Name:     "notalk_flash",
		Value:    typ + "|" + message,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   10,
	})
}

// getFlash reads and clears the flash cookie.
func getFlash(w http.ResponseWriter, r *http.Request) *Flash {
	c, err := r.Cookie("notalk_flash")
	if err != nil || c.Value == "" {
		return nil
	}
	// Clear immediately
	http.SetCookie(w, &http.Cookie{
		Name:   "notalk_flash",
		Value:  "",
		Path:   "/",
		MaxAge: -1,
	})
	parts := strings.SplitN(c.Value, "|", 2)
	if len(parts) != 2 {
		return nil
	}
	return &Flash{Type: parts[0], Message: parts[1]}
}
