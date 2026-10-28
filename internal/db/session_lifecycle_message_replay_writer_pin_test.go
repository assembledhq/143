package db

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"unicode"

	"github.com/stretchr/testify/require"
)

// This source pin covers the current literal/constant/concatenated SQL in the DB
// package. It is not a verifier for arbitrary dynamic SQL, stored procedures or
// writers outside this directory, nor a replacement for ownership review.
func TestMessageAdmissionReplayProgressMatchesActualWriters(t *testing.T) {
	t.Parallel()
	root, err := os.OpenRoot(".")
	require.NoError(t, err, "open the package source beneath one fixed filesystem root")
	t.Cleanup(func() { require.NoError(t, root.Close(), "close the bounded source-reader root") })
	entries, err := os.ReadDir(".")
	require.NoError(t, err, "enumerate only the current DB package's source files")
	constants := make(map[string]ast.Expr)
	functions := make(map[string]*ast.FuncDecl)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		source, err := root.ReadFile(entry.Name())
		require.NoError(t, err, "read source only through the fixed package root")
		file, err := parser.ParseFile(token.NewFileSet(), entry.Name(), source, 0)
		require.NoError(t, err, "parse the actual tracked Go writer implementations")
		for _, declaration := range file.Decls {
			switch node := declaration.(type) {
			case *ast.GenDecl:
				if node.Tok != token.CONST {
					continue
				}
				for _, spec := range node.Specs {
					value, ok := spec.(*ast.ValueSpec)
					if !ok {
						continue
					}
					for i, name := range value.Names {
						if i < len(value.Values) {
							constants[name.Name] = value.Values[i]
						}
					}
				}
			case *ast.FuncDecl:
				name := node.Name.Name
				if node.Recv != nil {
					receiver := node.Recv.List[0].Type
					if pointer, ok := receiver.(*ast.StarExpr); ok {
						receiver = pointer.X
					}
					switch generic := receiver.(type) {
					case *ast.IndexExpr:
						receiver = generic.X
					case *ast.IndexListExpr:
						receiver = generic.X
					}
					identifier, ok := receiver.(*ast.Ident)
					require.True(t, ok, "a new receiver shape requires deliberate writer-pin review")
					name = identifier.Name + "." + name
				}
				functions[name] = node
			}
		}
	}
	retrySQL, ok := replayWriterConstantString(constants["continuationRetryPayloadSQL"], constants, nil)
	require.True(t, ok, "derive recovery SQL from its actual constant and concatenated dependencies")
	attempt := functions["JobStore.RecordContinuationAttemptWithLease"]
	require.NotNil(t, attempt, "the real fenced continuation writer must remain explicitly inventoried")
	var attemptSQL string
	writerSQL := make(map[string][]string)
	var writers, recoveryCallers []string
	for name, function := range functions {
		hasPayloadWrite, hasRecoveryCall := false, false
		ast.Inspect(function.Body, func(node ast.Node) bool {
			if call, ok := node.(*ast.CallExpr); ok {
				if identifier, ok := call.Fun.(*ast.Ident); ok && identifier.Name == "continuationRecoveryQuery" {
					hasRecoveryCall = true
				}
			}
			expression, ok := node.(ast.Expr)
			if !ok {
				return true
			}
			sql, ok := replayWriterConstantString(expression, constants, nil)
			if !ok || !replaySQLWritesJobPayload(t, sql) {
				return true
			}
			hasPayloadWrite = true
			writerSQL[name] = append(writerSQL[name], sql)
			if function == attempt {
				attemptSQL = sql
			}
			// A concatenated SQL expression is one source site, not its nested
			// literal fragments as well. Full reviewed writer-body fingerprints
			// below also detect a new second write or dynamic helper invocation.
			return false
		})
		if hasPayloadWrite {
			writers = append(writers, name)
		}
		if hasRecoveryCall {
			recoveryCallers = append(recoveryCallers, name)
		}
	}
	slices.Sort(recoveryCallers)
	expectedRecovery := []string{
		"JobStore.ReclaimLostRunningJobs", "JobStore.ReclaimLostRunningSessionJobsForSession",
		"JobStore.RetryWithLease", "JobStore.RetryWithLeaseAndTarget", "JobStore.RetryWithoutConsumingAttemptWithLease",
		"JobStore.RetryWithoutConsumingAttemptWithLeaseAndResetWindow", "JobStore.RetryWithoutConsumingAttemptWithLeaseAndTarget",
		"SessionExecutorStore.ReclaimLost", "SessionExecutorStore.ReclaimLostForSession",
	}
	slices.Sort(expectedRecovery)
	require.Equal(t, expectedRecovery, recoveryCallers, "every retry/reclaim writer family using the shared recovery transform must be reviewed when it changes")
	expectedWriters := append(slices.Clone(expectedRecovery), "JobStore.RecordContinuationAttemptWithLease", "CodeReviewStore.EnsureFreshnessRetryStartedAt", "CodeReviewStore.EnsureCaptureIdentityRetryStartedAt")
	slices.Sort(expectedWriters)
	slices.Sort(writers)
	require.Equal(t, expectedWriters, writers, "any new DB SQL family mutating a job payload requires explicit replay-identity review")
	require.NotEmpty(t, attemptSQL, "extract actual continuation-attempt SQL rather than maintain a parallel key list")
	// Review-trigger syntax pins deliberately avoid pretending that a literal
	// occurrence proves a SQL boolean predicate. Full retry SQL pins the exact
	// excluded CASE branch; full review SQL pins both job-family exclusions; Go
	// body pins catch extra writes hidden inside an already inventoried symbol.
	fingerprints := map[string]string{"sql:continuationRetryPayloadSQL": replayWriterSQLFingerprint(t, retrySQL)}
	for _, name := range writers {
		fingerprints["body:"+name] = replayWriterBodyFingerprint(t, functions[name])
		if strings.HasPrefix(name, "CodeReviewStore.") {
			require.Len(t, writerSQL[name], 1, "each excluded review writer must retain exactly its reviewed SQL site")
			fingerprints["sql:"+name] = replayWriterSQLFingerprint(t, writerSQL[name][0])
		}
	}
	for _, name := range []string{"continuationRecoveryQuery", "messageAdmissionJobOptions"} {
		require.NotNil(t, functions[name], "the actual query transformer and original nonempty-input producer are dependencies of the reviewed exclusions")
		fingerprints["body:"+name] = replayWriterBodyFingerprint(t, functions[name])
	}
	require.Equal(t, replayWriterReviewedFingerprints, fingerprints, "changes to reviewed writer bodies or excluded SQL predicates require explicit replay-contract review, not an automatic fingerprint refresh")
	for _, name := range []string{"CodeReviewStore.EnsureFreshnessRetryStartedAt", "CodeReviewStore.EnsureCaptureIdentityRetryStartedAt"} {
		for _, replacement := range []string{"job_type IN ('run_code_review','continue_session')", "(job_type='run_code_review' OR job_type='continue_session')"} {
			changed := strings.Replace(writerSQL[name][0], "job_type='run_code_review'", replacement, 1)
			require.NotEqual(t, writerSQL[name][0], changed, "calibration must actually broaden the source-derived code-review exclusion")
			require.NotEqual(t, replayWriterReviewedFingerprints["sql:"+name], replayWriterSQLFingerprint(t, changed), "both IN and OR job-family broadening must invalidate the exact reviewed exclusion pin")
		}
	}
	unprovenBranch := ") THEN j.payload || jsonb_build_object('execution_purpose','checkpoint_recovery','recovery_unproven',true)"
	broadenedRetry := strings.Replace(retrySQL, unprovenBranch, ") OR TRUE THEN j.payload || jsonb_build_object('execution_purpose','checkpoint_recovery','recovery_unproven',true)", 1)
	require.NotEqual(t, retrySQL, broadenedRetry, "calibration must broaden the actual source-derived unreachable branch")
	require.NotEqual(t, replayWriterReviewedFingerprints["sql:continuationRetryPayloadSQL"], replayWriterSQLFingerprint(t, broadenedRetry), "retaining old substrings while making the excluded marker reachable must invalidate the full transform pin")
	changedWriter := *functions["JobStore.RetryWithLease"]
	changedBody := *changedWriter.Body
	changedBody.List = append(slices.Clone(changedBody.List), &ast.ExprStmt{X: &ast.CallExpr{
		Fun:  &ast.SelectorExpr{X: ast.NewIdent("q"), Sel: ast.NewIdent("Exec")},
		Args: []ast.Expr{ast.NewIdent("ctx"), &ast.BasicLit{Kind: token.STRING, Value: strconv.Quote(`UPDATE jobs SET payload=payload||'{"future_key":true}' WHERE id=$1`)}},
	}})
	changedWriter.Body = &changedBody
	require.NotEqual(t, replayWriterReviewedFingerprints["body:JobStore.RetryWithLease"], replayWriterBodyFingerprint(t, &changedWriter), "a second payload update inside an existing writer symbol must invalidate the reviewed body pin")
	keys := append(replaySQLJSONBuildKeys(t, retrySQL), replaySQLJSONBuildKeys(t, attemptSQL)...)
	slices.Sort(keys)
	keys = slices.Compact(keys)
	// This branch is unreachable for A1's nonempty queued input. Pin both its
	// exact writer expression and its empty-input precondition before exclusion.
	require.Contains(t, retrySQL, "CASE WHEN COALESCE(j.payload->>'continuation_runtime_id','') = '' AND COALESCE(j.payload->>'queued_message_id','') = '' AND EXISTS", "recovery-unproven exclusion requires the actual empty-input precondition")
	require.Contains(t, retrySQL, "jsonb_build_object('execution_purpose','checkpoint_recovery','recovery_unproven',true)", "the excluded unreachable marker must stay in its reviewed writer branch")
	require.Contains(t, keys, "recovery_unproven", "derive the unreachable marker from writer source before deliberately excluding it")
	keys = slices.DeleteFunc(keys, func(key string) bool { return key == "recovery_unproven" })
	a1 := functions["messageAdmissionJobOptions"]
	require.NotNil(t, a1, "derive A1-owned keys from the actual admission payload producer")
	var ownedKeys []string
	ast.Inspect(a1.Body, func(node ast.Node) bool {
		entry, ok := node.(*ast.KeyValueExpr)
		if !ok {
			return true
		}
		value, ok := replayWriterConstantString(entry.Key, constants, nil)
		if ok && slices.Contains(keys, value) {
			ownedKeys = append(ownedKeys, value)
		}
		return true
	})
	slices.Sort(ownedKeys)
	ownedKeys = slices.Compact(ownedKeys)
	require.Equal(t, []string{"queued_message_id", "thread_id"}, ownedKeys, "writer-set admission keys remain exact original input, never mutable progress")
	keys = slices.DeleteFunc(keys, func(key string) bool { return slices.Contains(ownedKeys, key) })
	actual := slices.Clone(messageAdmissionReplayProgressKeys[:])
	slices.Sort(actual)
	require.Equal(t, keys, actual, "replay may allow exactly the reachable keys derived from real writers minus immutable A1-owned keys")
}

