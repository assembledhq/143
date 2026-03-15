package db

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWhereClause_Build(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		setup          func(w *whereClause)
		expectedSQL    string
		expectedArgLen int
	}{
		{
			name:           "empty returns empty string",
			setup:          func(w *whereClause) {},
			expectedSQL:    "",
			expectedArgLen: 0,
		},
		{
			name: "single condition",
			setup: func(w *whereClause) {
				w.add("org_id = @org_id", "org_id", "test-org")
			},
			expectedSQL:    " WHERE org_id = @org_id",
			expectedArgLen: 1,
		},
		{
			name: "multiple conditions joined with AND",
			setup: func(w *whereClause) {
				w.add("org_id = @org_id", "org_id", "test-org")
				w.add("status = @status", "status", "active")
			},
			expectedSQL:    " WHERE org_id = @org_id AND status = @status",
			expectedArgLen: 2,
		},
		{
			name: "addArg adds arg without condition",
			setup: func(w *whereClause) {
				w.add("(a, b) < (@a, @b)", "a", 1)
				w.addArg("b", 2)
			},
			expectedSQL:    " WHERE (a, b) < (@a, @b)",
			expectedArgLen: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			w := newWhereClause()
			tt.setup(w)
			sql, args := w.build()
			require.Equal(t, tt.expectedSQL, sql)
			require.Len(t, args, tt.expectedArgLen)
		})
	}
}

func TestEscapeLike(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input    string
		expected string
	}{
		{input: "session.", expected: "session."},
		{input: "100%", expected: `100\%`},
		{input: "foo_bar", expected: `foo\_bar`},
		{input: `back\slash`, expected: `back\\slash`},
		{input: `%_\`, expected: `\%\_\\`},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.expected, escapeLike(tt.input))
		})
	}
}
