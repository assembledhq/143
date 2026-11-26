package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSplitWords(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected []string
	}{
		{name: "snake case", input: "user_email", expected: []string{"user", "email"}},
		{name: "camel case", input: "githubLogin", expected: []string{"github", "login"}},
		{name: "leading acronym", input: "IPAddress", expected: []string{"ip", "address"}},
		{name: "inner acronym", input: "remoteIPAddr", expected: []string{"remote", "ip", "addr"}},
		{name: "trailing acronym", input: "userID", expected: []string{"user", "id"}},
		{name: "header name", input: "X-Forwarded-For", expected: []string{"x", "forwarded", "for"}},
		{name: "dotted key", input: "creator.email", expected: []string{"creator", "email"}},
		{name: "digits stay attached", input: "ipv4", expected: []string{"ipv4"}},
		{name: "empty", input: "", expected: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.expected, splitWords(tt.input), "splitWords should split %q into lowercase words", tt.input)
		})
	}
}

func TestClassifyName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		expected category
	}{
		{name: "email", expected: categoryEmail},
		{name: "user_email", expected: categoryEmail},
		{name: "creatorEmail", expected: categoryEmail},
		{name: "Emails", expected: categoryEmail},
		{name: "email_address", expected: categoryEmail},
		{name: "phone_number", expected: categoryPhone},
		{name: "mobileNumber", expected: categoryPhone},
		{name: "display_name", expected: categoryPersonName},
		{name: "FirstName", expected: categoryPersonName},
		{name: "inviterName", expected: categoryPersonName},
		{name: "user_full_name", expected: categoryPersonName},
		{name: "nickname", expected: categoryPersonName},
		{name: "github_login", expected: categoryAccountHandle},
		{name: "AuthorLogin", expected: categoryAccountHandle},
		{name: "username", expected: categoryAccountHandle},
		{name: "slack_handle", expected: categoryAccountHandle},
		{name: "ip", expected: categoryIPAddress},
		{name: "client_ip", expected: categoryIPAddress},
		{name: "RemoteAddr", expected: categoryIPAddress},
		{name: "ip_address", expected: categoryIPAddress},
		{name: "X-Forwarded-For", expected: categoryIPAddress},
		{name: "x-real-ip", expected: categoryIPAddress},
		{name: "user_agent", expected: categoryUserAgent},
		{name: "User-Agent", expected: categoryUserAgent},
		{name: "billing_address", expected: categoryPostalAddress},
		{name: "postal_code", expected: categoryPostalAddress},
		{name: "date_of_birth", expected: categorySensitiveID},
		{name: "ssn", expected: categorySensitiveID},
		{name: "card_number", expected: categorySensitiveID},

		{name: "email_domain"},
		{name: "email_verified"},
		{name: "email_template"},
		{name: "user_id"},
		{name: "slack_user_id"},
		{name: "login_method"},
		{name: "name"},
		{name: "container_name"},
		{name: "repo_full_name"},
		{name: "FullName"},
		{name: "handle"},
		{name: "preview_handle"},
		{name: "container_ip"},
		{name: "addr"},
		{name: "listen_address"},
		{name: "agent"},
		{name: "agent_type"},
		{name: "tooltip"},
		{name: "address"},
		{name: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, ok := classifyName(tt.name)
			require.Equal(t, tt.expected, got, "classifyName(%q) should return the expected category", tt.name)
			require.Equal(t, tt.expected != "", ok, "classifyName(%q) should report whether it matched", tt.name)
		})
	}
}

