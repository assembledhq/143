package main

import (
	"bytes"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"net/url"
	"testing"

	"github.com/golang-migrate/migrate/v4"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

func TestLifecycleMigrationDatabaseURL(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, raw, expected string
	}{
		{
			name:     "postgres URI preserves explicit target",
			raw:      "postgres://synthetic_user:synthetic_password@database.example.test:55432/synthetic_database?sslmode=disable",
			expected: "postgres://synthetic_user:synthetic_password@database.example.test:55432/synthetic_database?application_name=143%3Aoperator%3Adev&sslmode=disable",
		},
		{
			name:     "postgresql URI preserves escaped credentials path TLS and fragment",
			raw:      "postgresql://synthetic%40user:synthetic%3Apassword@[2001:db8::1]:55432/synthetic%2Fdatabase?sslmode=verify-full&sslrootcert=%2Fsynthetic%2Froot.pem&connect_timeout=7#synthetic-fragment",
			expected: "postgresql://synthetic%40user:synthetic%3Apassword@[2001:db8::1]:55432/synthetic%2Fdatabase?application_name=143%3Aoperator%3Adev&connect_timeout=7&sslmode=verify-full&sslrootcert=%2Fsynthetic%2Froot.pem#synthetic-fragment",
		},
		{
			name:     "socket and runtime settings remain driver owned",
			raw:      "postgres:///synthetic?host=%2Fsynthetic%2Fsocket&port=55432&search_path=synthetic_schema&statement_timeout=4321&lock_timeout=1234&fallback_application_name=synthetic_fallback",
			expected: "postgres:///synthetic?application_name=143%3Aoperator%3Adev&fallback_application_name=synthetic_fallback&host=%2Fsynthetic%2Fsocket&lock_timeout=1234&port=55432&search_path=synthetic_schema&statement_timeout=4321",
		},
		{
			name:     "repeated options database overrides and custom migration settings are preserved",
			raw:      "postgres://database.example.test/original?options=-c%20search_path%3Dfirst&options=-c%20search_path%3Dsecond&dbname=first_database&dbname=second_database&x-migrations-table=first_table&x-migrations-table=second_table&x-multi-statement=true&x-statement-timeout=9000",
			expected: "postgres://database.example.test/original?application_name=143%3Aoperator%3Adev&dbname=first_database&dbname=second_database&options=-c+search_path%3Dfirst&options=-c+search_path%3Dsecond&x-migrations-table=first_table&x-migrations-table=second_table&x-multi-statement=true&x-statement-timeout=9000",
		},
		{
			name:     "repeated and differently cased attribution aliases are replaced together",
			raw:      "postgres://database.example.test/synthetic?application_name=first&application_name=second&APPLICATION_NAME=uppercase&Application_Name=mixed&sslmode=disable",
			expected: "postgres://database.example.test/synthetic?application_name=143%3Aoperator%3Adev&sslmode=disable",
		},
		{
			name:     "encoded application key cannot escape override",
			raw:      "postgres://database.example.test/synthetic?%61pplication_name=synthetic_override&x-migrations-table=synthetic_table",
			expected: "postgres://database.example.test/synthetic?application_name=143%3Aoperator%3Adev&x-migrations-table=synthetic_table",
		},
		{
			name:     "malformed query pair keeps the original forgiving driver semantics",
			raw:      "postgres://database.example.test/synthetic?bad=%&sslmode=disable&x-migrations-table=synthetic_table&options=first&options=second",
			expected: "postgres://database.example.test/synthetic?application_name=143%3Aoperator%3Adev&options=first&options=second&sslmode=disable&x-migrations-table=synthetic_table",
		},
		{
			name:     "unescaped query semicolon is discarded as it was by the driver",
			raw:      "postgres://database.example.test/synthetic?bad=left;right&x-statement-timeout=9000&sslmode=disable",
			expected: "postgres://database.example.test/synthetic?application_name=143%3Aoperator%3Adev&sslmode=disable&x-statement-timeout=9000",
		},
		{
			name:     "legacy empty target parsing is not replaced",
			raw:      "postgres://?sslmode=disable",
			expected: "postgres://?sslmode=disable",
		},
		{
			name:     "omitted host keeps the original database path and driver defaults",
			raw:      "postgres:///synthetic?sslmode=disable",
			expected: "postgres:///synthetic?application_name=143%3Aoperator%3Adev&sslmode=disable",
		},
		{
			name:     "omitted host and database retain explicit user and driver defaults",
			raw:      "postgresql://synthetic_user@?sslmode=disable",
			expected: "postgresql://synthetic_user@?application_name=143%3Aoperator%3Adev&sslmode=disable",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			actual := lifecycleMigrationDatabaseURL(tt.raw, "dev")
			require.Equal(t, tt.expected, actual, "only attribution and existing query canonicalization may change the migration URL")
		})
	}
}

