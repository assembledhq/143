package connector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrPostgresQueryDenied = errors.New("postgres query denied")

type postgresDB interface {
	BeginTx(ctx context.Context, txOptions pgx.TxOptions) (pgx.Tx, error)
}

type PostgresPolicy struct {
	MaxRows            int
	StatementTimeoutMs int
	RedactColumns      []string
	RedactPatterns     []string
	AllowedSchemas     []string
	DeniedTables       []string
	AllowSampleRows    bool
}

type PostgresProvider struct {
	resourceID uuid.UUID
	db         postgresDB
	policy     PostgresPolicy
}

type lazyPostgresDB struct {
	connString string
	mu         sync.Mutex
	pool       *pgxpool.Pool
}

type PostgresQueryResult struct {
	Columns   []string         `json:"columns"`
	Rows      []map[string]any `json:"rows"`
	Truncated bool             `json:"truncated"`
}

type PostgresSchemaResult struct {
	Tables []PostgresTableSchema `json:"tables"`
}

type PostgresTableSchema struct {
	Schema  string                 `json:"schema"`
	Name    string                 `json:"name"`
	Columns []PostgresColumnSchema `json:"columns"`
}

type PostgresColumnSchema struct {
	Name     string `json:"name"`
	DataType string `json:"data_type"`
	Nullable bool   `json:"nullable"`
}

type PostgresExplainResult struct {
	Plan json.RawMessage `json:"plan"`
}

type PostgresIndexResult struct {
	Indexes []PostgresIndex `json:"indexes"`
}

type PostgresIndex struct {
	Schema string `json:"schema"`
	Table  string `json:"table"`
	Name   string `json:"name"`
	Def    string `json:"definition"`
}

func NewPostgresProvider(resourceID uuid.UUID, db postgresDB, policy PostgresPolicy) *PostgresProvider {
	if policy.MaxRows <= 0 {
		policy.MaxRows = 100
	}
	if policy.StatementTimeoutMs <= 0 {
		policy.StatementTimeoutMs = 5000
	}
	return &PostgresProvider{resourceID: resourceID, db: db, policy: policy}
}

func NewPostgresProviderFromConnString(resourceID uuid.UUID, connString string, policy PostgresPolicy) *PostgresProvider {
	return NewPostgresProvider(resourceID, &lazyPostgresDB{connString: connString}, policy)
}

func (db *lazyPostgresDB) BeginTx(ctx context.Context, txOptions pgx.TxOptions) (pgx.Tx, error) {
	db.mu.Lock()
	defer db.mu.Unlock()
	if db.pool == nil {
		pool, err := pgxpool.New(ctx, db.connString)
		if err != nil {
			return nil, err
		}
		db.pool = pool
	}
	return db.pool.BeginTx(ctx, txOptions)
}

func (p *PostgresProvider) Name() string { return "postgres" }

func (p *PostgresProvider) Version() string { return "v1" }

func (p *PostgresProvider) Capabilities() []string {
	return []string{
		"postgres.query",
		"postgres.read_query",
		"postgres.schema",
		"postgres.explain",
		"postgres.sample_rows",
		"postgres.indexes",
	}
}

func (p *PostgresProvider) HandleAction(ctx context.Context, req ActionRequest) (ActionResult, error) {
	if req.ResourceID != p.resourceID {
		return ActionResult{}, ErrResourceUnauthorized
	}
	switch req.Capability {
	case "postgres.query", "postgres.read_query":
		return p.handleQuery(ctx, req)
	case "postgres.schema":
		return p.handleSchema(ctx, req)
	case "postgres.explain":
		return p.handleExplain(ctx, req)
	case "postgres.sample_rows":
		return p.handleSampleRows(ctx, req)
	case "postgres.indexes":
		return p.handleIndexes(ctx, req)
	default:
		return ActionResult{}, ErrCapabilityUnsupported
	}
}

