package middleware

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	jwt "github.com/golang-jwt/jwt/v5"
)

type AuthConfig struct {
	Enabled        bool
	HMACSecret     string
	Issuer         string
	Audience       string
	ScopeClaim     string
	OptionalPaths  []string
	AllowAnonymous bool
	ClockSkew      time.Duration
}

type contextKey string

const (
	ContextKeyToken  contextKey = "gateway.token"
	ContextKeyScopes contextKey = "gateway.scopes"
)

type Authenticator struct {
	cfg           AuthConfig
	logger        *log.Logger
	secret        []byte
	optionalPaths []string
	once          sync.Once
}

func NewAuthenticator(cfg AuthConfig, logger *log.Logger) *Authenticator {
	if logger == nil {
		logger = log.Default()
	}
	auth := &Authenticator{cfg: cfg, logger: logger}
	auth.once.Do(func() {
		auth.secret = []byte(strings.TrimSpace(cfg.HMACSecret))
		if auth.cfg.ScopeClaim == "" {
			auth.cfg.ScopeClaim = "scope"
		}
		if auth.cfg.ClockSkew <= 0 {
			auth.cfg.ClockSkew = 2 * time.Minute
		}
	})
	return auth
}

func (a *Authenticator) Middleware(requiredScopes ...string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !a.cfg.Enabled {
				next.ServeHTTP(w, r)
				return
			}
			if a.isOptional(r.URL.Path) && a.cfg.AllowAnonymous {
				next.ServeHTTP(w, r)
				return
			}
			tokenString := extractBearer(r.Header.Get("Authorization"))
			if tokenString == "" {
				http.Error(w, "missing bearer token", http.StatusUnauthorized)
				return
			}
			claims, err := a.parseToken(tokenString)
			if err != nil {
				a.logger.Printf("auth: token validation failed: %v", err)
				http.Error(w, "invalid token", http.StatusUnauthorized)
				return
			}
			if err := validateClaims(claims, a.cfg.Issuer, a.cfg.Audience); err != nil {
				a.logger.Printf("auth: claim validation failed: %v", err)
				http.Error(w, "invalid token", http.StatusUnauthorized)
				return
			}
			scopes := extractScopes(claims, a.cfg.ScopeClaim)
			if len(requiredScopes) > 0 && !hasScopes(scopes, requiredScopes) {
				http.Error(w, "insufficient scope", http.StatusForbidden)
				return
			}
			ctx := context.WithValue(r.Context(), ContextKeyToken, tokenString)
			ctx = context.WithValue(ctx, ContextKeyScopes, scopes)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func (a *Authenticator) isOptional(path string) bool {
	for _, prefix := range a.cfg.OptionalPaths {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}

func (a *Authenticator) parseToken(tokenString string) (jwt.MapClaims, error) {
	if len(a.secret) == 0 {
		return nil, errors.New("auth secret not configured")
	}
	token, err := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, errors.New("unexpected signing method")
		}
		return a.secret, nil
	}, jwt.WithLeeway(a.cfg.ClockSkew))
	if err != nil {
		return nil, err
	}
	if !token.Valid {
		return nil, errors.New("token invalid")
	}
	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return nil, errors.New("claims not map")
	}
	return claims, nil
}

func validateClaims(claims jwt.MapClaims, issuer, audience string) error {
	if issuer != "" {
		if value, ok := claims["iss"].(string); !ok || value != issuer {
			return errors.New("issuer mismatch")
		}
	}
	if audience != "" {
		switch val := claims["aud"].(type) {
		case string:
			if val != audience {
				return errors.New("audience mismatch")
			}
		case []interface{}:
			matched := false
			for _, entry := range val {
				if s, ok := entry.(string); ok && s == audience {
					matched = true
					break
				}
			}
			if !matched {
				return errors.New("audience mismatch")
			}
		}
	}
	if exp, ok := claims["exp"].(float64); ok {
		if int64(exp) < time.Now().Unix() {
			return errors.New("token expired")
		}
	}
	return nil
}

func extractScopes(claims jwt.MapClaims, scopeClaim string) []string {
	if scopeClaim == "" {
		scopeClaim = "scope"
	}
	raw, ok := claims[scopeClaim]
	if !ok {
		return nil
	}
	switch v := raw.(type) {
	case string:
		trimmed := strings.TrimSpace(v)
		if trimmed == "" {
			return nil
		}
		fields := strings.Fields(trimmed)
		out := make([]string, 0, len(fields))
		out = append(out, fields...)
		return out
	case []interface{}:
		out := make([]string, 0, len(v))
		for _, entry := range v {
			if s, ok := entry.(string); ok {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

func hasScopes(scopes []string, required []string) bool {
	if len(required) == 0 {
		return true
	}
	set := make(map[string]struct{}, len(scopes))
	for _, scope := range scopes {
		set[scope] = struct{}{}
	}
	for _, req := range required {
		if _, ok := set[req]; !ok {
			return false
		}
	}
	return true
}

func extractBearer(header string) string {
	if header == "" {
		return ""
	}
	parts := strings.SplitN(header, " ", 2)
	if len(parts) != 2 {
		return ""
	}
	if !strings.EqualFold(parts[0], "Bearer") {
		return ""
	}
	return strings.TrimSpace(parts[1])
}
