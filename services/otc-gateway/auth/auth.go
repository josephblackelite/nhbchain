package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// Context keys for storing authenticated user information.
type contextKey string

const (
	contextKeyUserID contextKey = "user_id"
	contextKeyRole   contextKey = "user_role"
)

// Role represents an authorized persona within the OTC system.
type Role string

// Supported roles for the OTC service.
const (
	RoleTeller     Role = "teller"
	RoleSupervisor Role = "supervisor"
	RoleCompliance Role = "compliance"
	RoleSuperAdmin Role = "superadmin"
	RoleAuditor    Role = "auditor"
)

var allowedRoles = map[Role]struct{}{
	RoleTeller:     {},
	RoleSupervisor: {},
	RoleCompliance: {},
	RoleSuperAdmin: {},
	RoleAuditor:    {},
}

// Claims represents identity data extracted from the inbound request.
type Claims struct {
	Subject string
	Role    Role
}

// FromContext extracts the Claims previously attached by Authenticate middleware.
func FromContext(ctx context.Context) (*Claims, error) {
	userID, ok := ctx.Value(contextKeyUserID).(string)
	if !ok || userID == "" {
		return nil, errors.New("missing user id in context")
	}
	roleStr, ok := ctx.Value(contextKeyRole).(string)
	if !ok || roleStr == "" {
		return nil, errors.New("missing role in context")
	}
	return &Claims{Subject: userID, Role: Role(roleStr)}, nil
}

// Authenticate validates pseudo OIDC + WebAuthn headers to gate the handler execution.
//
// In production this logic should be replaced with a real OIDC validator and WebAuthn
// verification library. The current implementation expects:
//
//	Authorization: Bearer <subject>|<role>
//	X-WebAuthn-Verified: true
//
// The middleware ensures the role is a known staff persona and that WebAuthn attestation
// succeeded for privileged actions.
func Authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authz := r.Header.Get("Authorization")
		if authz == "" {
			http.Error(w, "missing authorization", http.StatusUnauthorized)
			return
		}

		parts := strings.SplitN(authz, " ", 2)
		if len(parts) != 2 || !strings.EqualFold(parts[0], "bearer") {
			http.Error(w, "invalid authorization scheme", http.StatusUnauthorized)
			return
		}

		tokenParts := strings.SplitN(parts[1], "|", 2)
		if len(tokenParts) != 2 {
			http.Error(w, "invalid token format", http.StatusUnauthorized)
			return
		}

		subject := tokenParts[0]
		role := Role(strings.ToLower(tokenParts[1]))
		if _, ok := allowedRoles[role]; !ok {
			http.Error(w, fmt.Sprintf("unauthorized role %s", role), http.StatusForbidden)
			return
		}

		if strings.ToLower(r.Header.Get("X-WebAuthn-Verified")) != "true" {
			http.Error(w, "multi-factor authentication required", http.StatusUnauthorized)
			return
		}

		ctx := context.WithValue(r.Context(), contextKeyUserID, subject)
		ctx = context.WithValue(ctx, contextKeyRole, string(role))
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// RequireRole ensures the authenticated user has at least one of the allowed roles.
func RequireRole(roles ...Role) func(http.Handler) http.Handler {
	allowed := make(map[Role]struct{}, len(roles))
	for _, r := range roles {
		allowed[r] = struct{}{}
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims, err := FromContext(r.Context())
			if err != nil {
				http.Error(w, "missing identity", http.StatusUnauthorized)
				return
			}
			if _, ok := allowed[claims.Role]; !ok {
				http.Error(w, "insufficient role", http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