// Conservative source-change review triggers, not independent semantic or
// runtime proof. Even a harmless Go-body edit intentionally requires review.
var replayWriterReviewedFingerprints = map[string]string{
	"sql:continuationRetryPayloadSQL":                                   "d3a4b05f89e77dd44ffce052f73be66b7c0956cffbe6e2d270172b1876bd10b5",
	"sql:CodeReviewStore.EnsureFreshnessRetryStartedAt":                 "fcbec2436367cba8159bb03a60703b17d5189fa558f20e7835de53576c576488",
	"sql:CodeReviewStore.EnsureCaptureIdentityRetryStartedAt":           "d5530b6565c3fefce20de8befdbafcc70f70ebc6f51ac9daacfd791c1e6fad3a",
	"body:CodeReviewStore.EnsureFreshnessRetryStartedAt":                "87b2c05548d3059ae680f8ec7210ceb24ccb7cd5a04218658b219b3e7101f771",
	"body:CodeReviewStore.EnsureCaptureIdentityRetryStartedAt":          "af52ec87eaf3f3dd9c2188e7fdbb2262a4baa44701527a22eda1bf40b3fac84b",
	"body:JobStore.RecordContinuationAttemptWithLease":                  "b9f6d0e4780346a0b7d8298f84cc163fbef7b790d5309e96adc3091d541cc512",
	"body:JobStore.RetryWithLease":                                      "baca5d87999efb9dcecc15f57d97f98a0d5b7f0cab2de858437a20e65c088885",
	"body:JobStore.RetryWithLeaseAndTarget":                             "b0b58b4776fa0166c20591ed9bc0eb2a4a3b51c5bd0abb34a3f172e8a63af25b",
	"body:JobStore.RetryWithoutConsumingAttemptWithLease":               "28bd3392742709a17ce45c83997a0ea3ff2f3f649d1293ed59e711048967d28b",
	"body:JobStore.RetryWithoutConsumingAttemptWithLeaseAndResetWindow": "54584af171559f063473764bf5ca8972f4c186ca83d337e2af5dac2dc9796a02",
	"body:JobStore.RetryWithoutConsumingAttemptWithLeaseAndTarget":      "309d9ae4925c4ae4d3ea34522a8c45395a7661849bb5f2afbe8b04fe50ad1de8",
	"body:JobStore.ReclaimLostRunningJobs":                              "71b92ae16fae30888ac793a3d108cfb1fd0af93521fdb584a37734ad7133fc0a",
	"body:JobStore.ReclaimLostRunningSessionJobsForSession":             "aa58e15e430775c62dcdace07ad0765914c5da86a2492a356491ebac46b00ad7",
	"body:SessionExecutorStore.ReclaimLost":                             "baad804ba72b6ff55a354625b1d361b59736b5b52381a749f4a0b029cc025042",
	"body:SessionExecutorStore.ReclaimLostForSession":                   "0a5af923e5efeba3d93f78926c96e99d19f47eadc5435023987c41255342f2ae",
	"body:continuationRecoveryQuery":                                    "53430b7210fdd1bde95dba322e1c9b3e6068c8815c20e38eb0a3f6d6019eb951",
	"body:messageAdmissionJobOptions":                                   "a21a50825df2096f7ef47455e1784d052f2e9fd83b270cf5b9024b83fe00bd6c",
}

