package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCheckFunc(t *testing.T) {
	t.Parallel()

	// All carrier-bearing tests use type Foo, which is registered as a known
	// carrier. The "unknown carrier" case below uses Bar, which is not.
	carriers := map[string]bool{"Foo": true}

	tests := []struct {
		name    string
		src     string
		wantHit bool
	}{
		{
			name: "flags exported store method with no org scope",
			src: `package x
import "context"
type FooStore struct{}
func (s *FooStore) GetByID(ctx context.Context, id string) {}`,
			wantHit: true,
		},
		{
			name: "accepts orgID uuid.UUID parameter",
			src: `package x
import (
  "context"
  "github.com/google/uuid"
)
type FooStore struct{}
func (s *FooStore) GetByID(ctx context.Context, orgID uuid.UUID, id string) {}`,
			wantHit: false,
		},
		{
			name: "accepts uppercase OrgID uuid.UUID",
			src: `package x
import (
  "context"
  "github.com/google/uuid"
)
type FooStore struct{}
func (s *FooStore) List(ctx context.Context, OrgID uuid.UUID) {}`,
			wantHit: false,
		},
		{
			name: "accepts srcOrgID uuid.UUID (suffix match)",
			src: `package x
import (
  "context"
  "github.com/google/uuid"
)
type FooStore struct{}
func (s *FooStore) Copy(ctx context.Context, srcOrgID, destOrgID uuid.UUID) {}`,
			wantHit: false,
		},
		{
			name: "rejects organizerID uuid.UUID (not org_id)",
			src: `package x
import (
  "context"
  "github.com/google/uuid"
)
type FooStore struct{}
func (s *FooStore) AssignOrganizer(ctx context.Context, organizerID uuid.UUID) {}`,
			wantHit: true,
		},
		{
			name: "accepts *models.X carrier when X has OrgID",
			src: `package x
import (
  "context"
  "pkg/models"
)
type FooStore struct{}
func (s *FooStore) Create(ctx context.Context, e *models.Foo) {}`,
			wantHit: false,
		},
		{
			name: "accepts models.X value carrier when X has OrgID",
			src: `package x
import (
  "context"
  "pkg/models"
)
type FooStore struct{}
func (s *FooStore) Create(ctx context.Context, e models.Foo) {}`,
			wantHit: false,
		},
		{
			name: "rejects *models.X when X is NOT a known OrgID-bearing carrier",
			src: `package x
import (
  "context"
  "pkg/models"
)
type FooStore struct{}
func (s *FooStore) Create(ctx context.Context, e *models.Bar) {}`,
			wantHit: true,
		},
		{
			name: "respects opt-out comment",
			src: `package x
import "context"
type FooStore struct{}
// lint:allow-no-orgid reason="pre-auth"
func (s *FooStore) GetByToken(ctx context.Context, token string) {}`,
			wantHit: false,
		},
		{
			name: "ignores unexported methods",
			src: `package x
import "context"
type FooStore struct{}
func (s *FooStore) scan(ctx context.Context, raw string) {}`,
			wantHit: false,
		},
		{
			name: "ignores non-Store receivers",
			src: `package x
import "context"
type FooThing struct{}
func (s *FooThing) Something(ctx context.Context, x string) {}`,
			wantHit: false,
		},
		{
			name: "param named orgID but wrong type does NOT pass",
			src: `package x
import "context"
type FooStore struct{}
func (s *FooStore) Get(ctx context.Context, orgID string) {}`,
			wantHit: true,
		},
		{
			name: "batched name list: `a, orgID uuid.UUID`",
			src: `package x
import (
  "context"
  "github.com/google/uuid"
)
type FooStore struct{}
func (s *FooStore) Get(ctx context.Context, a, orgID uuid.UUID) {}`,
			wantHit: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, "test.go", tt.src, parser.ParseComments)
			require.NoError(t, err, "parse test source")

			var hits []string
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok {
					continue
				}
				if v := checkFunc(fset, fn, carriers); v != "" {
					hits = append(hits, v)
				}
			}

			if tt.wantHit {
				require.NotEmpty(t, hits, "expected a violation")
			} else {
				require.Empty(t, hits, "expected no violations, got %v", hits)
			}
		})
	}
}

func TestLoadOrgIDCarriers(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "models.go"), []byte(`package models

import "github.com/google/uuid"

type WithOrgID struct {
	ID    uuid.UUID
	OrgID uuid.UUID
	Name  string
}

type WithoutOrgID struct {
	ID   uuid.UUID
	Name string
}

type AlsoWithOrgID struct {
	OrgID uuid.UUID
}
`), 0o600), "write models.go")

	// A test file in the same dir should be skipped.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "models_test.go"), []byte(`package models

type FromTestFile struct {
	OrgID string
}
`), 0o600), "write test file")

	carriers, err := loadOrgIDCarriers(dir)
	require.NoError(t, err)

	require.True(t, carriers["WithOrgID"], "WithOrgID should be a carrier")
	require.True(t, carriers["AlsoWithOrgID"], "AlsoWithOrgID should be a carrier")
	require.False(t, carriers["WithoutOrgID"], "WithoutOrgID should not be a carrier")
	require.False(t, carriers["FromTestFile"], "test files should be skipped")
}
