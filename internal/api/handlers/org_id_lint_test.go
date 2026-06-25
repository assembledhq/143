package handlers

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestHandlersMustUseOrgIDFromContext ensures every HTTP handler method on a
// handler struct calls middleware.OrgIDFromContext (or is explicitly exempted).
//
// This prevents regressions where new endpoints accidentally skip org-scoping
// and allows cross-org data access.
//
// If you add a new handler that is legitimately exempt (e.g. public routes,
// webhooks, or methods that access no org-scoped data), add it to the
// allowlist below with a comment explaining why.
func TestHandlersMustUseOrgIDFromContext(t *testing.T) {
	t.Parallel()

	// Methods exempt from requiring OrgIDFromContext, keyed as "TypeName.MethodName".
	// Each entry has a comment explaining why the exemption exists.
	allowlist := map[string]string{
		// Public routes — no auth middleware, no org context.
		"HealthHandler.Healthz":        "public health check",
		"HealthHandler.Readyz":         "public health check",
		"HealthHandler.Version":        "public version endpoint",
		"AuthHandler.Providers":        "public, pre-auth",
		"AuthHandler.Login":            "public, pre-auth (GitHub OAuth start)",
		"AuthHandler.Callback":         "public, pre-auth (GitHub OAuth callback)",
		"AuthHandler.GoogleLogin":      "public, pre-auth (Google OAuth start)",
		"AuthHandler.GoogleCallback":   "public, pre-auth (Google OAuth callback)",
		"AuthHandler.Register":         "public, pre-auth",
		"AuthHandler.EmailLogin":       "public, pre-auth",
		"TeamHandler.AcceptInvitation": "public, token-based (no auth middleware)",
		"AuthHandler.CLIStart":         "public, pre-auth (CLI browser-login start; chains into GitHub OAuth)",
		"AuthHandler.CLIExchange":      "public, pre-auth (one-time code + verifier exchange; org comes from the code row)",

		// CLI distribution — public installer/binary routes, no auth.
		"CLIDistributionHandler.InstallScript":  "public installer script",
		"CLIDistributionHandler.DownloadBinary": "public binary download",
		"CLIDistributionHandler.Checksums":      "public checksum file",
		"CLIDistributionHandler.Version":        "public version endpoint",

		// Webhook routes — signature-verified, not session-authenticated.
		"WebhookHandler.HandleGitHub":                      "external webhook, signature auth",
		"IngestionWebhookHandler.HandleSentry":             "external webhook, signature auth",
		"IngestionWebhookHandler.HandleLinear":             "external webhook, signature auth",
		"PagerDutyWebhookHandler.Handle":                   "external webhook, signature/shared-secret auth; org resolves from integration_id",
		"PagerDutyWebhookHandler.HandleStartSessionAction": "external PagerDuty custom incident action, signature/shared-secret auth; org resolves from integration_id",

		// OAuth routes — org is encoded in signed OAuth state rather than request org context.
		"PagerDutyIntegrationHandler.StartOAuth": "PagerDuty OAuth start; org comes from signed OAuth state and session context",

		// Internal API routes — use claims.OrgID from internal JWT, not middleware.
		"InternalIssueHandler.Create":                       "internal API, uses claims.OrgID",
		"InternalEvalHandler.AddCandidate":                  "internal sandbox API, uses claims.OrgID and requires eval_bootstrap session origin",
		"InternalAutomationGoalImprovementHandler.Complete": "internal sandbox API, uses claims.OrgID and requires automation_goal_improvement session origin",
		"InternalPullRequestHandler.Create":                 "internal API, uses claims.OrgID",
		"InternalProjectHandler.Propose":                    "internal API, uses claims.OrgID",
		"InternalSessionTabsHandler.List":                   "internal sandbox API, uses claims.OrgID and claims.SessionID",
		"InternalSessionTabsHandler.Get":                    "internal sandbox API, uses claims.OrgID and claims.SessionID",
		"InternalSessionTabsHandler.Create":                 "internal sandbox API, uses claims.OrgID and claims.SessionID",
		"InternalSessionTabsHandler.SendMessage":            "internal sandbox API, uses claims.OrgID and claims.SessionID",
		"InternalSessionTabsHandler.Messages":               "internal sandbox API, uses claims.OrgID and claims.SessionID",
		"InternalPreviewHandler.AuthCheck":                  "internal worker deploy compatibility probe; validates signed target-node token and reads no org-scoped data",
		"InternalSandboxAuthHandler.Acquire":                "internal worker RPC, uses signed token claims.OrgID plus claims.SessionID and holder id",
		"InternalSandboxAuthHandler.Release":                "internal worker RPC, uses signed token claims.OrgID plus claims.SessionID and holder id",
		"InternalAgentCapabilitiesHandler.Effective":        "internal API, uses signed tool token claims.OrgID and claims.SessionID",
		"InternalAgentCapabilitiesHandler.Request":          "internal API, uses signed tool token claims.OrgID and claims.SessionID",
		"InternalSessionHistoryHandler.Search":              "internal API, uses signed tool token claims.OrgID and claims.SessionID",
		"InternalSessionHistoryHandler.Get":                 "internal API, uses signed tool token claims.OrgID and claims.SessionID",
		"InternalSessionHistoryHandler.Messages":            "internal API, uses signed tool token claims.OrgID and claims.SessionID",
		"InternalAutomationHandler.Create":                  "internal sandbox API, uses signed tool token claims.OrgID and restricts repository_id to claims.RepoID",
		"InternalAutomationHandler.Update":                  "internal sandbox API, uses signed tool token claims.OrgID and restricts automation repository_id to claims.RepoID",
		"InternalAutomationHandler.RunNow":                  "internal sandbox API, uses signed tool token claims.OrgID and restricts automation repository_id to claims.RepoID",
		"InternalAutomationHandler.Pause":                   "internal sandbox API, uses signed tool token claims.OrgID and restricts automation repository_id to claims.RepoID",
		"InternalAutomationHandler.Resume":                  "internal sandbox API, uses signed tool token claims.OrgID and restricts automation repository_id to claims.RepoID",

		// Authenticated but legitimately no org-scoped data access.
		"AuthHandler.Me":                       "returns user from context only",
		"AuthHandler.Logout":                   "deletes session by cookie token only",
		"AuthHandler.SetActiveOrg":             "user-scoped preference write; validates membership directly instead of using request org context",
		"AuthHandler.ClaimInvitation":          "grants membership in a different org than the active one; target org comes from the invitation token, not the request context",
		"AuthHandler.ListPendingInvitations":   "user-scoped query spanning all orgs the user is invited to",
		"AuthHandler.ListCLITokens":            "user-scoped: a user's CLI device tokens span orgs, like auth_sessions",
		"AuthHandler.RevokeCLIToken":           "user-scoped self-service revocation; ownership enforced by user_id in the store query",
		"AuthHandler.AcceptInvitationByID":     "invitee-scoped accept; org context comes from the invitation row itself, not the request",
		"AuthHandler.DeclineInvitationByID":    "invitee-scoped decline; org context comes from the invitation row itself, not the request",
		"OrgDomainsHandler.ListJoinable":       "user-scoped cross-org discovery; must work for zero-membership users outside OrgContext",
		"AuthHandler.SendEmailVerification":    "user-scoped identity action; sends to the session user's own address, no org data touched",
		"AuthHandler.ConfirmEmailVerification": "public token-claim route; user and (optional) auto-join org come from the token, not request context",
		"OrgDomainsHandler.Join":               "grants membership in a different org than the active one; target org comes from the URL and is re-validated against the session's verified email domain",
		"OrganizationsHandler.Create":          "creates a new org; runs outside OrgContext, no pre-existing org to scope against",
		"SettingsHandler.GetLLMDefaults":       "returns static server config",
		"SettingsHandler.GetLLMModels":         "returns static server config",
		"AgentCapabilitiesHandler.Catalog":     "returns static capability catalog",
		"ProjectGenerateHandler.Generate":      "calls LLM only, no org-scoped data",
		"GitHubStatusHandler.StartConnect":     "OAuth redirect only, no store calls",

		// OAuth start handlers — just redirect to external provider, no org data access.
		"IntegrationHandler.StartLinearOAuth":  "OAuth redirect only",
		"IntegrationHandler.StartSentryOAuth":  "OAuth redirect only",
		"IntegrationHandler.StartGitHubOAuth":  "OAuth redirect only",
		"IntegrationHandler.StartSlackOAuth":   "OAuth redirect only",
		"IntegrationHandler.ReinstallSlackBot": "delegates to Slack OAuth redirect only",
		"SlackbotHandler.Events":               "public Slack callback resolves org from verified Slack team/app installation",
		"SlackbotHandler.Commands":             "public Slack callback resolves org from verified Slack team/app installation",
		"SlackbotHandler.Interactions":         "public Slack callback resolves org from verified Slack team/app installation",
		"InternalSlackMessageHandler.Send":     "internal sandbox route resolves org from the signed session-scoped internal token",

		// Thin wrappers that delegate to a helper which calls OrgIDFromContext.
		"PMHandler.Bootstrap":                      "delegates to enqueueAndRespond which uses OrgIDFromContext",
		"PMHandler.Refresh":                        "delegates to enqueueAndRespond which uses OrgIDFromContext",
		"ProjectHandler.Start":                     "delegates to transitionStatus which uses OrgIDFromContext",
		"RepositoryHandler.Disconnect":             "delegates to setRepoStatus which uses OrgIDFromContext",
		"RepositoryHandler.Reconnect":              "delegates to setRepoStatus which uses OrgIDFromContext",
		"SessionFileHandler.ListFiles":             "delegates to getSessionContainer which uses OrgIDFromContext",
		"SessionFileHandler.GetFileContent":        "delegates to getSessionContainer which uses OrgIDFromContext",
		"SessionFileHandler.GetFileContext":        "delegates to getSessionContainer which uses OrgIDFromContext",
		"SessionComposerHandler.ListSlashCommands": "built-in catalog is not org-scoped; project discovery branch delegates to buildProjectSlashCommandGroup which uses OrgIDFromContext",
		"SessionHandler.StreamLogs":                "EventSource cannot send X-Active-Org-ID; delegates to streamOrgID which calls OrgIDFromContext and additionally honours a membership-validated ?org_id= query fallback",
		"parseReviewCommentIDsError.write":         "internal error-rendering helper; org scoping happens upstream in SendMessage where the error was produced",
		"UploadHandler.ServeUpload":                "<img>/<a> tag fetches cannot send X-Active-Org-ID; authorizes off the path-encoded org-id + UserFromContext membership check rather than active-org context",
		"UsageHandler.ExportCSV":                   "window.open downloads cannot send X-Active-Org-ID; delegates to exportOrgID which calls OrgIDFromContext and additionally honours a membership-validated ?org_id= query fallback",

		// Preview inspector handlers — delegate to requireInspector + getActivePreview
		// which use OrgIDFromContext.
		"PreviewHandler.DetectReadiness":      "config-only check, no org-scoped data access",
		"PreviewHandler.CaptureScreenshot":    "delegates to getActivePreview which uses OrgIDFromContext",
		"PreviewHandler.InspectElement":       "delegates to getActivePreview which uses OrgIDFromContext",
		"PreviewHandler.ReadConsole":          "delegates to getActivePreview which uses OrgIDFromContext",
		"PreviewHandler.SubmitDesignFeedback": "delegates to getActivePreview which uses OrgIDFromContext; also calls OrgIDFromContext directly for log entry",
		"PreviewHandler.ExecuteInteraction":   "delegates to getActivePreview which uses OrgIDFromContext",
		"PreviewHandler.CaptureMultiViewport": "delegates to getActivePreview which uses OrgIDFromContext",
		"PreviewHandler.ComputeVisualDiff":    "delegates to getActivePreview which uses OrgIDFromContext",
		"PreviewHandler.RunAssertions":        "delegates to getActivePreview which uses OrgIDFromContext",

		// Linear agent settings — every handler delegates to requireOrgID,
		// which calls OrgIDFromContext and additionally rejects uuid.Nil
		// with a 401 (defense in depth against an upstream middleware
		// regression). The helper centralizes the check so the same
		// shape applies to every endpoint.
		"LinearAgentSettingsHandler.GetStatus":     "delegates to requireOrgID which uses OrgIDFromContext",
		"LinearAgentSettingsHandler.ListMappings":  "delegates to requireOrgID which uses OrgIDFromContext",
		"LinearAgentSettingsHandler.UpsertMapping": "delegates to requireOrgID which uses OrgIDFromContext",
		"LinearAgentSettingsHandler.PatchSettings": "delegates to requireOrgID which uses OrgIDFromContext",
		"LinearAgentSettingsHandler.ListSessions":  "delegates to requireOrgID which uses OrgIDFromContext",
		"LinearAgentSettingsHandler.GetSession":    "delegates to requireOrgID which uses OrgIDFromContext",
		"LinearAgentSettingsHandler.DeleteMapping": "delegates to requireOrgID which uses OrgIDFromContext",

		// Session creation thin wrappers — both delegate to createManual which calls OrgIDFromContext.
		"SessionHandler.CreateManual":   "delegates to createManual which uses OrgIDFromContext",
		"SessionHandler.CreateExternal": "delegates to createManual which uses OrgIDFromContext",

		// Automation creation thin wrappers — both delegate to Create or CreateExternal
		// which calls OrgIDFromContext.
		"AutomationHandler.CreatePublic":   "delegates to Create or CreateExternal which use OrgIDFromContext",
		"AutomationHandler.CreateExternal": "delegates to Create which uses OrgIDFromContext",
	}

	fset := token.NewFileSet()

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("failed to read handler directory: %v", err)
	}

	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}

		path := filepath.Join(".", name)
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("failed to parse %s: %v", name, err)
		}

		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || fn.Type.Results != nil {
				continue
			}

			// Check this is an HTTP handler: (http.ResponseWriter, *http.Request)
			params := fn.Type.Params.List
			if !isHTTPHandlerSignature(params) {
				continue
			}

			// Get receiver type name.
			recvType := receiverTypeName(fn.Recv.List[0].Type)
			if recvType == "" {
				continue
			}

			key := recvType + "." + fn.Name.Name

			if _, exempt := allowlist[key]; exempt {
				continue
			}

			// Check that the method body contains a call to OrgIDFromContext.
			if !containsOrgIDFromContext(fn.Body) {
				t.Errorf("%s:%d: %s does not call middleware.OrgIDFromContext — "+
					"add the call or add to the allowlist in org_id_lint_test.go with justification",
					name, fset.Position(fn.Pos()).Line, key)
			}
		}
	}
}

