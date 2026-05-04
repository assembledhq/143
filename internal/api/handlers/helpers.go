package handlers

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

// encodeCursor produces an opaque, base64-encoded cursor from a created_at
// timestamp and a string-encoded ID. Format: "RFC3339Nano,id" → base64.
func encodeCursor(createdAt time.Time, id string) string {
	raw := fmt.Sprintf("%s,%s", createdAt.UTC().Format(time.RFC3339Nano), id)
	return base64.StdEncoding.EncodeToString([]byte(raw))
}

// decodeCursor is the inverse of encodeCursor. It returns the timestamp and the
// raw ID string; callers are responsible for parsing the ID into its final type.
func decodeCursor(cursor string) (time.Time, string, error) {
	b, err := base64.StdEncoding.DecodeString(cursor)
	if err != nil {
		return time.Time{}, "", fmt.Errorf("invalid cursor encoding: %w", err)
	}
	parts := strings.SplitN(string(b), ",", 2)
	if len(parts) != 2 {
		return time.Time{}, "", fmt.Errorf("invalid cursor format")
	}
	t, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return time.Time{}, "", fmt.Errorf("invalid cursor timestamp: %w", err)
	}
	return t, parts[1], nil
}

func queryInt(r *http.Request, key string, defaultVal int) int {
	v := r.URL.Query().Get(key)
	if v == "" {
		return defaultVal
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return defaultVal
	}
	return n
}

// clampListLimit bounds a page-size request to [1, maxLimit], substituting the
// default when zero/negative. Keeping the clamp in the handler (not silently
// inside the store) means the value passed to the store equals the value used
// to compute next_cursor — otherwise a caller who asked for limit=1000 could
// get back 25 rows with no cursor and believe there were no more pages.
func clampListLimit(requested, defaultLimit, maxLimit int) int {
	if requested <= 0 {
		return defaultLimit
	}
	if requested > maxLimit {
		return maxLimit
	}
	return requested
}

func parseUUIDList(raw string) ([]uuid.UUID, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, fmt.Errorf("no valid UUIDs provided")
	}

	parts := strings.Split(raw, ",")
	ids := make([]uuid.UUID, 0, len(parts))
	seen := make(map[uuid.UUID]struct{}, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		id, err := uuid.Parse(part)
		if err != nil {
			return nil, err
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("no valid UUIDs provided")
	}
	return ids, nil
}

func strPtr(s string) *string { return &s }

// clearWriteDeadline disables the global http.Server WriteTimeout for a
// single in-flight request. Use it at the top of handlers whose work can
// legitimately exceed the server-wide WriteTimeout — preview start in
// particular blends snapshot restore (~10s), infra image pull (~30s), and
// readiness probes (90s default), and the 15s server WriteTimeout will
// otherwise drop the connection mid-handler. The dropped connection
// surfaces to the API caller as `EOF` and to the user as a 502 with no
// usable error code, hiding the real failure.
//
// Scoped per-response: Go's http.Server re-arms WriteTimeout on the next
// request that arrives on the same keep-alive connection. The request
// context's own deadline (driven by the client connection or the
// worker_client's 10-minute timeout) still bounds the handler.
//
// Stop / StopActivePreviewForSession deliberately do NOT clear the
// deadline: those paths only signal an existing container and complete
// well within the 15s budget; keeping the default catches a genuinely
// stuck stop instead of letting it hang the connection.
//
// Logs at Debug and returns silently on response writers that don't
// support http.ResponseController — this is a perf/UX optimization, not
// a correctness requirement, so degrading to the default WriteTimeout
// is acceptable for those cases.
func clearWriteDeadline(w http.ResponseWriter, r *http.Request) {
	if err := http.NewResponseController(w).SetWriteDeadline(time.Time{}); err != nil {
		zerolog.Ctx(r.Context()).Debug().Err(err).Msg("could not clear write deadline; continuing with server default")
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	// Encode directly to the response writer. If encoding fails, the status
	// header is already sent so we can only log — http.Error would attempt a
	// second WriteHeader which is a no-op and prints a warning.
	_ = json.NewEncoder(w).Encode(v)
}

// writeError logs the error and writes a JSON error response. It logs at Error
// level for 5xx status codes and Info level for 4xx. If an error is provided
// via errs, it is attached to the log entry with .Err().
func writeError(w http.ResponseWriter, r *http.Request, status int, code, message string, errs ...error) {
	logger := zerolog.Ctx(r.Context()).With().Str("code", code).Int("status", status).Logger()
	var evt *zerolog.Event
	if status >= 500 {
		evt = logger.Error()
	} else {
		evt = logger.Info()
	}
	if len(errs) > 0 && errs[0] != nil {
		evt = evt.Err(errs[0])
	}
	evt.Msg(message)

	writeJSON(w, status, models.ErrorResponse{
		Error: models.ErrorDetail{Code: code, Message: message},
	})
}
