package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"path/filepath"
	"slices"
	"sort"
	"strings"
)

// This is a separate status-policy ledger, not another lifecycle call graph.
// It resolves ordinary SQL table aliases and lexical subquery/CTE scopes, then
// fingerprints the whole query so changes to negation, joins or surrounding
// eligibility cannot silently keep a reviewed status set. It is deliberately
// not a SQL parser, Go type checker, or proof of dynamic query completeness.
type jobStatusInventory struct {
	Version     int              `json:"version"`
	Scope       []string         `json:"scope"`
	Limitations []string         `json:"limitations"`
	Entries     []jobStatusEntry `json:"entries"`
}

type jobStatusEntry struct {
	ID                string                     `json:"id"`
	SourceSHA256      string                     `json:"source_sha256"`
	Classification    string                     `json:"classification"`
	Action0a          string                     `json:"action_0a"`
	Reason            string                     `json:"reason"`
	Evidence          []string                   `json:"evidence"`
	PredicatePolicies []jobStatusPredicatePolicy `json:"predicate_policies,omitempty"`
}

type jobStatusPredicatePolicy struct {
	Fact           string `json:"fact"`
	Classification string `json:"classification"`
	Action0a       string `json:"action_0a"`
}

type jobStatusCandidate struct {
	ID           string   `json:"id"`
	Line         int      `json:"line"`
	SourceSHA256 string   `json:"source_sha256"`
	Kind         string   `json:"kind"`
	Facts        []string `json:"facts"`
	SQL          string   `json:"sql,omitempty"`
}

func runJobStatus(root, manifest string, discover bool, out io.Writer) error {
	found, err := scanJobStatus(root)
	if err != nil {
		return err
	}
	if discover {
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		return encoder.Encode(found)
	}
	data, err := readRepositoryFile(root, filepath.Join(root, manifest))
	if err != nil {
		return fmt.Errorf("read jobs-status inventory: %w", err)
	}
	var reviewed jobStatusInventory
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&reviewed); err != nil {
		return fmt.Errorf("decode jobs-status inventory: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("jobs-status inventory must contain exactly one JSON object")
	}
	if err := compareJobStatus(found, reviewed); err != nil {
		return err
	}
	_, err = fmt.Fprintf(out, "jobs-status inventory: %d classified syntax sites match; not SQL/type or runtime-policy proof\n", len(found))
	return err
}

