package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"
)

func TestServeAndWaitForShutdownNormalClosure(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		serveErr error
	}{
		{name: "server closed", serveErr: http.ErrServerClosed},
		{name: "wrapped server closed", serveErr: fmt.Errorf("listener stopped: %w", http.ErrServerClosed)},
		{name: "nil serve result"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			synctest.Test(t, func(t *testing.T) {
				shutdownDone := make(chan struct{})
				var finishOnce sync.Once
				finishShutdown := func() { finishOnce.Do(func() { close(shutdownDone) }) }
				defer finishShutdown()
				result := make(chan error, 1)
				serveCalls := 0
				go func() {
					result <- serveAndWaitForShutdown(func() error {
						serveCalls++
						return tt.serveErr
					}, shutdownDone)
				}()

				// A quiescent bubble proves the helper has reached its wait without
				// depending on a sleep or the scheduler running it promptly.
				synctest.Wait()
				require.Equal(t, 1, serveCalls, "normal closure should invoke Serve exactly once")
				select {
				case err := <-result:
					t.Fatalf("normal listener closure returned before coordinated finalization: %v", err)
				default:
				}
				finishShutdown()
				require.NoError(t, awaitShutdownTestValue(t, result), "normal closure should return after coordinated finalization")
			})
		})
	}
}

func TestServeAndWaitForShutdownErrorBypassesWait(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		shutdownDone := make(chan struct{})
		defer close(shutdownDone)
		wantErr := errors.New("listener failure")
		result := make(chan error, 1)
		go func() {
			result <- serveAndWaitForShutdown(func() error { return wantErr }, shutdownDone)
		}()
		synctest.Wait()
		select {
		case err := <-result:
			require.Equal(t, wantErr, err, "genuine serving errors should propagate unchanged without a shutdown signal")
		default:
			t.Fatal("genuine serving error waited for coordinated shutdown")
		}
	})
}

func TestServeAndWaitForShutdownAlreadyCompleted(t *testing.T) {
	t.Parallel()

	shutdownDone := make(chan struct{})
	close(shutdownDone)
	serveCalls := 0
	err := serveAndWaitForShutdown(func() error {
		serveCalls++
		return http.ErrServerClosed
	}, shutdownDone)
	require.NoError(t, err, "cleanup completed before Serve returns should not block normal closure")
	require.Equal(t, 1, serveCalls, "completed cleanup should still invoke Serve exactly once")
}

func TestServeAndWaitForShutdownHTTPFinalization(t *testing.T) {
	t.Parallel()

	handlerStarted := make(chan struct{})
	handlerRelease := make(chan struct{})
	finalizerStarted := make(chan struct{})
	finalizerRelease := make(chan struct{})
	var handlerOnce, finalizerOnce sync.Once
	releaseHandler := func() { handlerOnce.Do(func() { close(handlerRelease) }) }
	releaseFinalizer := func() { finalizerOnce.Do(func() { close(finalizerRelease) }) }
	requestCtx, cancelRequest := context.WithCancel(context.Background())
	server := &http.Server{
		ReadHeaderTimeout: 5 * time.Second,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			close(handlerStarted)
			<-handlerRelease
			w.WriteHeader(http.StatusNoContent)
		}),
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err, "the shutdown regression should open an isolated loopback listener")
	transport := &http.Transport{DisableKeepAlives: true}
	client := &http.Client{Transport: transport, Timeout: 10 * time.Second}
	serveReturned := make(chan error, 1)
	shutdownDone := make(chan struct{})
	var shutdownOnce sync.Once
	finishShutdown := func() { shutdownOnce.Do(func() { close(shutdownDone) }) }
	helperResult := make(chan error, 1)
	helperStopped := make(chan struct{})
	clientStopped := make(chan struct{})
	shutdownStopped := make(chan struct{})
	shutdownStarted := false
	t.Cleanup(func() {
		releaseHandler()
		releaseFinalizer()
		cancelRequest()
		closeErr := server.Close()
		finishShutdown()
		transport.CloseIdleConnections()
		awaitShutdownTestValue(t, helperStopped)
		awaitShutdownTestValue(t, clientStopped)
		if shutdownStarted {
			awaitShutdownTestValue(t, shutdownStopped)
		}
		require.NoError(t, closeErr, "test cleanup should close the loopback HTTP server")
	})
	go func() {
		defer close(helperStopped)
		helperResult <- serveAndWaitForShutdown(func() error {
			serveErr := server.Serve(listener)
			serveReturned <- serveErr
			return serveErr
		}, shutdownDone)
	}()
	type responseResult struct {
		status int
		err    error
	}
	clientResult := make(chan responseResult, 1)
	go func() {
		defer close(clientStopped)
		req, requestErr := http.NewRequestWithContext(requestCtx, http.MethodGet, "http://"+listener.Addr().String(), nil)
		if requestErr != nil {
			clientResult <- responseResult{err: requestErr}
			return
		}
		resp, requestErr := client.Do(req)
		if requestErr != nil {
			clientResult <- responseResult{err: requestErr}
			return
		}
		_, readErr := io.Copy(io.Discard, resp.Body)
		closeErr := resp.Body.Close()
		clientResult <- responseResult{status: resp.StatusCode, err: errors.Join(readErr, closeErr)}
	}()
	awaitShutdownTestValue(t, handlerStarted)
	shutdownResult := make(chan error, 1)
	shutdownStarted = true
	go func() {
		defer close(shutdownStopped)
		defer finishShutdown()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		shutdownErr := server.Shutdown(shutdownCtx)
		close(finalizerStarted)
		<-finalizerRelease
		shutdownResult <- shutdownErr
	}()
	require.ErrorIs(t, awaitShutdownTestValue(t, serveReturned), http.ErrServerClosed, "HTTP Serve should return when shutdown closes its listener")
	select {
	case <-finalizerStarted:
		t.Fatal("HTTP Shutdown completed while the active handler was still blocked")
	default:
	}
	releaseHandler()
	awaitShutdownTestValue(t, finalizerStarted)
	select {
	case err := <-helperResult:
		t.Fatalf("Serve helper returned before the coordinated finalizer completed: %v", err)
	default:
	}
	releaseFinalizer()
	require.NoError(t, awaitShutdownTestValue(t, helperResult), "HTTP listener closure should join coordinated finalization")
	require.NoError(t, awaitShutdownTestValue(t, shutdownResult), "the active handler should complete within the existing HTTP shutdown deadline")
	require.Equal(t, responseResult{status: http.StatusNoContent}, awaitShutdownTestValue(t, clientResult), "shutdown should allow the admitted HTTP request to finish normally")
}

func awaitShutdownTestValue[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(15 * time.Second):
		t.Fatal("timed out waiting for the shutdown test barrier")
		var zero T
		return zero
	}
}