func TestCheckSource(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		body     string
		expected []finding
	}{
		{
			name:     "flags a personal-data field key",
			body:     `logger.Info().Str("email", v).Msg("invited")`,
			expected: []finding{logFinding(4, categoryEmail, `field "email"`)},
		},
		{
			name:     "flags a camelCase field key",
			body:     `logger.Info().Str("githubLogin", v).Msg("linked")`,
			expected: []finding{logFinding(4, categoryAccountHandle, `field "githubLogin"`)},
		},
		{
			name:     "flags a personal-data struct field under an innocuous key",
			body:     `logger.Info().Str("to", user.Email).Msg("sent")`,
			expected: []finding{logFinding(4, categoryEmail, "value user.Email")},
		},
		{
			name:     "flags a person's Name field by its receiver",
			body:     `logger.Info().Str("inviter", currentUser.Name).Msg("sent")`,
			expected: []finding{logFinding(4, categoryPersonName, "value currentUser.Name")},
		},
		{
			name:     "flags Msgf arguments",
			body:     `logger.Info().Msgf("sent to %s", email)`,
			expected: []finding{logFinding(4, categoryEmail, "value email")},
		},
		{
			name:     "flags string concatenation in Msg",
			body:     `logger.Info().Msg("failed for " + login)`,
			expected: []finding{logFinding(4, categoryAccountHandle, "value login")},
		},
		{
			name:     "flags client IP header lookups",
			body:     `logger.Info().Str("client", r.Header.Get("X-Forwarded-For")).Send()`,
			expected: []finding{logFinding(4, categoryIPAddress, `value r.Header.Get("X-Forwarded-For")`)},
		},
		{
			name:     "flags getter methods",
			body:     `logger.Info().Str("agent", r.UserAgent()).Send()`,
			expected: []finding{logFinding(4, categoryUserAgent, "value r.UserAgent()")},
		},
		{
			name:     "flags the request remote address",
			body:     `logger.Info().Str("peer", r.RemoteAddr).Send()`,
			expected: []finding{logFinding(4, categoryIPAddress, "value r.RemoteAddr")},
		},
		{
			name:     "flags a whole user record passed to Interface",
			body:     `logger.Info().Interface("payload", user).Send()`,
			expected: []finding{logFinding(4, categoryUserRecord, "value user")},
		},
		{
			name:     "flags a whole user record formatted with Sprintf",
			body:     `logger.Info().Str("dump", fmt.Sprintf("%+v", member)).Send()`,
			expected: []finding{logFinding(4, categoryUserRecord, "value member")},
		},
		{
			name:     "flags a whole header map",
			body:     `logger.Info().Interface("request", r.Header).Send()`,
			expected: []finding{logFinding(4, categoryHeaders, "value r.Header")},
		},
		{
			name:     "flags Fields map keys",
			body:     `logger.Info().Fields(map[string]any{"phone_number": p, "org_id": o}).Send()`,
			expected: []finding{logFinding(4, categoryPhone, `field "phone_number"`)},
		},
		{
			name:     "flags Fields slice keys",
			body:     `logger.Info().Fields([]any{"client_ip", v, "org_id", o}).Send()`,
			expected: []finding{logFinding(4, categoryIPAddress, `field "client_ip"`)},
		},
		{
			name:     "flags logger context fields",
			body:     `logger = logger.With().Str("display_name", n).Logger()`,
			expected: []finding{logFinding(4, categoryPersonName, `field "display_name"`)},
		},
		{
			name:     "flags map lookups by personal-data key",
			body:     `logger.Info().Str("who", claims["email"]).Send()`,
			expected: []finding{logFinding(4, categoryEmail, `value claims["email"]`)},
		},
		{
			name:     "flags standard library log calls",
			body:     `log.Printf("login %s", username)`,
			expected: []finding{logFinding(4, categoryAccountHandle, "value username")},
		},
		{
			name:     "flags errors built inline with personal data",
			body:     `logger.Warn().Err(fmt.Errorf("invite %s failed", invitee.Email)).Msg("invite failed")`,
			expected: []finding{logFinding(4, categoryEmail, "value invitee.Email")},
		},
		{
			name: "reports each field of a multi-line chain at its own line",
			body: `logger.Info().
		Str("email", body.Email).
		Str("role", body.Role).
		Str("github_username", body.GitHubUsername).
		Msg("invitation created")`,
			expected: []finding{
				logFinding(7, categoryAccountHandle, `field "github_username"`),
				logFinding(5, categoryEmail, `field "email"`),
			},
		},
		{
			name: "ignores metadata about personal data",
			body: `logger.Info().Str("email_domain", d).Bool("email_verified", ok).Int("emails", n).
		Int("email_len", len(email)).Str("email_hash", hashEmail(email)).Str("valid", strconv.FormatBool(isEmail(email))).Send()`,
		},
		{
			name: "ignores stable IDs",
			body: `logger.Info().Str("user_id", user.ID.String()).Str("slack_user_id", slackUserID).Send()`,
		},
		{
			name: "ignores names of things that are not people",
			body: `logger.Info().Str("repo", repo.FullName).Str("service", svc.Name).Str("container_name", c.Name).Str("handle", handle).Send()`,
		},
		{
			name: "ignores infrastructure addresses",
			body: `logger.Info().Str("container_ip", containerIP).Str("addr", addr).Send()`,
		},
		{
			name: "ignores comparisons",
			body: `logger.Info().Str("self", strconv.FormatBool(login == owner)).Send()`,
		},
		{
			name: "ignores message text that only mentions personal data",
			body: `logger.Info().Msg("failed to send verification email to user")`,
		},
		{
			name: "ignores CLI output and non-zerolog helpers",
			body: `fmt.Printf("signed in as %s\n", email)
	_ = strings.Fields(email)`,
		},
		{
			name: "allows a statement annotated with a reason",
			body: `// lint:allow-log-pii reason="abuse investigation needs the client IP"
	logger.Warn().Str("ip", ip).Msg("rate limited")`,
		},
		{
			name: "allows every field of an annotated multi-line statement",
			body: `// lint:allow-log-pii reason="admin audit trail"
	logger.Warn().
		Str("ip", ip).
		Str("email", e).
		Msg("x")`,
		},
		{
			name: "allows only the field an inline annotation precedes",
			body: `logger.Warn().
		// lint:allow-log-pii reason="abuse investigation needs the client IP"
		Str("ip", ip).
		Str("email", e).
		Msg("x")`,
			expected: []finding{logFinding(7, categoryEmail, `field "email"`)},
		},
		{
			name: "does not honor an annotation without a reason",
			body: `// lint:allow-log-pii
	logger.Warn().Str("ip", ip).Msg("rate limited")`,
			expected: []finding{logFinding(5, categoryIPAddress, `field "ip"`)},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			src := "package x\n\nfunc f() {\n\t" + tt.body + "\n}\n"
			result, err := checkSource("x.go", []byte(src))
			require.NoError(t, err, "checkSource should parse the fixture")
			require.Equal(t, tt.expected, result.findings, "checkSource should report exactly the expected findings")
		})
	}
}

