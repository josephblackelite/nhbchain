package middleware

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"nhbchain/services/otc-gateway/models"
)

// ContextKeyIDKey stores the idempotency key associated with the request.
type ContextKeyIDKey string

const contextKeyIdempotency ContextKeyIDKey = "idempotency-key"

// WithIdempotency ensures requests with the same key are executed once.
func WithIdempotency(db *gorm.DB, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("Idempotency-Key")
		if key == "" {
			next.ServeHTTP(w, r)
			return
		}

		var record models.IdempotencyKey
		if err := db.First(&record, "key = ?", key).Error; err == nil {
			for k, values := range map[string][]string{
				"Content-Type": {"application/json"},
			} {
				for _, v := range values {
					w.Header().Add(k, v)
				}
			}
			w.WriteHeader(record.Status)
			_, _ = io.WriteString(w, record.Response)
			return
		}

		recorder := &responseRecorder{ResponseWriter: w}
		ctx := context.WithValue(r.Context(), contextKeyIdempotency, key)
		next.ServeHTTP(recorder, r.WithContext(ctx))

		payload := models.IdempotencyKey{
			Key:       key,
			RequestID: uuid.NewString(),
			Method:    r.Method,
			Path:      r.URL.Path,
			Status:    recorder.status,
			Response:  recorder.buf,
			CreatedAt: time.Now(),
		}
		if payload.Status == 0 {
			payload.Status = http.StatusOK
		}
		_ = db.Create(&payload).Error
	})
}

// responseRecorder captures the response for idempotent operations.
type responseRecorder struct {
	http.ResponseWriter
	buf    string
	status int
}

func (rr *responseRecorder) WriteHeader(status int) {
	rr.status = status
	rr.ResponseWriter.WriteHeader(status)
}

func (rr *responseRecorder) Write(b []byte) (int, error) {
	rr.buf += string(b)
	return rr.ResponseWriter.Write(b)
}

// SerializeResponse stores the payload to be saved as the idempotent response.
func SerializeResponse(v any) string {
	data, _ := json.Marshal(v)
	return string(data)
}