func replayWriterSQLFingerprint(t *testing.T, sql string) string {
	t.Helper()
	var canonical strings.Builder
	for _, item := range replayWriterSQLTokens(t, sql) {
		if item.literal {
			canonical.WriteByte('L')
		} else {
			canonical.WriteByte('T')
		}
		canonical.WriteString(strconv.Quote(item.text))
		canonical.WriteByte('\n')
	}
	return fmt.Sprintf("%x", sha256.Sum256([]byte(canonical.String())))
}

func replayWriterBodyFingerprint(t *testing.T, function *ast.FuncDecl) string {
	t.Helper()
	var canonical bytes.Buffer
	require.NoError(t, format.Node(&canonical, token.NewFileSet(), function.Body), "canonicalize the actual reviewed Go writer body without comments or machine-local paths")
	return fmt.Sprintf("%x", sha256.Sum256(canonical.Bytes()))
}

func replayWriterConstantString(expression ast.Expr, constants map[string]ast.Expr, visiting map[string]bool) (string, bool) {
	switch node := expression.(type) {
	case *ast.BasicLit:
		if node.Kind != token.STRING {
			return "", false
		}
		value, err := strconv.Unquote(node.Value)
		return value, err == nil
	case *ast.BinaryExpr:
		if node.Op != token.ADD {
			return "", false
		}
		left, leftOK := replayWriterConstantString(node.X, constants, visiting)
		right, rightOK := replayWriterConstantString(node.Y, constants, visiting)
		return left + right, leftOK && rightOK
	case *ast.ParenExpr:
		return replayWriterConstantString(node.X, constants, visiting)
	case *ast.Ident:
		if visiting[node.Name] || constants[node.Name] == nil {
			return "", false
		}
		if visiting == nil {
			visiting = make(map[string]bool)
		}
		visiting[node.Name] = true
		value, ok := replayWriterConstantString(constants[node.Name], constants, visiting)
		delete(visiting, node.Name)
		return value, ok
	default:
		return "", false
	}
}