func TestCheckSourceFunctionNames(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		src      string
		expected string
	}{
		{
			name:     "pointer receiver method",
			src:      "package x\n\nfunc (h *TeamHandler) Invite() { logger.Info().Str(\"email\", e).Send() }\n",
			expected: "(*TeamHandler).Invite",
		},
		{
			name:     "value receiver method",
			src:      "package x\n\nfunc (s Sender) Send() { logger.Info().Str(\"email\", e).Send() }\n",
			expected: "(Sender).Send",
		},
		{
			name:     "package-level variable",
			src:      "package x\n\nvar l = logger.With().Str(\"email\", e).Logger()\n",
			expected: "package scope",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result, err := checkSource("x.go", []byte(tt.src))
			require.NoError(t, err, "checkSource should parse the fixture")
			require.Len(t, result.findings, 1, "fixture should produce one finding")
			require.Equal(t, tt.expected, result.findings[0].Func, "finding should name the enclosing declaration")
		})
	}
}

func TestCheckSourceMarkerErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		src      string
		expected []string
	}{
		{
			name:     "reports a marker without a reason",
			src:      "package x\n\n// lint:allow-log-pii\nfunc f() {}\n",
			expected: []string{`x.go:3: lint:allow-log-pii needs a reason="..." clause`},
		},
		{
			name:     "reports a marker with an empty reason",
			src:      "package x\n\n// lint:allow-log-pii reason=\"\"\nfunc f() {}\n",
			expected: []string{`x.go:3: lint:allow-log-pii needs a reason="..." clause`},
		},
		{
			name: "accepts a marker with a reason",
			src:  "package x\n\n// lint:allow-log-pii reason=\"audit trail\"\nfunc f() {}\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result, err := checkSource("x.go", []byte(tt.src))
			require.NoError(t, err, "checkSource should parse the fixture")
			require.Equal(t, tt.expected, result.markerErrors, "checkSource should report malformed markers")
		})
	}
}

