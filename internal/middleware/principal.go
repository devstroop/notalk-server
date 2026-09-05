package middleware

import (
	"context"
	"net/http"
)

// Principal represents an authenticated caller — the convergence point
// of all credential mechanisms. Handlers must never inspect where it came
// from; authorization consumes Scopes/Roles only.
//
// Invariant: route ≠ authentication mechanism ≠ authorization policy.
// Authn is metadata (observability), not authority.
type Principal struct {
	// Identity
	UserID   string `json:"user_id"`
	TenantID string `json:"tenant_id"` // "" today; future user.tenant_id
	Username string `json:"username"`

	// Authorities — canonical authorization source
	Roles  []string `json:"roles"`  // role names, e.g. ["admin"]
	Scopes []string `json:"scopes"` // permissions, e.g. ["accounts:read","messages:*","*"]

	// Authn — metadata only. For audit/logging (`authn=api_key`), never for
	// authorization branching. Do NOT: if p.Authn=="secret" { allow }
	Authn string `json:"authn"` // "secret"|"jwt"|"api_key"|"session"|"service"|""

	// ScopedAccountID is set for account-bound API keys or MCPScope ?account_id.
	ScopedAccountID *string `json:"-"`

	// Legacy: RoleName is deprecated alias for Roles[0]; retained for compat
	// during Identity→Principal migration. New code uses Roles/Scopes.
	RoleName string `json:"role_name"`
}

// HasPermission mirrors Identity.HasPermission but on the canonical Scopes.
func (p *Principal) HasPermission(required string) bool {
	return hasPermission(p.Scopes, required)
}

// hasPermission is shared with Identity.HasPermission to keep parity during
// migration; both delegate to this single implementation.
func hasPermission(scopes []string, required string) bool {
	for _, p := range scopes {
		if p == "*" {
			return true
		}
		if p == required {
			return true
		}
		if len(p) > 2 && p[len(p)-2] == ':' && p[len(p)-1] == '*' {
			// e.g. "messages:*" matches "messages:read"
			prefix := p[:len(p)-1]
			if len(required) >= len(prefix) && required[:len(prefix)] == prefix {
				return true
			}
		}
	}
	return false
}

// Principal context key — single convergence point.
// During migration Identity and Principal coexist; both use identityKey until
// Identity is removed. Web and API share this key.
const principalKey contextKey = "principal"

// GetPrincipal extracts the Principal from the request context.
// Returns nil if unauthenticated or on legacy Identity-only contexts.
func GetPrincipal(r *http.Request) *Principal {
	if v, _ := r.Context().Value(principalKey).(*Principal); v != nil {
		return v
	}
	// Compatibility shim: promote legacy Identity to Principal for reads
	// during P0 before all Authenticators are wired. Remove after migration.
	if id := GetIdentity(r); id != nil {
		return promoteIdentity(id, r.Context())
	}
	return nil
}

// GetPrincipalFromContext extracts Principal from raw context.
func GetPrincipalFromContext(ctx context.Context) *Principal {
	if v, _ := ctx.Value(principalKey).(*Principal); v != nil {
		return v
	}
	if v, _ := ctx.Value(identityKey).(*Identity); v != nil {
		// promote with no ScopedAccount lookup — caller can call GetScopedAccountID separately
		return &Principal{
			UserID:   v.UserID,
			Username: v.Username,
			RoleName: v.RoleName,
			Roles:    rolesFromName(v.RoleName),
			Scopes:   v.Permissions,
			Authn:    "",
		}
	}
	return nil
}

func rolesFromName(name string) []string {
	if name == "" {
		return nil
	}
	return []string{name}
}

func promoteIdentity(id *Identity, ctx context.Context) *Principal {
	scoped := GetScopedAccountID(ctx)
	var scopedPtr *string
	if scoped != "" {
		scopedPtr = &scoped
	}
	return &Principal{
		UserID:          id.UserID,
		Username:        id.Username,
		RoleName:        id.RoleName,
		Roles:           rolesFromName(id.RoleName),
		Scopes:          id.Permissions,
		Authn:           "", // unknown when promoted from legacy Identity
		ScopedAccountID: scopedPtr,
	}
}

// withPrincipal stores Principal on context and, for migration,
// mirrors to legacy identityKey so existing RequirePermission still works.
func withPrincipal(ctx context.Context, p *Principal) context.Context {
	ctx = context.WithValue(ctx, principalKey, p)
	// Mirror to legacy Identity for backward compat during P0/P1
	ctx = context.WithValue(ctx, identityKey, &Identity{
		UserID:      p.UserID,
		Username:    p.Username,
		RoleName:    p.RoleName,
		Permissions: p.Scopes,
	})
	if p.ScopedAccountID != nil && *p.ScopedAccountID != "" {
		ctx = context.WithValue(ctx, scopedAccountKey, *p.ScopedAccountID)
	}
	return ctx
}

// Ensure withPrincipal is considered used for lint (P0 migration, wired in next PR).
var _ = withPrincipal
