package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCheckFunc(t *testing.T) {
	t.Parallel()

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
			name: "accepts *models.X carrier",
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
			name: "accepts models.X value carrier",
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
				if v := checkFunc(fset, fn); v != "" {
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