func TestCompareToBaseline(t *testing.T) {
	t.Parallel()

	email := finding{File: "a.go", Line: 10, Func: "f", Category: categoryEmail, Subject: `field "email"`}
	movedEmail := finding{File: "a.go", Line: 42, Func: "f", Category: categoryEmail, Subject: `field "email"`}
	ip := finding{File: "b.go", Line: 3, Func: "g", Category: categoryIPAddress, Subject: `field "ip"`}

	tests := []struct {
		name            string
		findings        []finding
		baseline        map[string]int
		expectedMatched []finding
		expectedNew     []finding
		expectedStale   []string
	}{
		{
			name:            "matches by file, function, and subject regardless of line",
			findings:        []finding{movedEmail},
			baseline:        map[string]int{email.baselineKey(): 1},
			expectedMatched: []finding{movedEmail},
		},
		{
			name:            "reports a second occurrence beyond the baselined count",
			findings:        []finding{email, movedEmail},
			baseline:        map[string]int{email.baselineKey(): 1},
			expectedMatched: []finding{email},
			expectedNew:     []finding{movedEmail},
		},
		{
			name:          "reports baseline entries that no longer match",
			baseline:      map[string]int{ip.baselineKey(): 1},
			expectedStale: []string{`b.go  g  field "ip"`},
		},
		{
			name:        "reports everything when there is no baseline",
			findings:    []finding{email, ip},
			baseline:    map[string]int{},
			expectedNew: []finding{email, ip},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			matched, newFindings, stale := compareToBaseline(tt.findings, tt.baseline)
			require.Equal(t, tt.expectedMatched, matched, "compareToBaseline should return the baselined findings")
			require.Equal(t, tt.expectedNew, newFindings, "compareToBaseline should return findings missing from the baseline")
			require.Equal(t, tt.expectedStale, stale, "compareToBaseline should return unmatched baseline entries")
		})
	}
}