func TestLifecycleMigrationBuildLabel(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, build, expected string
	}{
		{name: "full SHA", build: "0123456789abcdef0123456789abcdef01234567", expected: "143:operator:0123456789abcdef0123456789abcdef01234567"},
		{name: "mixed case full SHA", build: "0123456789aBcDeF0123456789aBcDeF01234567", expected: "143:operator:0123456789aBcDeF0123456789aBcDeF01234567"},
		{name: "local development", build: "dev", expected: "143:operator:dev"},
		{name: "empty build", expected: "143:operator:unknown"},
		{name: "arbitrary metadata is only a marked fingerprint", build: "synthetic-build", expected: "143:operator:sha256-395aa0b0e625728f6ee72ac23c267dbd"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			actual := lifecycleMigrationDatabaseURL("postgres://database.example.test/synthetic", tt.build)
			parsed, err := url.Parse(actual)
			require.NoError(t, err, "attributed URL must remain a PostgreSQL driver URL")
			require.Equal(t, []string{tt.expected}, parsed.Query()["application_name"], "migration connections must use exactly the shared fixed-role safe build label")
		})
	}
}

func TestLifecycleMigrationPreservesDriverOptions(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, raw string
	}{
		{name: "repeated URI and migration options", raw: "postgres://synthetic:synthetic@database.example.test:55432/path_database?dbname=first&dbname=second&options=first&options=second&x-migrations-table=first&x-migrations-table=second&x-migrations-table-quoted=true&x-multi-statement=true&x-multi-statement-max-size=12345&x-statement-timeout=9000"},
		{name: "malformed query pair already dropped by driver", raw: "postgresql://database.example.test/synthetic?bad=%&x-migrations-table=synthetic&x-statement-timeout=invalid&sslmode=disable"},
		{name: "malformed custom pair already dropped by driver", raw: "postgres://database.example.test/synthetic?x-migrations-table=%&x-multi-statement=invalid&options=-c%20application_name%3Dsynthetic_override&fallback_application_name=synthetic_fallback"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			original, err := url.Parse(tt.raw)
			require.NoError(t, err, "the original driver accepts this URL before query filtering")
			attributed := lifecycleMigrationDatabaseURL(tt.raw, "dev")
			actual, err := url.Parse(attributed)
			require.NoError(t, err, "attribution must preserve the driver's URL representation")
			query := actual.Query()
			query.Del("application_name")
			actual.RawQuery = query.Encode()
			require.Equal(t, original.Query(), actual.Query(), "all valid query values including repeated and x-* settings must survive in original order")
			originalDSN, err := pq.ParseURL(migrate.FilterCustomQuery(original).String())
			require.NoError(t, err, "parse the original driver-filtered URL without opening a connection")
			actualDSN, err := pq.ParseURL(migrate.FilterCustomQuery(actual).String())
			require.NoError(t, err, "parse the attributed driver-filtered URL without opening a connection")
			require.Equal(t, originalDSN, actualDSN, "removing the intended attribution must leave the pinned libpq connection settings identical")
		})
	}
}

func TestLifecycleMigrationInvalidAndUnsupportedURLs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, raw  string
		parseError bool
	}{
		{name: "invalid path escape still rejected", raw: "postgres://database.example.test/%", parseError: true},
		{name: "invalid credential escape still rejected", raw: "postgres://synthetic%:synthetic@database.example.test/synthetic", parseError: true},
		{name: "invalid host escape still rejected", raw: "postgresql://database%zz.example.test/synthetic", parseError: true},
		{name: "unsupported URI remains for original driver dispatch", raw: "mysql://database.example.test/synthetic?application_name=original"},
		{name: "uppercase unsupported driver remains unsupported", raw: "POSTGRES://database.example.test/synthetic"},
		{name: "opaque PostgreSQL input keeps its original rejection", raw: "postgres:opaque"},
		{name: "non-URI PostgreSQL input keeps its original rejection", raw: "postgresql:/synthetic"},
		{name: "bare empty PostgreSQL URI keeps its original rejection", raw: "postgres://"},
		{name: "keyword input is not newly accepted", raw: "host=database.example.test dbname=synthetic"},
		{name: "empty input remains for original dispatch"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			actual := lifecycleMigrationDatabaseURL(tt.raw, "dev")
			require.Equal(t, tt.raw, actual, "unsupported or unparseable input must remain unchanged for the original migration constructor and driver")
			if tt.parseError {
				_, originalErr := url.Parse(tt.raw)
				require.Error(t, originalErr, "the pinned migration driver would reject the original malformed URL")
			}
		})
	}
}