type replayWriterSQLToken struct {
	text    string
	literal bool
}

func replayWriterSQLTokens(t *testing.T, sql string) []replayWriterSQLToken {
	t.Helper()
	var tokens []replayWriterSQLToken
	for i := 0; i < len(sql); {
		switch {
		case unicode.IsSpace(rune(sql[i])):
			i++
		case strings.HasPrefix(sql[i:], "--"):
			for i < len(sql) && sql[i] != '\n' {
				i++
			}
		case strings.HasPrefix(sql[i:], "/*"):
			end := strings.Index(sql[i+2:], "*/")
			require.NotEqual(t, -1, end, "writer SQL comments must terminate before source-key extraction")
			i += end + 4
		case sql[i] == '\'' || sql[i] == '"':
			quote := sql[i]
			i++
			var value strings.Builder
			closed := false
			for i < len(sql) {
				if sql[i] == quote {
					if i+1 < len(sql) && sql[i+1] == quote {
						value.WriteByte(quote)
						i += 2
						continue
					}
					i++
					closed = true
					break
				}
				value.WriteByte(sql[i])
				i++
			}
			require.True(t, closed, "writer SQL quotes must terminate before source-key extraction")
			tokens = append(tokens, replayWriterSQLToken{text: value.String(), literal: quote == '\''})
		case unicode.IsLetter(rune(sql[i])) || sql[i] == '_':
			start := i
			for i < len(sql) && (unicode.IsLetter(rune(sql[i])) || unicode.IsDigit(rune(sql[i])) || sql[i] == '_') {
				i++
			}
			tokens = append(tokens, replayWriterSQLToken{text: strings.ToLower(sql[start:i])})
		default:
			tokens = append(tokens, replayWriterSQLToken{text: sql[i : i+1]})
			i++
		}
	}
	return tokens
}