func TestRun(t *testing.T) {
	t.Parallel()

	const emailLog = "package x\n\nfunc Invite() {\n\tlogger.Info().Str(\"email\", e).Msg(\"invited\")\n}\n"
	const cleanLog = "package x\n\nfunc Invite() {\n\tlogger.Info().Str(\"user_id\", id).Msg(\"invited\")\n}\n"
	emailEntry := "internal/x/x.go\tInvite\tfield \"email\"\n"
	staleEntry := "internal/x/gone.go\tOld\tfield \"ip\"\n"

	tests := []struct {
		name             string
		source           string
		baseline         string
		prune            bool
		expectedCode     int
		expectedStdout   string
		expectedStderr   string
		expectedBaseline string
	}{
		{
			name:           "passes clean code",
			source:         cleanLog,
			baseline:       baselineHeader,
			expectedCode:   0,
			expectedStdout: "lint-logs: OK — scanned 1 file(s); 0 pre-existing finding(s) remain in baseline.txt\n",
		},
		{
			name:           "passes a baselined finding",
			source:         emailLog,
			baseline:       baselineHeader + emailEntry,
			expectedCode:   0,
			expectedStdout: "lint-logs: OK — scanned 1 file(s); 1 pre-existing finding(s) remain in baseline.txt\n",
		},
		{
			name:         "fails a new finding without suggesting the baseline",
			source:       emailLog,
			baseline:     baselineHeader,
			expectedCode: 1,
			expectedStderr: "lint-logs: log statements that write personal data\n" +
				"  internal/x/x.go:4: Invite logs an email address (field \"email\")\n\n" +
				"Logs are retained, shipped off-host, and searchable, so they must not carry\n" +
				"personal data. Log the stable ID instead (user_id, slack_user_id), or drop the\n" +
				"field. If the value is genuinely required, add a comment above the statement\n" +
				"(the reason clause is required):\n" +
				"  // lint:allow-log-pii reason=\"...\"\n",
		},
		{
			name:         "fails a stale baseline entry",
			source:       emailLog,
			baseline:     baselineHeader + emailEntry + staleEntry,
			expectedCode: 1,
			expectedStderr: "lint-logs: baseline.txt lists findings that no longer exist\n" +
				"  internal/x/gone.go  Old  field \"ip\"\n" +
				"Remove them with: go run ./cmd/lint-logs -prune-baseline\n",
		},
		{
			name:         "prunes stale entries and keeps matched ones",
			source:       emailLog,
			baseline:     baselineHeader + emailEntry + staleEntry,
			prune:        true,
			expectedCode: 0,
			expectedStdout: "lint-logs: removed 1 stale baseline entry from baseline.txt\n" +
				"lint-logs: OK — scanned 1 file(s); 1 pre-existing finding(s) remain in baseline.txt\n",
			expectedBaseline: baselineHeader + emailEntry,
		},
		{
			name:           "pruning never adds new findings to the baseline",
			source:         emailLog,
			baseline:       baselineHeader + staleEntry,
			prune:          true,
			expectedCode:   1,
			expectedStdout: "lint-logs: removed 1 stale baseline entry from baseline.txt\n",
			expectedStderr: "lint-logs: log statements that write personal data\n" +
				"  internal/x/x.go:4: Invite logs an email address (field \"email\")\n\n" +
				"Logs are retained, shipped off-host, and searchable, so they must not carry\n" +
				"personal data. Log the stable ID instead (user_id, slack_user_id), or drop the\n" +
				"field. If the value is genuinely required, add a comment above the statement\n" +
				"(the reason clause is required):\n" +
				"  // lint:allow-log-pii reason=\"...\"\n",
			expectedBaseline: baselineHeader,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			writeFile(t, filepath.Join(root, "internal", "x", "x.go"), tt.source)
			writeFile(t, filepath.Join(root, "baseline.txt"), tt.baseline)

			var stdout, stderr bytes.Buffer
			code := run(root, "baseline.txt", tt.prune, &stdout, &stderr)

			require.Equal(t, tt.expectedCode, code, "run should return the expected exit code")
			require.Equal(t, tt.expectedStdout, stdout.String(), "run should print the expected stdout")
			require.Equal(t, tt.expectedStderr, stderr.String(), "run should print the expected stderr")
			if tt.expectedBaseline != "" {
				got, err := os.ReadFile(filepath.Join(root, "baseline.txt"))
				require.NoError(t, err, "baseline should remain readable")
				require.Equal(t, tt.expectedBaseline, string(got), "pruning should keep only entries that still match")
			}
		})
	}
}

func TestGoSourceFiles(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	for _, rel := range []string{
		"cmd/tool/main.go",
		"internal/a/a.go",
		"internal/a/a_test.go",
		"internal/a/testdata/fixture.go",
		"frontend/node_modules/pkg/x.go",
		".claude/worktrees/copy/internal/a/a.go",
		"internal/a/README.md",
	} {
		writeFile(t, filepath.Join(root, rel), "package x\n")
	}

	files, err := goSourceFiles(root)
	require.NoError(t, err, "goSourceFiles should walk the tree")
	require.Equal(t, []string{"cmd/tool/main.go", "internal/a/a.go"}, files, "goSourceFiles should return only non-test Go sources outside skipped directories")
}

func logFinding(line int, cat category, subject string) finding {
	return finding{File: "x.go", Line: line, Func: "f", Category: cat, Subject: subject}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o750), "fixture directory should be created")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600), "fixture file should be written")
}
