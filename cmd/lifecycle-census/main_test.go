package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/services/lifecyclecensus"
	"github.com/stretchr/testify/require"
)

func censusReportFixture() lifecyclecensus.Report {
	return lifecyclecensus.Report{
		SchemaVersion: 1,
		Limitations:   []string{"observational_only"},
		Writers: lifecyclecensus.WriterSummary{
			ObservedAt:   time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
			Truncated:    true,
			CountsScope:  lifecyclecensus.CountsSample,
			RowsObserved: 1,
			Groups:       []lifecyclecensus.WriterGroup{{Role: "unknown", Build: "unknown", Version: 0, Count: 1}},
		},
		Connections: lifecyclecensus.ConnectionSummary{
			ObservedAt:   time.Date(2026, 1, 2, 3, 4, 6, 0, time.UTC),
			Visibility:   lifecyclecensus.VisibilityRestricted,
			CountsScope:  lifecyclecensus.CountsObserved,
			RowsObserved: 1,
			Groups: []lifecyclecensus.ConnectionGroup{{
				Role: "unknown", Build: "unknown", State: lifecyclecensus.StateUnknown, Count: 1, UnknownTransactionDetails: 1,
			}},
		},
	}
}

func TestRunRejectsConfigurationAndArguments(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		args     []string
		value    string
		expected string
	}{
		{name: "missing explicit database URL", expected: "{\"level\":\"error\",\"code\":\"invalid_configuration\",\"message\":\"DATABASE_URL must be explicitly set\"}\n"},
		{name: "whitespace is not explicit configuration", value: " \t\n ", expected: "{\"level\":\"error\",\"code\":\"invalid_configuration\",\"message\":\"DATABASE_URL must be explicitly set\"}\n"},
		{name: "arguments never become configuration", args: []string{"postgres://private:secret@private-host/db?password=secret\nextra"}, value: "configured", expected: "{\"level\":\"error\",\"code\":\"invalid_arguments\",\"message\":\"lifecycle-census does not accept arguments\"}\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var stdout, stderr bytes.Buffer
			called := false
			code := run(context.Background(), tt.args, func(key string) string {
				require.Equal(t, "DATABASE_URL", key, "command should read only the explicit database configuration")
				return tt.value
			}, &stdout, &stderr, func(context.Context, string) (lifecyclecensus.Report, error) {
				called = true
				return censusReportFixture(), nil
			})
			require.Equal(t, 2, code, "invalid invocation should return the usage/configuration exit code")
			require.False(t, called, "invalid invocation must not access the database")
			require.Equal(t, "", stdout.String(), "invalid invocation must not emit an observation report")
			require.Equal(t, tt.expected, stderr.String(), "diagnostics must be structured and omit supplied values")
		})
	}
}

func TestRunSuccessIsDeterministicObservationOnly(t *testing.T) {
	t.Parallel()

	const databaseURL = "postgres://synthetic:secret@synthetic-host/test?sslmode=disable"
	const expected = "{\"schema_version\":1,\"limitations\":[\"observational_only\"],\"writers\":{\"observed_at\":\"2026-01-02T03:04:05Z\",\"truncated\":true,\"counts_scope\":\"retained_sample\",\"rows_observed\":1,\"groups\":[{\"role\":\"unknown\",\"build\":\"unknown\",\"writer_version\":0,\"count\":1}]},\"connections\":{\"observed_at\":\"2026-01-02T03:04:06Z\",\"visibility\":\"restricted\",\"truncated\":false,\"counts_scope\":\"observed_rows\",\"rows_observed\":1,\"groups\":[{\"role\":\"unknown\",\"build\":\"unknown\",\"state\":\"unknown\",\"count\":1,\"unknown_backend_type\":0,\"observed_open_transactions\":0,\"unknown_transaction_details\":1,\"oldest_observed_transaction_at\":null}]}}\n"
	for range 2 {
		var stdout, stderr bytes.Buffer
		calls := 0
		code := run(context.Background(), nil, func(string) string { return databaseURL }, &stdout, &stderr, func(ctx context.Context, supplied string) (lifecyclecensus.Report, error) {
			calls++
			require.NoError(t, ctx.Err(), "collector should receive the live command context")
			require.Equal(t, databaseURL, supplied, "collector should receive the explicit configuration without a fallback")
			return censusReportFixture(), nil
		})
		require.Equal(t, 0, code, "restricted, truncated and unknown observations are successful collection, not a rollout decision")
		require.Equal(t, 1, calls, "command should perform one bounded collection")
		require.Equal(t, expected, stdout.String(), "identical observations should have exact deterministic versioned JSON output")
		require.Equal(t, "", stderr.String(), "successful collection should not log configuration or diagnostics")
	}
}

