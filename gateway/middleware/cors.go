package middleware

import "net/http"

type CORSConfig struct {
	AllowedOrigins   []string
	AllowedMethods   []string
	AllowedHeaders   []string
	AllowCredentials bool
}

func CORS(cfg CORSConfig) func(http.Handler) http.Handler {
	origins := cfg.AllowedOrigins
	if len(origins) == 0 {
		origins = []string{"*"}
	}
	methods := cfg.AllowedMethods
	if len(methods) == 0 {
		methods = []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"}
	}
	headers := cfg.AllowedHeaders
	if len(headers) == 0 {
		headers = []string{"Content-Type", "Authorization", "X-Requested-With"}
	}
	allowCredentials := "false"
	if cfg.AllowCredentials {
		allowCredentials = "true"
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			for _, origin := range origins {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				break
			}
			w.Header().Set("Access-Control-Allow-Methods", join(methods))
			w.Header().Set("Access-Control-Allow-Headers", join(headers))
			w.Header().Set("Access-Control-Allow-Credentials", allowCredentials)
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func join(values []string) string {
	if len(values) == 0 {
		return ""
	}
	out := values[0]
	for i := 1; i < len(values); i++ {
		out += ", " + values[i]
	}
	return out
}