// isHTTPHandlerSignature checks that params match (http.ResponseWriter, *http.Request).
func isHTTPHandlerSignature(params []*ast.Field) bool {
	if len(params) != 2 {
		return false
	}

	// First param: http.ResponseWriter (a selector expression)
	sel1, ok := params[0].Type.(*ast.SelectorExpr)
	if !ok || sel1.Sel.Name != "ResponseWriter" {
		return false
	}

	// Second param: *http.Request (a pointer to selector expression)
	star, ok := params[1].Type.(*ast.StarExpr)
	if !ok {
		return false
	}
	sel2, ok := star.X.(*ast.SelectorExpr)
	if !ok || sel2.Sel.Name != "Request" {
		return false
	}

	return true
}

// receiverTypeName extracts the type name from a receiver (handles *T and T).
func receiverTypeName(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.StarExpr:
		if ident, ok := t.X.(*ast.Ident); ok {
			return ident.Name
		}
	case *ast.Ident:
		return t.Name
	}
	return ""
}

// containsOrgIDFromContext walks the AST looking for a call to OrgIDFromContext.
func containsOrgIDFromContext(node ast.Node) bool {
	if node == nil {
		return false
	}
	found := false
	ast.Inspect(node, func(n ast.Node) bool {
		if found {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		// Match: middleware.OrgIDFromContext(...) or just OrgIDFromContext(...)
		switch fn := call.Fun.(type) {
		case *ast.SelectorExpr:
			if fn.Sel.Name == "OrgIDFromContext" {
				found = true
				return false
			}
		case *ast.Ident:
			if fn.Name == "OrgIDFromContext" {
				found = true
				return false
			}
		}
		return true
	})
	return found
}