func replaySQLWritesJobPayload(t *testing.T, sql string) bool {
	t.Helper()
	// Avoid treating arbitrary error strings or SQL fragments as whole writers.
	if !strings.Contains(strings.ToLower(sql), "update") || !strings.Contains(strings.ToLower(sql), "payload") {
		return false
	}
	tokens := replayWriterSQLTokens(t, sql)
	for i := 0; i+1 < len(tokens); i++ {
		if tokens[i].literal || tokens[i].text != "update" {
			continue
		}
		table := i + 1
		if table+2 < len(tokens) && tokens[table].text == "public" && tokens[table+1].text == "." {
			table += 2
		}
		if tokens[table].literal || tokens[table].text != "jobs" {
			continue
		}
		depth := 0
		for j := table + 1; j+1 < len(tokens); j++ {
			if tokens[j].literal {
				continue
			}
			switch tokens[j].text {
			case "(":
				depth++
			case ")":
				depth--
			}
			if depth < 0 || depth == 0 && (tokens[j].text == "where" || tokens[j].text == "returning") {
				break
			}
			if depth == 0 && tokens[j].text == "payload" && tokens[j+1].text == "=" {
				return true
			}
		}
	}
	return false
}

func TestMessageAdmissionReplayWriterSQLDetection(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, sql string
		writes    bool
	}{
		{name: "current aliased update", sql: `UPDATE jobs j SET status='pending',payload=j.payload WHERE id=$1`, writes: true},
		{name: "qualified and quoted table", sql: `UPDATE public."jobs" SET "payload"='{}' WHERE id=$1`, writes: true},
		{name: "nested where before payload assignment", sql: `UPDATE jobs SET status=(SELECT status FROM other WHERE id=$1),payload='{}' WHERE id=$1`, writes: true},
		{name: "another table is not a job writer", sql: `UPDATE other SET payload='{}' WHERE id=$1`},
		{name: "payload read is not a payload write", sql: `UPDATE jobs SET status='pending' WHERE payload='{}'`},
		{name: "literal text cannot invent a writer", sql: `SELECT 'UPDATE jobs SET payload=anything'`},
		{name: "comment cannot invent a writer", sql: `SELECT 1 /* UPDATE jobs SET payload=anything */`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.writes, replaySQLWritesJobPayload(t, tt.sql), "the source pin must recognize actual job payload assignments without treating reads comments or strings as new writers")
		})
	}
}

func replaySQLJSONBuildKeys(t *testing.T, sql string) []string {
	t.Helper()
	tokens := replayWriterSQLTokens(t, sql)
	var keys []string
	for i := 0; i+1 < len(tokens); i++ {
		if tokens[i].literal || tokens[i].text != "jsonb_build_object" || tokens[i+1].text != "(" {
			continue
		}
		depth, start, argument := 1, i+2, 0
		for j := start; j < len(tokens); j++ {
			if !tokens[j].literal {
				switch tokens[j].text {
				case "(", "[":
					depth++
				case ")", "]":
					depth--
				}
			}
			if depth != 0 && (depth != 1 || tokens[j].literal || tokens[j].text != ",") {
				continue
			}
			if argument%2 == 0 {
				require.Equal(t, start+1, j, "actual writer JSON keys must be single SQL literals, not a dynamic expression")
				require.True(t, tokens[start].literal, "derive JSON field names from actual writer SQL key literals")
				keys = append(keys, tokens[start].text)
			}
			argument++
			if depth == 0 {
				require.Equal(t, 0, argument%2, "actual writer JSON objects require matched key/value arguments")
				break
			}
			start = j + 1
		}
	}
	require.NotEmpty(t, keys, fmt.Sprintf("the real writer SQL must expose explicit JSON object keys: %q", sql[:min(32, len(sql))]))
	return keys
}
