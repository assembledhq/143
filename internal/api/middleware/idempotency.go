package middleware

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"
)

type captureResponseWriter struct {
	http.ResponseWriter
	status int
	body   bytes.Buffer
}

func (w *captureResponseWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *captureResponseWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	w.body.Write(b)
	return w.ResponseWriter.Write(b)
}

func ExternalAPIIdempotency(store *db.APIIdempotencyStore, logger zerolog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if store == nil || APITokenFromContext(r.Context()) == nil || !isIdempotentMutation(r) {
				next.ServeHTTP(w, r)
				return
			}
			key := r.Header.Get("Idempotency-Key")
			if key == "" {
				next.ServeHTTP(w, r)
				return
			}
			client := APIClientFromContext(r.Context())
			token := APITokenFromContext(r.Context())
			body, err := io.ReadAll(r.Body)
			if err != nil {
				writeError(w, http.StatusBadRequest, "INVALID_BODY", "failed to read request body")
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
			sum := sha256.Sum256(body)
			bodyHash := "sha256:" + hex.EncodeToString(sum[:])
			path := r.URL.Path

			existing, err := store.Get(r.Context(), client.OrgID, client.ID, r.Method, path, key)
			if err == nil {
				if existing.RequestBodyHash != bodyHash {
					writeError(w, http.StatusConflict, "IDEMPOTENCY_KEY_REUSED", "idempotency key was reused with different request content")
					return
				}
				if existing.ResponseStatus != nil {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(*existing.ResponseStatus)
					if _, writeErr := w.Write(existing.ResponseBody); writeErr != nil {
						logger.Warn().Err(writeErr).Msg("failed to replay idempotent response")
					}
					return
				}
			} else if !errors.Is(err, pgx.ErrNoRows) {
				logger.Warn().Err(err).Msg("idempotency lookup failed")
				writeError(w, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "idempotency lookup failed")
				return
			}
			if errors.Is(err, pgx.ErrNoRows) {
				inserted, createErr := store.Create(r.Context(), client.OrgID, client.ID, token.ID, r.Method, path, key, bodyHash, time.Now().Add(24*time.Hour))
				if createErr != nil {
					logger.Warn().Err(createErr).Msg("idempotency create failed")
					writeError(w, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "idempotency create failed")
					return
				}
				if !inserted {
					if replayed := waitForIdempotentResponse(w, r, store, logger, client.OrgID, client.ID, r.Method, path, key, bodyHash); replayed {
						return
					}
				}
			} else if existing.ResponseStatus == nil {
				if replayed := waitForIdempotentResponse(w, r, store, logger, client.OrgID, client.ID, r.Method, path, key, bodyHash); replayed {
					return
				}
			}

			capture := &captureResponseWriter{ResponseWriter: w}
			next.ServeHTTP(capture, r)
			status := capture.status
			if status == 0 {
				status = http.StatusOK
			}
			if saveErr := store.SaveResponse(r.Context(), client.OrgID, client.ID, r.Method, path, key, status, capture.body.Bytes()); saveErr != nil {
				logger.Warn().Err(saveErr).Msg("failed to save idempotent response")
			}
		})
	}
}

func waitForIdempotentResponse(w http.ResponseWriter, r *http.Request, store *db.APIIdempotencyStore, logger zerolog.Logger, orgID, clientID uuid.UUID, method, path, key, bodyHash string) bool {
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
		existing, err := store.Get(r.Context(), orgID, clientID, method, path, key)
		if err != nil {
			if !errors.Is(err, pgx.ErrNoRows) {
				logger.Warn().Err(err).Msg("idempotency wait lookup failed")
				writeError(w, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "idempotency lookup failed")
				return true
			}
			continue
		}
		if existing.RequestBodyHash != bodyHash {
			writeError(w, http.StatusConflict, "IDEMPOTENCY_KEY_REUSED", "idempotency key was reused with different request content")
			return true
		}
		if existing.ResponseStatus != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(*existing.ResponseStatus)
			if _, writeErr := w.Write(existing.ResponseBody); writeErr != nil {
				logger.Warn().Err(writeErr).Msg("failed to replay idempotent response")
			}
			return true
		}
	}
	writeError(w, http.StatusConflict, "IDEMPOTENCY_KEY_IN_PROGRESS", "idempotency key is already processing")
	return true
}

func isIdempotentMutation(r *http.Request) bool {
	if r.Method != http.MethodPost {
		return false
	}
	path := r.URL.Path
	switch {
	case path == "/api/v1/sessions":
		return true
	case path == "/api/v1/automations":
		return true
	case strings.HasSuffix(path, "/messages") && strings.HasPrefix(path, "/api/v1/sessions/"):
		return true
	case (strings.HasSuffix(path, "/pr") || strings.HasSuffix(path, "/branch")) && strings.HasPrefix(path, "/api/v1/sessions/"):
		return true
	case strings.HasSuffix(path, "/run") && strings.HasPrefix(path, "/api/v1/automations/"):
		return true
	case path == "/api/v1/previews":
		return true
	case strings.HasSuffix(path, "/restart") && strings.HasPrefix(path, "/api/v1/previews/"):
		return true
	default:
		return false
	}
}
