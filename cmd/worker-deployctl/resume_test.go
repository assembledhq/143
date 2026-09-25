package main

import (
	"testing"

	"github.com/assembledhq/143/internal/db"
	"github.com/stretchr/testify/require"
)

func TestParseResumeArgs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		args      []string
		expected  db.ResumeNodeParams
		expectErr bool
	}{
		{"explicit generation and operator", []string{"--node-id", "worker-old", "--reason", "replacement failed", "--requested-by", "operator"}, db.ResumeNodeParams{NodeID: "worker-old", Reason: "replacement failed", RequestedBy: "operator"}, false},
		{"missing node", []string{"--reason", "rollback", "--requested-by", "operator"}, db.ResumeNodeParams{}, true},
		{"missing reason", []string{"--node-id", "worker-old", "--requested-by", "operator"}, db.ResumeNodeParams{}, true},
		{"missing operator", []string{"--node-id", "worker-old", "--reason", "rollback"}, db.ResumeNodeParams{}, true},
		{"blank reason", []string{"--node-id", "worker-old", "--reason", " ", "--requested-by", "operator"}, db.ResumeNodeParams{}, true},
		{"unknown flag", []string{"--force"}, db.ResumeNodeParams{}, true},
		{"unexpected argument", []string{"--node-id", "worker-old", "--reason", "rollback", "--requested-by", "operator", "extra"}, db.ResumeNodeParams{}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			actual, err := parseResumeArgs(tt.args)
			if tt.expectErr {
				require.Error(t, err, "resume must require explicit attribution and one exact node")
			} else {
				require.NoError(t, err, "complete resume arguments should parse")
			}
			require.Equal(t, tt.expected, actual, "resume should preserve the operator's exact intent")
		})
	}
}