func scanJobStatus(root string) ([]jobStatusCandidate, error) {
	type source struct {
		path string
		file *ast.File
	}
	fset := token.NewFileSet()
	var sources []source
	packages := map[string][]*ast.File{}
	for _, dir := range []string{"cmd", "internal"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if d.IsDir() && path == filepath.Join(root, "cmd", "lint-lifecycle") {
				return filepath.SkipDir
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			src, err := readRepositoryFile(root, path)
			if err != nil {
				return err
			}
			file, err := parser.ParseFile(fset, path, src, 0)
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			rel = filepath.ToSlash(rel)
			sources = append(sources, source{rel, file})
			packages[filepath.Dir(rel)] = append(packages[filepath.Dir(rel)], file)
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	values := map[string]map[string]string{}
	for pkg, files := range packages {
		values[pkg] = packageStringConstants(files)
	}
	var found []jobStatusCandidate
	add := func(id string, line int, kind string, facts []string, query string) {
		digest := sha256.Sum256([]byte(kind + "\n" + strings.Join(facts, "\n") + "\n" + query))
		found = append(found, jobStatusCandidate{ID: id, Line: line, SourceSHA256: hex.EncodeToString(digest[:]), Kind: kind, Facts: facts, SQL: query})
	}
	for _, src := range sources {
		for _, declaration := range src.file.Decls {
			fn, ok := declaration.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			constants := map[string]string{}
			for name, value := range values[filepath.Dir(src.path)] {
				constants[name] = value
			}
			// Assignment-order evaluation covers ordinary query construction and
			// += fragments. Branch/alias/loop semantics are not proved here.
			seenSQL := map[string]bool{}
			sqlOrdinal, goOrdinal := 0, 0
			ast.Inspect(fn.Body, func(node ast.Node) bool {
				switch n := node.(type) {
				case *ast.ValueSpec:
					for i, name := range n.Names {
						if i < len(n.Values) {
							constants[name.Name] = jobQueryString(n.Values[i], constants)
						}
					}
				case *ast.AssignStmt:
					for i, lhs := range n.Lhs {
						if name, ok := lhs.(*ast.Ident); ok && i < len(n.Rhs) {
							value := jobQueryString(n.Rhs[i], constants)
							if n.Tok == token.ADD_ASSIGN {
								value = constants[name.Name] + value
							}
							constants[name.Name] = value
						}
					}
				case *ast.CallExpr:
					// SQL may be passed through a local lease/recovery helper rather
					// than directly to DBTX. Do not trust method-name allowlists.
					if expression(n.Fun) == "fmt.Sprintf" {
						break // A constructor's template is not a second SQL sink.
					}
					captured := false
					for _, argument := range n.Args {
						raw := jobQueryString(argument, constants)
						query := canonicalJobSQL(raw)
						facts := jobSQLStatusFacts(raw)
						if len(facts) == 0 || seenSQL[query] {
							continue
						}
						seenSQL[query] = true
						captured = true
						sqlOrdinal++
						add(fmt.Sprintf("%s:%s:sql:%d", src.path, symbol(fn), sqlOrdinal), fset.Position(argument.Pos()).Line, "jobs-sql", facts, query)
					}
					if captured {
						return false // Do not double-count inner fmt.Sprintf fragments.
					}
				case *ast.IfStmt:
					fact := expression(n.Cond)
					if strings.Contains(fact, "models.JobStatus") || strings.Contains(fact, "jobStatus") {
						goOrdinal++
						add(fmt.Sprintf("%s:%s:go:%d", src.path, symbol(fn), goOrdinal), fset.Position(n.Pos()).Line, "go-job-status-syntax", []string{fact}, "")
					}
				case *ast.SwitchStmt:
					fact := "switch "
					if n.Tag != nil {
						fact += expression(n.Tag)
					}
					for _, statement := range n.Body.List {
						clause := statement.(*ast.CaseClause)
						fact += ";case" + boundaryCall("", clause.List)
					}
					if strings.Contains(fact, "models.JobStatus") || fn.Recv != nil && receiverType(fn) == "JobStatus" {
						goOrdinal++
						add(fmt.Sprintf("%s:%s:go:%d", src.path, symbol(fn), goOrdinal), fset.Position(n.Pos()).Line, "go-job-status-syntax", []string{fact}, "")
					}
				}
				return true
			})
		}
	}
	migrations, err := filepath.Glob(filepath.Join(root, "migrations", "*.sql"))
	if err != nil {
		return nil, err
	}
	for _, path := range migrations {
		data, err := readRepositoryFile(root, path)
		if err != nil {
			return nil, err
		}
		query := canonicalJobSQL(string(data))
		if facts := jobSQLStatusFacts(string(data)); len(facts) > 0 {
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return nil, err
			}
			add(filepath.ToSlash(rel)+":SQL", 1, "historical-jobs-sql", facts, query)
		}
	}
	sort.Slice(found, func(i, j int) bool { return found[i].ID < found[j].ID })
	return found, nil
}

func jobQueryString(expr ast.Expr, values map[string]string) string {
	switch n := expr.(type) {
	case *ast.BinaryExpr:
		if n.Op == token.ADD {
			return jobQueryString(n.X, values) + jobQueryString(n.Y, values)
		}
	case *ast.ParenExpr:
		return jobQueryString(n.X, values)
	case *ast.CallExpr:
		if expression(n.Fun) == "fmt.Sprintf" && len(n.Args) > 0 {
			value := jobQueryString(n.Args[0], values)
			for _, argument := range n.Args[1:] {
				if at := strings.Index(value, "%s"); at >= 0 {
					value = value[:at] + jobQueryString(argument, values) + value[at+2:]
				}
			}
			return value
		}
	}
	return constantString(expr, values)
}

// Tokens retain string-literal identity so words such as "jobs" or "SELECT"
// inside JSON/status literals cannot create a spurious table or query scope.
type jobSQLToken struct {
	text   string
	quoted bool
}

func jobSQLTokens(query string) []jobSQLToken {
	var tokens []jobSQLToken
	for i := 0; i < len(query); {
		c := query[i]
		if c == ' ' || c == '\n' || c == '\t' || c == '\r' {
			i++
			continue
		}
		if i+1 < len(query) && query[i:i+2] == "--" {
			if end := strings.IndexByte(query[i:], '\n'); end >= 0 {
				i += end
				continue
			}
			break
		}
		if i+1 < len(query) && query[i:i+2] == "/*" {
			if end := strings.Index(query[i+2:], "*/"); end >= 0 {
				i += end + 4
				continue
			}
			break
		}
		if c == '\'' || c == '"' {
			start, quote := i, c
			i++
			for i < len(query) {
				if query[i] == quote {
					i++
					if i < len(query) && query[i] == quote {
						i++
						continue
					}
					break
				}
				i++
			}
			value := query[start:i]
			if quote == '"' {
				value = strings.Trim(value, "\"")
			}
			tokens = append(tokens, jobSQLToken{text: value, quoted: quote == '\''})
			continue
		}
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '_' || c >= '0' && c <= '9' || c == '$' || c == '@' {
			start := i
			for i < len(query) {
				c = query[i]
				if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '_' || c >= '0' && c <= '9' || c == '$' || c == '@') {
					break
				}
				i++
			}
			tokens = append(tokens, jobSQLToken{text: strings.ToLower(query[start:i])})
			continue
		}
		if i+1 < len(query) && slices.Contains([]string{"<>", "!=", "<=", ">=", "::"}, query[i:i+2]) {
			tokens = append(tokens, jobSQLToken{text: query[i : i+2]})
			i += 2
			continue
		}
		tokens = append(tokens, jobSQLToken{text: string(c)})
		i++
	}
	return tokens
}

