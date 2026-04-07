package github

import (
	"testing"
)

func TestParseCodeowners(t *testing.T) {
	content := `# Global owners
* @global-owner1 @global-owner2

# Frontend
/frontend/ @frontend-team
*.tsx @frontend-team

# Backend
/internal/ @backend-team
*.go @backend-team @go-reviewers

# Docs
docs/ @docs-team
`
	rules := ParseCodeowners(content)
	if len(rules) != 6 {
		t.Fatalf("expected 6 rules, got %d", len(rules))
	}
	if rules[0].Pattern != "*" || len(rules[0].Owners) != 2 {
		t.Errorf("rule 0: expected * with 2 owners, got %q with %d owners", rules[0].Pattern, len(rules[0].Owners))
	}
	if rules[0].Owners[0] != "global-owner1" {
		t.Errorf("rule 0 owner 0: expected global-owner1, got %s", rules[0].Owners[0])
	}
}

func TestMatchOwners(t *testing.T) {
	rules := ParseCodeowners(`
* @fallback
/internal/ @backend
*.go @go-team
/frontend/ @frontend
*.tsx @react-team
docs/ @docs
`)

	tests := []struct {
		path    string
		want    []string
	}{
		{"internal/services/github/pr.go", []string{"go-team"}},
		{"frontend/src/app/page.tsx", []string{"react-team"}},
		{"README.md", []string{"fallback"}},
		{"docs/design/foo.md", []string{"docs"}},
	}

	for _, tt := range tests {
		got := MatchOwners(rules, tt.path)
		if len(got) != len(tt.want) {
			t.Errorf("MatchOwners(%q): got %v, want %v", tt.path, got, tt.want)
			continue
		}
		for i := range got {
			if got[i] != tt.want[i] {
				t.Errorf("MatchOwners(%q)[%d]: got %s, want %s", tt.path, i, got[i], tt.want[i])
			}
		}
	}
}

func TestResolveReviewers(t *testing.T) {
	rules := ParseCodeowners(`
* @fallback
/internal/ @backend
*.go @go-team
/frontend/ @frontend
*.tsx @react-team
`)

	// Mixed changes: 3 Go files, 1 TSX file
	files := []string{
		"internal/services/github/pr.go",
		"internal/models/project.go",
		"internal/db/projects.go",
		"frontend/src/app/page.tsx",
	}

	reviewers := ResolveReviewers(rules, files)

	// go-team should be first (matches 3 Go files)
	if len(reviewers) == 0 {
		t.Fatal("expected reviewers, got none")
	}
	if reviewers[0] != "go-team" {
		t.Errorf("expected first reviewer to be go-team, got %s", reviewers[0])
	}
}

func TestResolveReviewers_Empty(t *testing.T) {
	reviewers := ResolveReviewers(nil, []string{"foo.go"})
	if len(reviewers) != 0 {
		t.Errorf("expected no reviewers for nil rules, got %v", reviewers)
	}

	rules := ParseCodeowners("* @owner")
	reviewers = ResolveReviewers(rules, nil)
	if len(reviewers) != 0 {
		t.Errorf("expected no reviewers for nil files, got %v", reviewers)
	}
}

func TestMatchPattern_TeamSlug(t *testing.T) {
	rules := ParseCodeowners("*.go @myorg/backend-team")
	if len(rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(rules))
	}
	if rules[0].Owners[0] != "myorg/backend-team" {
		t.Errorf("expected myorg/backend-team, got %s", rules[0].Owners[0])
	}
}
