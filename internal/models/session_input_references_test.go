package models

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSessionInputReferenceKindValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		kind    SessionInputReferenceKind
		wantErr string
	}{
		{name: "file", kind: SessionInputReferenceKindFile},
		{name: "directory", kind: SessionInputReferenceKindDirectory},
		{name: "app", kind: SessionInputReferenceKindApp},
		{name: "plugin", kind: SessionInputReferenceKindPlugin},
		{name: "invalid", kind: SessionInputReferenceKind("nope"), wantErr: `invalid SessionInputReferenceKind: "nope"`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.kind.Validate()
			if tt.wantErr == "" {
				require.NoError(t, err, "Validate should accept supported reference kinds")
				return
			}
			require.EqualError(t, err, tt.wantErr, "Validate should reject unsupported reference kinds")
		})
	}
}

func TestSessionInputReferenceValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		reference SessionInputReference
		wantErr   string
	}{
		{
			name: "valid file reference",
			reference: SessionInputReference{
				Kind:    SessionInputReferenceKindFile,
				Path:    "internal/api/handlers/sessions.go",
				Display: "internal/api/handlers/sessions.go",
			},
		},
		{
			name: "valid plugin reference",
			reference: SessionInputReference{
				Kind:    SessionInputReferenceKindPlugin,
				ID:      "github",
				Display: "GitHub",
			},
		},
		{
			name: "missing display",
			reference: SessionInputReference{
				Kind: SessionInputReferenceKindFile,
				Path: "internal/api/handlers/sessions.go",
			},
			wantErr: "display is required",
		},
		{
			name: "file missing path",
			reference: SessionInputReference{
				Kind:    SessionInputReferenceKindFile,
				Display: "internal/api/handlers/sessions.go",
			},
			wantErr: "path is required for file references",
		},
		{
			name: "app missing id",
			reference: SessionInputReference{
				Kind:    SessionInputReferenceKindApp,
				Display: "GitHub",
			},
			wantErr: "id is required for app references",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.reference.Validate()
			if tt.wantErr == "" {
				require.NoError(t, err, "Validate should accept well-formed session references")
				return
			}
			require.EqualError(t, err, tt.wantErr, "Validate should reject malformed session references")
		})
	}
}

func TestSessionInputReferencesValue(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		references SessionInputReferences
		want       string
	}{
		{name: "empty slice", references: SessionInputReferences{}, want: "[]"},
		{
			name: "encoded references",
			references: SessionInputReferences{
				{
					Kind:    SessionInputReferenceKindDirectory,
					Token:   "@frontend/src",
					Path:    "frontend/src",
					Display: "frontend/src",
				},
			},
			want: `[{"kind":"directory","token":"@frontend/src","path":"frontend/src","display":"frontend/src"}]`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			value, err := tt.references.Value()
			require.NoError(t, err, "Value should encode session references")
			encoded, ok := value.([]byte)
			require.True(t, ok, "Value should return JSON bytes")
			require.Equal(t, tt.want, string(encoded), "Value should encode the expected JSON payload")
		})
	}
}

func TestSessionInputReferencesScan(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		src     any
		want    SessionInputReferences
		wantNil bool
		wantErr string
	}{
		{name: "nil source", src: nil, wantNil: true},
		{name: "empty bytes", src: []byte{}, wantNil: true},
		{
			name: "json bytes",
			src:  []byte(`[{"kind":"file","path":"internal/api/handlers/sessions.go","display":"internal/api/handlers/sessions.go"}]`),
			want: SessionInputReferences{
				{
					Kind:    SessionInputReferenceKindFile,
					Path:    "internal/api/handlers/sessions.go",
					Display: "internal/api/handlers/sessions.go",
				},
			},
		},
		{
			name: "json string",
			src:  `[{"kind":"plugin","id":"github","display":"GitHub"}]`,
			want: SessionInputReferences{
				{
					Kind:    SessionInputReferenceKindPlugin,
					ID:      "github",
					Display: "GitHub",
				},
			},
		},
		{
			name:    "unsupported type",
			src:     42,
			wantErr: "unsupported session input references type int",
		},
		{
			name:    "invalid json",
			src:     []byte("{"),
			wantErr: "unmarshal session input references: unexpected end of JSON input",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var references SessionInputReferences
			err := references.Scan(tt.src)
			if tt.wantErr != "" {
				require.EqualError(t, err, tt.wantErr, "Scan should surface decoding errors")
				return
			}

			require.NoError(t, err, "Scan should decode supported session reference payloads")
			if tt.wantNil {
				require.Nil(t, references, "Scan should preserve nil for empty sources")
				return
			}
			require.Equal(t, tt.want, references, "Scan should decode the expected session references")
		})
	}
}