func TestLifecycleMigrationEmptyTargetDriverFraming(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, raw, filtered string
	}{
		{name: "postgres empty authority and path", raw: "postgres://?sslmode=disable", filtered: "postgres:?sslmode=disable"},
		{name: "postgresql empty authority and path", raw: "postgresql://?sslmode=disable", filtered: "postgresql:?sslmode=disable"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			original, err := url.Parse(tt.raw)
			require.NoError(t, err, "the migration driver parses this target before applying its existing filter")
			require.Equal(t, tt.filtered, migrate.FilterCustomQuery(original).String(), "empty-target framing is already normalized by the pinned migration driver")
			attributed := lifecycleMigrationDatabaseURL(tt.raw, "dev")
			require.Equal(t, tt.raw, attributed, "empty-target legacy parsing must remain byte-for-byte unchanged rather than gain a query assignment")
			actual, err := url.Parse(attributed)
			require.NoError(t, err, "adding attribution must retain the same parseable driver scheme")
			query := actual.Query()
			query.Del("application_name")
			actual.RawQuery = query.Encode()
			require.Equal(t, tt.filtered, migrate.FilterCustomQuery(actual).String(), "removing attribution must leave the driver's existing target framing exactly unchanged")
			// The real constructor resolves both schemes before opening its
			// source. An unregistered synthetic source stops before any database
			// access, proving identical dispatch without probing default targets.
			const source = "lifecycle-attribution-no-source://synthetic"
			const expected = `failed to open source, "lifecycle-attribution-no-source://synthetic": source driver: unknown driver 'lifecycle-attribution-no-source' (forgotten import?)`
			_, originalErr := migrate.New(source, tt.raw)
			_, actualErr := migrate.New(source, attributed)
			require.EqualError(t, originalErr, expected, "the original constructor must reach source dispatch after accepting the original database scheme")
			require.EqualError(t, actualErr, expected, "attribution must retain the exact original constructor source-dispatch order without a new database URL rejection")
		})
	}
}

func TestLifecycleMigrationMainWiring(t *testing.T) {
	t.Parallel()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "main.go", nil, 0)
	require.NoError(t, err, "inspect the real single-file migration entrypoint rather than an unused helper")
	type wiring struct {
		outputs []string
		call    string
		args    []string
	}
	var actual []wiring
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Name.Name != "main" {
			continue
		}
		ast.Inspect(function.Body, func(node ast.Node) bool {
			assignment, ok := node.(*ast.AssignStmt)
			if !ok || len(assignment.Rhs) != 1 {
				return true
			}
			call, ok := assignment.Rhs[0].(*ast.CallExpr)
			if !ok {
				return true
			}
			name := migrationASTExpression(t, fset, call.Fun)
			if name != "lifecycleMigrationDatabaseURL" && name != "migrate.New" {
				return true
			}
			item := wiring{call: name}
			for _, output := range assignment.Lhs {
				item.outputs = append(item.outputs, migrationASTExpression(t, fset, output))
			}
			for _, argument := range call.Args {
				item.args = append(item.args, migrationASTExpression(t, fset, argument))
			}
			actual = append(actual, item)
			return true
		})
	}
	require.Equal(t, []wiring{
		{outputs: []string{"dbURL"}, call: "lifecycleMigrationDatabaseURL", args: []string{"dbURL", "version.BuildSHA"}},
		{outputs: []string{"m", "err"}, call: "migrate.New", args: []string{"migrationSource", "dbURL"}},
	}, actual, "main must pass its actual binary SHA and use the attributed URL in the original migration constructor and source")
}

func migrationASTExpression(t *testing.T, fset *token.FileSet, node ast.Node) string {
	t.Helper()
	var rendered bytes.Buffer
	require.NoError(t, format.Node(&rendered, fset, node), "render the actual migration wiring expression for an exact comparison")
	return rendered.String()
}
