package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	defaultDatabaseURL = "postgres://onefortythree:dev@localhost:5432/onefortythree?sslmode=disable"
	defaultTimeout     = 60 * time.Second
	pollInterval       = time.Second
)

func main() {
	dbURL := databaseURLFromEnv()
	timeout, err := timeoutFromEnv()
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid WAIT_POSTGRES_TIMEOUT: %v\n", err)
		os.Exit(1)
	}
	if err := waitForPostgres(context.Background(), dbURL, timeout); err != nil {
		fmt.Fprintf(os.Stderr, "Postgres did not become ready: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("Postgres is ready.")
}

func databaseURLFromEnv() string {
	return databaseURLFromLookup(os.Getenv)
}

func databaseURLFromLookup(getenv func(string) string) string {
	if dbURL := getenv("DATABASE_URL"); dbURL != "" {
		return dbURL
	}
	return defaultDatabaseURL
}

func timeoutFromEnv() (time.Duration, error) {
	return timeoutFromLookup(os.Getenv)
}

func timeoutFromLookup(getenv func(string) string) (time.Duration, error) {
	value := getenv("WAIT_POSTGRES_TIMEOUT")
	if value == "" {
		return defaultTimeout, nil
	}
	timeout, err := time.ParseDuration(value)
	if err != nil {
		return 0, err
	}
	if timeout <= 0 {
		return 0, errors.New("must be positive")
	}
	return timeout, nil
}

func waitForPostgres(ctx context.Context, dbURL string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		attemptCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		conn, err := pgx.Connect(attemptCtx, dbURL)
		if err == nil {
			err = conn.Ping(attemptCtx)
			closeErr := conn.Close(context.Background())
			if err == nil {
				err = closeErr
			}
		}
		cancel()
		if err == nil {
			return nil
		}
		lastErr = err
		if time.Now().Add(pollInterval).After(deadline) {
			return lastErr
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pollInterval):
		}
	}
}