func canonicalJobSQL(query string) string {
	var parts []string
	for _, token := range jobSQLTokens(query) {
		parts = append(parts, token.text)
	}
	return strings.Join(parts, " ")
}

func jobSQLStatusFacts(query string) []string {
	tokens := jobSQLTokens(query)
	type scope struct {
		depth   int
		parent  *scope
		aliases map[string]bool // false records a non-jobs alias, shadowing outer aliases
		jobs    bool
	}
	root := &scope{depth: -1, aliases: map[string]bool{}}
	current, depth := root, 0
	owners := make([]*scope, len(tokens))
	reserved := strings.Fields("where set on join left right inner outer full cross group order limit returning for union having using values select from update delete insert into as and or offset conflict do")
	for i, token := range tokens {
		word := token.text
		if !token.quoted && word == ";" {
			root = &scope{depth: -1, aliases: map[string]bool{}}
			current, depth = root, 0
		}
		if !token.quoted && word == ")" {
			depth--
			for current != root && current.depth > depth {
				current = current.parent
			}
		}
		if !token.quoted && slices.Contains([]string{"select", "update", "delete", "insert"}, word) {
			if current != root && current.depth == depth {
				current = current.parent
			}
			current = &scope{depth: depth, parent: current, aliases: map[string]bool{}}
		}
		owners[i] = current
		if !token.quoted && slices.Contains([]string{"from", "join", "update", "into", "table", "on"}, word) && i+1 < len(tokens) {
			j := i + 1
			if tokens[j].text == "if" && j+3 < len(tokens) {
				j += 3 // IF NOT EXISTS
			}
			if tokens[j].quoted || tokens[j].text == "(" {
				continue
			}
			table := tokens[j].text
			if j+2 < len(tokens) && tokens[j+1].text == "." {
				j += 2
				table = tokens[j].text
			}
			if table == "jobs" {
				current.jobs = true
			}
			current.aliases[table] = table == "jobs"
			j++
			if j < len(tokens) && tokens[j].text == "as" {
				j++
			}
			if j < len(tokens) && !tokens[j].quoted && !slices.Contains(reserved, tokens[j].text) && tokens[j].text != "(" && tokens[j].text != ")" && tokens[j].text != "," {
				current.aliases[tokens[j].text] = table == "jobs"
			}
		}
		if !token.quoted && word == "(" {
			depth++
		}
	}
	var facts []string
	derivedJobStatus := false
	for i, token := range tokens {
		if token.text == "status" && !token.quoted && i >= 2 && tokens[i-1].text == "." && i+2 < len(tokens) && tokens[i+1].text == "as" && tokens[i+2].text == "job_status" {
			for s := owners[i]; s != nil; s = s.parent {
				if value, exists := s.aliases[tokens[i-2].text]; exists {
					derivedJobStatus = derivedJobStatus || value
					break
				}
			}
		}
	}
	for i, token := range tokens {
		if token.quoted || (token.text != "status" && !(derivedJobStatus && token.text == "job_status")) {
			continue
		}
		owner := owners[i]
		isJobs := derivedJobStatus && token.text == "job_status"
		start := i
		if i >= 2 && tokens[i-1].text == "." && !isJobs {
			alias := tokens[i-2].text
			start = i - 2
			for s := owner; s != nil; s = s.parent {
				if value, exists := s.aliases[alias]; exists {
					isJobs = value
					break
				}
			}
		} else if !isJobs {
			isJobs = owner.jobs
		}
		if isJobs && i >= 2 && tokens[i-1].text == "." {
			start = i - 2
		}
		if !isJobs {
			continue
		}
		end := i + 1
		if end < len(tokens) && slices.Contains([]string{"=", "!=", "<>", "in", "not", "is"}, tokens[end].text) {
			level := 0
			for ; end < len(tokens); end++ {
				word := tokens[end].text
				if !tokens[end].quoted && word == "(" {
					level++
				}
				if !tokens[end].quoted && word == ")" {
					if level == 0 {
						break
					}
					level--
				}
				if level == 0 && !tokens[end].quoted && slices.Contains([]string{"and", "or", ",", "from", "join", "where", "returning", "for", "order", "group", "offset", "limit", "union", ";", "then", "else"}, word) {
					break
				}
			}
		}
		var parts []string
		for _, token := range tokens[start:end] {
			parts = append(parts, token.text)
		}
		facts = append(facts, strings.Join(parts, " "))
	}
	return sortedUnique(facts)
}