func TestRunCollectionErrorsAreSanitized(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		err        error
		preCancel  bool
		postCancel bool
		expected   string
	}{
		{name: "database parse or source failure", err: errors.New("postgres://private:secret@private-host/db?password=secret\nquery SELECT private_data"), expected: "{\"level\":\"error\",\"code\":\"collection_failed\",\"message\":\"could not collect census observations\"}\n"},
		{name: "collector cancellation", err: fmt.Errorf("private secret: %w", context.Canceled), expected: "{\"level\":\"error\",\"code\":\"collection_canceled\",\"message\":\"census collection was canceled\"}\n"},
		{name: "collector time budget", err: fmt.Errorf("private secret: %w", context.DeadlineExceeded), expected: "{\"level\":\"error\",\"code\":\"collection_timed_out\",\"message\":\"census collection exceeded its time budget\"}\n"},
		{name: "canceled before collection", preCancel: true, expected: "{\"level\":\"error\",\"code\":\"collection_canceled\",\"message\":\"census collection was canceled\"}\n"},
		{name: "collector succeeds after cancellation", postCancel: true, expected: "{\"level\":\"error\",\"code\":\"collection_canceled\",\"message\":\"census collection was canceled\"}\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tt.preCancel {
				cancel()
			}
			var stdout, stderr bytes.Buffer
			called := false
			code := run(ctx, nil, func(string) string { return "postgres://private:secret@private-host/db?password=secret\n" }, &stdout, &stderr, func(context.Context, string) (lifecyclecensus.Report, error) {
				called = true
				if tt.postCancel {
					cancel()
				}
				return censusReportFixture(), tt.err
			})
			require.Equal(t, 1, code, "collection errors and cancellation should return a nonzero runtime exit code")
			require.Equal(t, !tt.preCancel, called, "already-canceled commands must not begin database collection")
			require.Equal(t, "", stdout.String(), "failed collection must not emit a success report")
			require.Equal(t, tt.expected, stderr.String(), "collection diagnostics must omit raw database errors and configuration")
		})
	}
}

type censusOutputWriter struct {
	buffer bytes.Buffer
	err    error
	short  bool
}

func (w *censusOutputWriter) Write(value []byte) (int, error) {
	if w.err != nil {
		return 0, w.err
	}
	if w.short {
		return w.buffer.Write(value[:1])
	}
	return w.buffer.Write(value)
}

func TestRunEncodingAndOutputFailures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		invalidTime    bool
		writeErr       error
		shortWrite     bool
		expectedOutput string
		expectedError  string
	}{
		{name: "encoding fails before output", invalidTime: true, expectedError: "{\"level\":\"error\",\"code\":\"encoding_failed\",\"message\":\"could not encode census report\"}\n"},
		{name: "output error details are private", writeErr: errors.New("private socket /Users/private postgres://user:secret@host"), expectedError: "{\"level\":\"error\",\"code\":\"output_failed\",\"message\":\"could not write census report\"}\n"},
		{name: "short write can leave partial output", shortWrite: true, expectedOutput: "{", expectedError: "{\"level\":\"error\",\"code\":\"output_failed\",\"message\":\"could not write census report\"}\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var stderr bytes.Buffer
			stdout := &censusOutputWriter{err: tt.writeErr, short: tt.shortWrite}
			code := run(context.Background(), nil, func(string) string { return "explicit" }, stdout, &stderr, func(context.Context, string) (lifecyclecensus.Report, error) {
				report := censusReportFixture()
				if tt.invalidTime {
					report.Writers.ObservedAt = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
				}
				return report, nil
			})
			require.Equal(t, 1, code, "encoding or incomplete report delivery must not return success")
			require.Equal(t, tt.expectedOutput, stdout.buffer.String(), "encoding failure must precede output and write failures may leave only their written prefix")
			require.Equal(t, tt.expectedError, stderr.String(), "output diagnostics must be structured and omit raw writer error details")
		})
	}
}

var _ io.Writer = (*censusOutputWriter)(nil)
