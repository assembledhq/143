package handlers

import (
	"context"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/version"
)

type HealthHandler struct {
	pool        *pgxpool.Pool
	redisHealth func(context.Context) bool
	draining    <-chan struct{}
}

func NewHealthHandler(pool *pgxpool.Pool) *HealthHandler {
	return &HealthHandler{pool: pool}
}

func (h *HealthHandler) SetRedisHealthCheck(check func(context.Context) bool) {
	h.redisHealth = check
}

func (h *HealthHandler) SetDrainingSignal(draining <-chan struct{}) {
	h.draining = draining
}

func (h *HealthHandler) Healthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if h.isDraining() {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"status":"draining"}`))
		return
	}
	redisStatus := "unavailable"
	if h.redisHealth != nil {
		ctx, cancel := context.WithTimeout(r.Context(), time.Second)
		defer cancel()
		if h.redisHealth(ctx) {
			redisStatus = "ok"
		}
	}
	_, _ = w.Write([]byte(`{"status":"ok","redis":"` + redisStatus + `"}`))
}

func (h *HealthHandler) isDraining() bool {
	if h.draining == nil {
		return false
	}
	select {
	case <-h.draining:
		return true
	default:
		return false
	}
}

// Version returns the server deploy SHA and build metadata.
func (h *HealthHandler) Version(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{
		"deploy_sha": version.BuildSHA,
	})
}

func (h *HealthHandler) Readyz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if err := h.pool.Ping(r.Context()); err != nil {
		zerolog.Ctx(r.Context()).Warn().Err(err).Msg("readiness check failed: database unavailable")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"status":"not ready","error":"database unavailable"}`))
		return
	}
	_, _ = w.Write([]byte(`{"status":"ready"}`))
}