func compareJobStatus(found []jobStatusCandidate, reviewed jobStatusInventory) error {
	var failures []string
	if reviewed.Version != 1 || len(reviewed.Scope) == 0 || len(reviewed.Limitations) == 0 {
		failures = append(failures, "jobs-status inventory requires version 1, scope and limitations")
	}
	entries := map[string]jobStatusEntry{}
	classes := []string{"runnable", "lease-owned", "nonterminal-consumer", "lifecycle-cleanup", "terminal-only", "observation", "unrelated-domain"}
	for _, entry := range reviewed.Entries {
		if _, exists := entries[entry.ID]; exists {
			failures = append(failures, "duplicate jobs-status site: "+entry.ID)
		}
		entries[entry.ID] = entry
		if entry.ID == "" || entry.SourceSHA256 == "" || !slices.Contains(classes, entry.Classification) || entry.Action0a == "" || entry.Reason == "" || len(entry.Evidence) == 0 {
			failures = append(failures, "unclassified jobs-status site: "+entry.ID)
		}
	}
	for _, candidate := range found {
		entry, exists := entries[candidate.ID]
		if !exists {
			failures = append(failures, "uncovered jobs-status site: "+candidate.ID)
			continue
		}
		if entry.SourceSHA256 != candidate.SourceSHA256 {
			failures = append(failures, "changed jobs-status source requires policy review: "+candidate.ID)
		}
		if len(entry.PredicatePolicies) > 0 {
			policies := map[string]bool{}
			for _, policy := range entry.PredicatePolicies {
				if policies[policy.Fact] || !slices.Contains(candidate.Facts, policy.Fact) || !slices.Contains(classes, policy.Classification) || policy.Action0a == "" {
					failures = append(failures, "invalid jobs-status predicate policy: "+candidate.ID)
				}
				policies[policy.Fact] = true
			}
			if len(policies) != len(candidate.Facts) {
				failures = append(failures, "incomplete jobs-status predicate policies: "+candidate.ID)
			}
		}
		delete(entries, candidate.ID)
	}
	for id := range entries {
		failures = append(failures, "stale jobs-status site: "+id)
	}
	sort.Strings(failures)
	if len(failures) > 0 {
		return fmt.Errorf("%s", strings.Join(failures, "\n"))
	}
	return nil
}
