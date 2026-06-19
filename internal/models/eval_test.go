package models

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEvalTaskSource_Validate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		source  EvalTaskSource
		wantErr bool
	}{
		{"manual", EvalTaskSourceManual, false},
		{"pr_bootstrap", EvalTaskSourcePRBootstrap, false},
		{"failure_derived", EvalTaskSourceFailureDerived, false},
		{"invalid", EvalTaskSource("unknown"), true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.source.Validate()
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestEvalComplexity_Validate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		complexity EvalComplexity
		wantErr    bool
	}{
		{"trivial", EvalComplexityTrivial, false},
		{"simple", EvalComplexitySimple, false},
		{"moderate", EvalComplexityModerate, false},
		{"complex", EvalComplexityComplex, false},
		{"invalid", EvalComplexity("extreme"), true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.complexity.Validate()
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestEvalBootstrapCandidateStatus_Validate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		status    EvalBootstrapCandidateStatus
		expectErr bool
	}{
		{name: "proposed", status: EvalBootstrapCandidateStatusProposed},
		{name: "accepted", status: EvalBootstrapCandidateStatusAccepted},
		{name: "needs revision", status: EvalBootstrapCandidateStatusNeedsRevision},
		{name: "rejected", status: EvalBootstrapCandidateStatusRejected},
		{name: "invalid", status: EvalBootstrapCandidateStatus("unknown"), expectErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.status.Validate()
			if tt.expectErr {
				require.Error(t, err, "Validate should reject unknown candidate statuses")
				return
			}
			require.NoError(t, err, "Validate should accept known candidate statuses")
		})
	}
}

func TestGraderType_Validate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		grader  GraderType
		wantErr bool
	}{
		{"code_check", GraderTypeCodeCheck, false},
		{"llm_judge", GraderTypeLLMJudge, false},
		{"invalid", GraderType("human_review"), true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.grader.Validate()
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}