func (p *PostgresProvider) handleQuery(ctx context.Context, req ActionRequest) (ActionResult, error) {
	var params struct {
		Query string `json:"query"`
		Limit *int   `json:"limit,omitempty"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return ActionResult{}, err
	}
	query := strings.TrimSpace(params.Query)
	if query == "" {
		return ActionResult{}, fmt.Errorf("%w: query is required", ErrPostgresQueryDenied)
	}
	if err := p.validateReadQuery(query); err != nil {
		return ActionResult{}, err
	}
	limit := p.policy.MaxRows
	if params.Limit != nil && *params.Limit > 0 && *params.Limit < limit {
		limit = *params.Limit
	}
	start := time.Now()
	result, err := p.query(ctx, query, limit)
	if err != nil {
		return ActionResult{}, err
	}
	redactPostgresRows(result.Rows, p.policy.RedactColumns, p.policy.RedactPatterns)
	payload, err := json.Marshal(result)
	if err != nil {
		return ActionResult{}, err
	}
	return ActionResult{
		Payload: payload,
		Metadata: ActionMetadata{
			ResultCount: len(result.Rows),
			FieldCount:  len(result.Columns),
			DurationMs:  int(time.Since(start).Milliseconds()),
		},
	}, nil
}

func (p *PostgresProvider) handleSchema(ctx context.Context, req ActionRequest) (ActionResult, error) {
	var params struct {
		Schema string `json:"schema,omitempty"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return ActionResult{}, err
	}
	schema := strings.TrimSpace(params.Schema)
	if schema != "" && !p.schemaAllowed(schema) {
		return ActionResult{}, fmt.Errorf("%w: schema %s is not allowed", ErrPostgresQueryDenied, schema)
	}
	start := time.Now()
	result, err := p.schema(ctx, schema)
	if err != nil {
		return ActionResult{}, err
	}
	payload, err := json.Marshal(result)
	if err != nil {
		return ActionResult{}, err
	}
	return ActionResult{
		Payload:  payload,
		Metadata: ActionMetadata{ResultCount: len(result.Tables), DurationMs: int(time.Since(start).Milliseconds())},
	}, nil
}

func (p *PostgresProvider) handleExplain(ctx context.Context, req ActionRequest) (ActionResult, error) {
	var params struct {
		Query string `json:"query"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return ActionResult{}, err
	}
	query := strings.TrimSpace(params.Query)
	if query == "" {
		return ActionResult{}, fmt.Errorf("%w: query is required", ErrPostgresQueryDenied)
	}
	if err := p.validateReadQuery(query); err != nil {
		return ActionResult{}, err
	}
	start := time.Now()
	result, err := p.explain(ctx, query)
	if err != nil {
		return ActionResult{}, err
	}
	payload, err := json.Marshal(result)
	if err != nil {
		return ActionResult{}, err
	}
	return ActionResult{
		Payload:  payload,
		Metadata: ActionMetadata{ResultCount: 1, DurationMs: int(time.Since(start).Milliseconds())},
	}, nil
}

func (p *PostgresProvider) handleSampleRows(ctx context.Context, req ActionRequest) (ActionResult, error) {
	if !p.policy.AllowSampleRows {
		return ActionResult{}, fmt.Errorf("%w: sample_rows is not enabled", ErrPostgresQueryDenied)
	}
	var params struct {
		Schema string `json:"schema,omitempty"`
		Table  string `json:"table"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return ActionResult{}, err
	}
	schema := strings.TrimSpace(params.Schema)
	if schema == "" {
		schema = "public"
	}
	table := strings.TrimSpace(params.Table)
	if table == "" {
		return ActionResult{}, fmt.Errorf("%w: table is required", ErrPostgresQueryDenied)
	}
	if err := p.validateTablePolicy(schema, table); err != nil {
		return ActionResult{}, err
	}
	start := time.Now()
	result, err := p.sampleRows(ctx, schema, table)
	if err != nil {
		return ActionResult{}, err
	}
	redactPostgresRows(result.Rows, p.policy.RedactColumns, p.policy.RedactPatterns)
	payload, err := json.Marshal(result)
	if err != nil {
		return ActionResult{}, err
	}
	return ActionResult{
		Payload:  payload,
		Metadata: ActionMetadata{ResultCount: len(result.Rows), FieldCount: len(result.Columns), DurationMs: int(time.Since(start).Milliseconds())},
	}, nil
}

