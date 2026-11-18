package main

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"testing/synctest"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/lifecyclewriter"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

type serverWriterStoreFunc func(context.Context, uuid.UUID, models.LifecycleWriterRole, string) error

func (f serverWriterStoreFunc) Observe(ctx context.Context, id uuid.UUID, role models.LifecycleWriterRole, build string) error {
	return f(ctx, id, role, build)
}

func TestServerWriterObservationShutdownLifetime(t *testing.T) {
	t.Parallel()
	tests := []struct {
		mode string
		role models.LifecycleWriterRole
	}{
		{mode: "api", role: models.LifecycleWriterRoleAPI},
		{mode: "worker", role: models.LifecycleWriterRoleWorker},
		{mode: "all", role: models.LifecycleWriterRoleAll},
	}
	for _, tt := range tests {
		t.Run(tt.mode, func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				observed := make(chan context.Context, 1)
				observationCanceled := make(chan struct{})
				allowObservationExit := make(chan struct{})
				observationExited := make(chan struct{})
				type identity struct {
					id    uuid.UUID
					role  models.LifecycleWriterRole
					build string
				}
				identities := make(chan identity, 1)
				store := serverWriterStoreFunc(func(ctx context.Context, id uuid.UUID, role models.LifecycleWriterRole, build string) error {
					defer close(observationExited)
					identities <- identity{id: id, role: role, build: build}
					observed <- ctx
					<-ctx.Done()
					close(observationCanceled)
					<-allowObservationExit
					return ctx.Err()
				})
				stop := lifecyclewriter.StartServer(store, tt.mode, "synthetic-build", zerolog.Nop())
				shutdownDone := make(chan struct{})
				finishShutdown := sync.OnceFunc(func() { close(shutdownDone) })
				finishObservation := sync.OnceFunc(func() { close(allowObservationExit) })
				defer func() {
					finishShutdown()
					finishObservation()
					stop()
					synctest.Wait()
				}()
				observationCtx := <-observed
				actualIdentity := <-identities
				require.NotEqual(t, uuid.Nil, actualIdentity.id, "the server must use an independent process identity during finalization")
				require.Equal(t, identity{id: actualIdentity.id, role: tt.role, build: "synthetic-build"}, actualIdentity, "shutdown must retain configured role and actual binary identity")
				returned := make(chan error, 1)
				events := make(chan string, 4)
				poolClosed := make(chan struct{})
				go func() {
					defer func() {
						events <- "pool closed"
						close(poolClosed)
					}()
					defer func() {
						stop()
						events <- "observer joined"
					}()
					err := serveAndWaitForShutdown(func() error {
						events <- "listener closed"
						return http.ErrServerClosed
					}, shutdownDone)
					returned <- err
				}()
				synctest.Wait()
				require.Equal(t, "listener closed", <-events, "Serve returning must not itself end the observer lifetime")
				require.NoError(t, observationCtx.Err(), "observation must remain live while coordinated finalization is blocked")
				select {
				case <-returned:
					t.Fatal("normal server return must wait for coordinated finalization")
				default:
				}
				events <- "finalization finished"
				finishShutdown()
				synctest.Wait()
				<-observationCanceled
				select {
				case <-poolClosed:
					t.Fatal("ordinary pool closed while the canceled observation was still finishing")
				default:
				}
				finishObservation()
				synctest.Wait()
				require.NoError(t, <-returned, "completed graceful shutdown should preserve successful server return")
				require.Equal(t, []string{"finalization finished", "observer joined", "pool closed"}, []string{<-events, <-events, <-events}, "observer cancellation/join must follow finalization and precede ordinary-pool closure")
				require.ErrorIs(t, observationCtx.Err(), context.Canceled, "joining must cancel the in-flight observation, not rely on its TTL or attempt deadline")
				select {
				case <-observationExited:
				default:
					t.Fatal("pool closure must not race an in-flight observation")
				}
			})
		})
	}
}
