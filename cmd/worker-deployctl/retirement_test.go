package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/stretchr/testify/require"
)

type fakeRetirementSealer struct {
	params db.SealNodeRetirementParams
	seal   db.NodeRetirementSeal
	err    error
	called bool
}

func (f *fakeRetirementSealer) Seal(_ context.Context, params db.SealNodeRetirementParams) (db.NodeRetirementSeal, error) {
	f.called = true
	f.params = params
	return f.seal, f.err
}

func TestSealRetirement(t *testing.T) {
	t.Parallel()
	validArgs := []string{"--node-id", "old-generation", "--deploy-id", "attempt-1", "--build-sha", "candidate-sha", "--json"}
	tests := []struct {
		name   string
		args   []string
		err    error
		code   int
		called bool
	}{
		{name: "verified seal", args: validArgs, called: true},
		{name: "only verified busy waits", args: validArgs, err: db.ErrNodeRetirementBusy, code: 3, called: true},
		{name: "unknown result halts", args: validArgs, err: errors.New("commit outcome unknown"), code: 1, called: true},
		{name: "missing node halts", args: []string{"--deploy-id", "attempt-1", "--build-sha", "sha"}, code: 1},
		{name: "missing deployment halts", args: []string{"--node-id", "old", "--build-sha", "sha"}, code: 1},
		{name: "missing build halts", args: []string{"--node-id", "old", "--deploy-id", "attempt-1"}, code: 1},
		{name: "blank identity halts", args: []string{"--node-id", " ", "--deploy-id", "attempt-1", "--build-sha", "sha"}, code: 1},
		{name: "extra argument halts", args: append(append([]string{}, validArgs...), "other-node"), code: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			want := db.NodeRetirementSeal{NodeID: "old-generation", DeployID: "attempt-1", BuildSHA: "candidate-sha", SealedAt: time.Unix(1, 0), Sealed: true}
			store := &fakeRetirementSealer{seal: want, err: tt.err}
			got, jsonOut, code, err := sealRetirement(context.Background(), store, tt.args)
			require.Equal(t, tt.code, code, "only positive busy evidence may return the retryable status")
			require.Equal(t, tt.called, store.called, "invalid arguments must not attempt retirement")
			if tt.code == 0 {
				require.NoError(t, err, "a verified seal should succeed")
				require.Equal(t, want, got, "return the exact immutable seal identity")
				require.True(t, jsonOut, "honor the machine-readable receipt flag")
			} else {
				require.Error(t, err, "failed or unknown retirement must preserve the error")
			}
			if tt.called {
				require.Equal(t, db.SealNodeRetirementParams{NodeID: "old-generation", DeployID: "attempt-1", BuildSHA: "candidate-sha"}, store.params, "bind retirement to the exact caller identities")
			}
		})
	}
}