func (p *PostgresProvider) handleIndexes(ctx context.Context, req ActionRequest) (ActionResult, error) {
	var params struct {
		Schema string `json:"schema,omitempty"`
		Table  string `json:"table,omitempty"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return ActionResult{}, err
	}
	schema := strings.TrimSpace(params.Schema)
	table := strings.TrimSpace(params.Table)
	if schema != "" && !p.schemaAllowed(schema) {
		return ActionResult{}, fmt.Errorf("%w: schema %s is not allowed", ErrPostgresQueryDenied, schema)
	}
	if schema != "" && table != "" {
		if err := p.validateTablePolicy(schema, table); err != nil {
			return ActionResult{}, err
		}
	}
	start := time.Now()
	result, err := p.indexes(ctx, schema, table)
	if err != nil {
		return ActionResult{}, err
	}
	payload, err := json.Marshal(result)
	if err != nil {
		return ActionResult{}, err
	}
	return ActionResult{
		Payload:  payload,
		Metadata: ActionMetadata{ResultCount: len(result.Indexes), DurationMs: int(time.Since(start).Milliseconds())},
	}, nil
}

func (p *PostgresProvider) query(ctx context.Context, query string, limit int) (PostgresQueryResult, error) {
	limited, err := limitedPostgresQuery(query, limit)
	if err != nil {
		return PostgresQueryResult{}, err
	}
	var out PostgresQueryResult
	err = p.withReadOnlyTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, limited)
		if err != nil {
			return err
		}
		defer rows.Close()
		result, err := collectPostgresRows(rows, limit)
		if err != nil {
			return err
		}
		out = result
		return nil
	})
	return out, err
}

func (p *PostgresProvider) schema(ctx context.Context, schema string) (PostgresSchemaResult, error) {
	var out PostgresSchemaResult
	err := p.withReadOnlyTx(ctx, func(tx pgx.Tx) error {
		query := `SELECT table_schema, table_name, column_name, data_type, is_nullable
			FROM information_schema.columns
			WHERE table_schema NOT IN ('pg_catalog', 'information_schema')`
		args := pgx.NamedArgs{}
		if schema != "" {
			query += ` AND table_schema = @schema`
			args["schema"] = schema
		}
		if len(p.policy.AllowedSchemas) > 0 && schema == "" {
			query += ` AND table_schema = ANY(@allowed_schemas)`
			args["allowed_schemas"] = p.policy.AllowedSchemas
		}
		query += ` ORDER BY table_schema, table_name, ordinal_position`
		rows, err := tx.Query(ctx, query, args)
		if err != nil {
			return err
		}
		defer rows.Close()
		var current *PostgresTableSchema
		for rows.Next() {
			var tableSchema, tableName, columnName, dataType, nullable string
			if err := rows.Scan(&tableSchema, &tableName, &columnName, &dataType, &nullable); err != nil {
				return err
			}
			if !p.tableAllowed(tableSchema, tableName) {
				continue
			}
			if current == nil || current.Schema != tableSchema || current.Name != tableName {
				out.Tables = append(out.Tables, PostgresTableSchema{Schema: tableSchema, Name: tableName})
				current = &out.Tables[len(out.Tables)-1]
			}
			current.Columns = append(current.Columns, PostgresColumnSchema{Name: columnName, DataType: dataType, Nullable: nullable == "YES"})
		}
		return rows.Err()
	})
	return out, err
}

func (p *PostgresProvider) explain(ctx context.Context, query string) (PostgresExplainResult, error) {
	var out PostgresExplainResult
	err := p.withReadOnlyTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, "EXPLAIN (FORMAT JSON) "+strings.TrimSuffix(strings.TrimSpace(query), ";"))
		if err != nil {
			return err
		}
		defer rows.Close()
		if !rows.Next() {
			return fmt.Errorf("%w: explain returned no rows", ErrPostgresQueryDenied)
		}
		var plan string
		if err := rows.Scan(&plan); err != nil {
			return err
		}
		out.Plan = json.RawMessage(plan)
		return rows.Err()
	})
	return out, err
}

func (p *PostgresProvider) sampleRows(ctx context.Context, schema string, table string) (PostgresQueryResult, error) {
	query := "SELECT * FROM " + pgx.Identifier{schema, table}.Sanitize() + " LIMIT 11"
	var out PostgresQueryResult
	err := p.withReadOnlyTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, query)
		if err != nil {
			return err
		}
		defer rows.Close()
		result, err := collectPostgresRows(rows, 10)
		if err != nil {
			return err
		}
		out = result
		return nil
	})
	return out, err
}

func (p *PostgresProvider) indexes(ctx context.Context, schema string, table string) (PostgresIndexResult, error) {
	var out PostgresIndexResult
	err := p.withReadOnlyTx(ctx, func(tx pgx.Tx) error {
		query := `SELECT schemaname, tablename, indexname, indexdef
			FROM pg_indexes
			WHERE schemaname NOT IN ('pg_catalog', 'information_schema')`
		args := pgx.NamedArgs{}
		if schema != "" {
			query += ` AND schemaname = @schema`
			args["schema"] = schema
		}
		if table != "" {
			query += ` AND tablename = @table`
			args["table"] = table
		}
		if len(p.policy.AllowedSchemas) > 0 && schema == "" {
			query += ` AND schemaname = ANY(@allowed_schemas)`
			args["allowed_schemas"] = p.policy.AllowedSchemas
		}
		query += ` ORDER BY schemaname, tablename, indexname`
		rows, err := tx.Query(ctx, query, args)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var idx PostgresIndex
			if err := rows.Scan(&idx.Schema, &idx.Table, &idx.Name, &idx.Def); err != nil {
				return err
			}
			if p.tableAllowed(idx.Schema, idx.Table) {
				out.Indexes = append(out.Indexes, idx)
			}
		}
		return rows.Err()
	})
	return out, err
}

func (p *PostgresProvider) withReadOnlyTx(ctx context.Context, fn func(tx pgx.Tx) error) error {
	tx, err := p.db.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, fmt.Sprintf("SET LOCAL statement_timeout = %d", p.policy.StatementTimeoutMs)); err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func collectPostgresRows(rows pgx.Rows, limit int) (PostgresQueryResult, error) {
	fields := rows.FieldDescriptions()
	columns := make([]string, 0, len(fields))
	for _, field := range fields {
		columns = append(columns, field.Name)
	}
	out := PostgresQueryResult{Columns: columns, Rows: make([]map[string]any, 0, limit)}
	for rows.Next() {
		values, err := rows.Values()
		if err != nil {
			return PostgresQueryResult{}, err
		}
		if len(out.Rows) >= limit {
			out.Truncated = true
			break
		}
		row := make(map[string]any, len(columns))
		for i, column := range columns {
			row[column] = postgresJSONValue(values[i])
		}
		out.Rows = append(out.Rows, row)
	}
	return out, rows.Err()
}

func containsDeniedPostgresOperation(query string) bool {
	lower := strings.ToLower(query)
	return strings.Contains(lower, "copy") || strings.Contains(strings.TrimSuffix(strings.TrimSpace(query), ";"), ";")
}

func limitedPostgresQuery(query string, limit int) (string, error) {
	query = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(query), ";"))
	if query == "" {
		return "", fmt.Errorf("%w: query is required", ErrPostgresQueryDenied)
	}
	if limit <= 0 {
		limit = 100
	}
	return fmt.Sprintf("SELECT * FROM (%s) AS private_connector_limited LIMIT %d", query, limit+1), nil
}

func (p *PostgresProvider) validateReadQuery(query string) error {
	if containsDeniedPostgresOperation(query) {
		return fmt.Errorf("%w: COPY and multi-statement queries are not allowed", ErrPostgresQueryDenied)
	}
	for _, denied := range p.policy.DeniedTables {
		normalized := strings.ToLower(strings.TrimSpace(denied))
		if normalized != "" && strings.Contains(strings.ToLower(query), normalized) {
			return fmt.Errorf("%w: table %s is denied", ErrPostgresQueryDenied, denied)
		}
	}
	return nil
}

func (p *PostgresProvider) validateTablePolicy(schema string, table string) error {
	if !p.tableAllowed(schema, table) {
		return fmt.Errorf("%w: table %s.%s is not allowed", ErrPostgresQueryDenied, schema, table)
	}
	return nil
}

func (p *PostgresProvider) tableAllowed(schema string, table string) bool {
	if !p.schemaAllowed(schema) {
		return false
	}
	full := strings.ToLower(strings.TrimSpace(schema + "." + table))
	for _, denied := range p.policy.DeniedTables {
		if strings.ToLower(strings.TrimSpace(denied)) == full {
			return false
		}
	}
	return true
}

func (p *PostgresProvider) schemaAllowed(schema string) bool {
	if len(p.policy.AllowedSchemas) == 0 {
		return true
	}
	for _, allowed := range p.policy.AllowedSchemas {
		if strings.EqualFold(strings.TrimSpace(allowed), strings.TrimSpace(schema)) {
			return true
		}
	}
	return false
}

func postgresJSONValue(value any) any {
	switch v := value.(type) {
	case []byte:
		return string(v)
	default:
		return v
	}
}

func redactPostgresRows(rows []map[string]any, columns []string, patterns []string) {
	if len(columns) == 0 && len(patterns) == 0 {
		return
	}
	redacted := make(map[string]struct{}, len(columns))
	for _, column := range columns {
		redacted[strings.ToLower(strings.TrimSpace(column))] = struct{}{}
	}
	compiled := make([]*regexp.Regexp, 0, len(patterns))
	for _, pattern := range patterns {
		re, err := regexp.Compile(pattern)
		if err == nil {
			compiled = append(compiled, re)
		}
	}
	for _, row := range rows {
		for column := range row {
			if row[column] == nil {
				continue
			}
			if _, ok := redacted[strings.ToLower(column)]; ok {
				row[column] = "[REDACTED]"
				continue
			}
			for _, re := range compiled {
				if re.MatchString(column) {
					row[column] = "[REDACTED]"
					break
				}
			}
		}
	}
}
