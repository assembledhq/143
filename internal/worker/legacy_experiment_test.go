package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestObsoleteExperimentHandler(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, payload string
		jobOrg        bool
		fail          bool
	}{
		{name: "legacy payload uses durable job organization", payload: `{"pull_request_id":"%s","commit_sha":"old-head"}`, jobOrg: true},
		{name: "explicit organization", payload: `{"org_id":"%s","pull_request_id":"%s"}`},
		{name: "malformed JSON", payload: `{`, fail: true},
		{name: "missing organization", payload: `{"pull_request_id":"%s"}`, fail: true},
		{name: "invalid pull request", payload: `{"pull_request_id":"invalid"}`, jobOrg: true, fail: true},
		{name: "zero pull request", payload: `{"pull_request_id":"00000000-0000-0000-0000-000000000000"}`, jobOrg: true, fail: true},
		{name: "wrong organization", payload: `{"org_id":"00000000-0000-0000-0000-000000000001","pull_request_id":"%s"}`, jobOrg: true, fail: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			orgID, prID := uuid.New(), uuid.New()
			payload := tt.payload
			if tt.name == "explicit organization" {
				payload = fmt.Sprintf(payload, orgID, prID)
			} else if tt.name == "legacy payload uses durable job organization" || tt.name == "missing organization" || tt.name == "wrong organization" {
				payload = fmt.Sprintf(payload, prID)
			}
			ctx := context.Background()
			if tt.jobOrg {
				ctx = withJobOrgID(ctx, orgID)
			}
			var logs bytes.Buffer
			err := newObsoleteExperimentHandler(zerolog.New(&logs))(ctx, "evaluate_experiment", json.RawMessage(payload))
			if tt.fail {
				var fatal *FatalError
				require.ErrorAs(t, err, &fatal, "malformed legacy work should terminate without retries")
				require.Empty(t, logs.String(), "invalid identities should not be logged as a successful skip")
				return
			}
			require.NoError(t, err, "obsolete work should complete without inventing experiment behavior")
			require.Contains(t, logs.String(), "skipping obsolete experiment evaluation job: feature is not implemented", "compatibility consumption must explain its intentional skip")
		})
	}
}
