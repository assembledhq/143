package connector

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestPostgresProviderAdvertisesReadOnlyCapabilities(t *testing.T) {
	t.Parallel()

	provider := NewPostgresProvider(uuid.New(), nil, PostgresPolicy{})

	require.ElementsMatch(t, []string{
		"postgres.query",
		"postgres.read_query",
		"postgres.schema",
		"postgres.explain",
		"postgres.sample_rows",
		"postgres.indexes",
	}, provider.Capabilities(), "Postgres provider should advertise the full read-only agent capability surface")
}

func TestPostgresProviderRunsQueryInReadOnlyTransaction(t *testing.T) {
	t.Parallel()

	resourceID := uuid.New()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock should initialize")
	defer mock.Close()
	mock.ExpectBeginTx(pgx.TxOptions{AccessMode: pgx.ReadOnly})
	mock.ExpectExec(`SET LOCAL statement_timeout`).
		WillReturnResult(pgxmock.NewResult("SET", 0))
	mock.ExpectQuery(`SELECT id, email FROM users LIMIT 2`).
		WillReturnRows(pgxmock.NewRows([]string{"id", "email"}).
			AddRow(int64(1), "dev@example.com").
			AddRow(int64(2), "ops@example.com"))
	mock.ExpectCommit()
	provider := NewPostgresProvider(resourceID, mock, PostgresPolicy{MaxRows: 10, StatementTimeoutMs: 5000})
	params := json.RawMessage(`{"query":"SELECT id, email FROM users LIMIT 2"}`)

	result, err := provider.HandleAction(context.Background(), ActionRequest{
		ResourceID: resourceID,
		Capability: "postgres.query",
		Params:     params,
	})

	require.NoError(t, err, "Postgres provider should execute read-only queries")
	require.Equal(t, 2, result.Metadata.ResultCount, "Postgres provider should report row count")
	var payload PostgresQueryResult
	require.NoError(t, json.Unmarshal(result.Payload, &payload), "Postgres provider result should decode")
	require.Equal(t, []string{"id", "email"}, payload.Columns, "Postgres provider should return column names")
	require.Equal(t, []map[string]any{
		{"id": float64(1), "email": "dev@example.com"},
		{"id": float64(2), "email": "ops@example.com"},
	}, payload.Rows, "Postgres provider should return rows keyed by column")
	require.False(t, payload.Truncated, "Postgres provider should not mark untruncated result")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestPostgresProviderSchemaInspectionUsesInformationSchema(t *testing.T) {
	t.Parallel()

	resourceID := uuid.New()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock should initialize")
	defer mock.Close()
	mock.ExpectBeginTx(pgx.TxOptions{AccessMode: pgx.ReadOnly})
	mock.ExpectExec(`SET LOCAL statement_timeout`).
		WillReturnResult(pgxmock.NewResult("SET", 0))
	mock.ExpectQuery(`information_schema\.columns`).
		WithArgs(pgx.NamedArgs{"schema": "public"}).
		WillReturnRows(pgxmock.NewRows([]string{"table_schema", "table_name", "column_name", "data_type", "is_nullable"}).
			AddRow("public", "users", "id", "uuid", "NO").
			AddRow("public", "users", "email", "text", "YES"))
	mock.ExpectCommit()
	provider := NewPostgresProvider(resourceID, mock, PostgresPolicy{AllowedSchemas: []string{"public"}})

	result, err := provider.HandleAction(context.Background(), ActionRequest{
		ResourceID: resourceID,
		Capability: "postgres.schema",
		Params:     json.RawMessage(`{"schema":"public"}`),
	})

	require.NoError(t, err, "Postgres provider should inspect schema through information_schema")
	var payload PostgresSchemaResult
	require.NoError(t, json.Unmarshal(result.Payload, &payload), "schema payload should decode")
	require.Equal(t, []PostgresTableSchema{{
		Schema: "public",
		Name:   "users",
		Columns: []PostgresColumnSchema{
			{Name: "id", DataType: "uuid", Nullable: false},
			{Name: "email", DataType: "text", Nullable: true},
		},
	}}, payload.Tables, "schema inspection should group columns by table")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestPostgresProviderExplainRunsBoundedExplain(t *testing.T) {
	t.Parallel()

	resourceID := uuid.New()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock should initialize")
	defer mock.Close()
	mock.ExpectBeginTx(pgx.TxOptions{AccessMode: pgx.ReadOnly})
	mock.ExpectExec(`SET LOCAL statement_timeout`).
		WillReturnResult(pgxmock.NewResult("SET", 0))
	mock.ExpectQuery(`EXPLAIN \(FORMAT JSON\) SELECT id FROM users`).
		WillReturnRows(pgxmock.NewRows([]string{"QUERY PLAN"}).
			AddRow(`[{"Plan":{"Node Type":"Seq Scan"}}]`))
	mock.ExpectCommit()
	provider := NewPostgresProvider(resourceID, mock, PostgresPolicy{})

	result, err := provider.HandleAction(context.Background(), ActionRequest{
		ResourceID: resourceID,
		Capability: "postgres.explain",
		Params:     json.RawMessage(`{"query":"SELECT id FROM users"}`),
	})

	require.NoError(t, err, "Postgres provider should run EXPLAIN for read-only query plans")
	var payload PostgresExplainResult
	require.NoError(t, json.Unmarshal(result.Payload, &payload), "explain payload should decode")
	require.Equal(t, json.RawMessage(`[{"Plan":{"Node Type":"Seq Scan"}}]`), payload.Plan, "explain should return JSON plan payload")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestPostgresProviderSampleRowsRequiresOptIn(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock should initialize")
	defer mock.Close()
	provider := NewPostgresProvider(uuid.New(), mock, PostgresPolicy{})

	_, err = provider.HandleAction(context.Background(), ActionRequest{
		ResourceID: provider.resourceID,
		Capability: "postgres.sample_rows",
		Params:     json.RawMessage(`{"schema":"public","table":"users"}`),
	})

	require.ErrorIs(t, err, ErrPostgresQueryDenied, "sample rows should be denied unless explicitly enabled")
	require.NoError(t, mock.ExpectationsWereMet(), "denied sample_rows should not issue database calls")
}

func TestPostgresProviderRedactsExactAndPatternColumnsPreservingNull(t *testing.T) {
	t.Parallel()

	rows := []map[string]any{{
		"email":          "dev@example.com",
		"billing_email":  "billing@example.com",
		"nullable_email": nil,
		"name":           "Dev",
	}}

	redactPostgresRows(rows, []string{"email"}, []string{".*_email$"})

	require.Equal(t, "[REDACTED]", rows[0]["email"], "exact PII columns should be redacted")
	require.Equal(t, "[REDACTED]", rows[0]["billing_email"], "pattern PII columns should be redacted")
	require.Nil(t, rows[0]["nullable_email"], "null PII values should remain null")
	require.Equal(t, "Dev", rows[0]["name"], "non-PII columns should remain visible")
}

func TestPostgresProviderRejectsDeniedTablesBeforeQuery(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock should initialize")
	defer mock.Close()
	resourceID := uuid.New()
	provider := NewPostgresProvider(resourceID, mock, PostgresPolicy{DeniedTables: []string{"public.payment_methods"}})

	_, err = provider.HandleAction(context.Background(), ActionRequest{
		ResourceID: resourceID,
		Capability: "postgres.query",
		Params:     json.RawMessage(`{"query":"SELECT * FROM public.payment_methods"}`),
	})

	require.True(t, errors.Is(err, ErrPostgresQueryDenied), "Postgres provider should deny configured sensitive tables before execution")
	require.NoError(t, mock.ExpectationsWereMet(), "denied table should not issue database calls")
}

func TestPostgresProviderRejectsCopy(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock should initialize")
	defer mock.Close()
	resourceID := uuid.New()
	provider := NewPostgresProvider(resourceID, mock, PostgresPolicy{})

	_, err = provider.HandleAction(context.Background(), ActionRequest{
		ResourceID: resourceID,
		Capability: "postgres.query",
		Params:     json.RawMessage(`{"query":"COPY users TO STDOUT"}`),
	})

	require.ErrorIs(t, err, ErrPostgresQueryDenied, "Postgres provider should reject COPY without touching the database")
	require.NoError(t, mock.ExpectationsWereMet(), "COPY rejection should not issue database calls")
}
