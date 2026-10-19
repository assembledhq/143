package main

import (
	"errors"
	"net/http"
)

// serveAndWaitForShutdown joins the existing coordinated cleanup after normal
// listener closure. A genuine serving failure must not wait for a signal that
// may never arrive. Joining cleanup does not establish that all work drained.
func serveAndWaitForShutdown(serve func() error, shutdownDone <-chan struct{}) error {
	if err := serve(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	<-shutdownDone
	return nil
}
