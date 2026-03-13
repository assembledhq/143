package pm

import (
	"context"
	"fmt"
	"strings"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agent"
)

func (s *Service) writeProductContextToAgentsMD(ctx context.Context, sb *agent.Sandbox, pc *models.ProductContext) error {
	if pc == nil {
		return nil
	}

	existing, err := s.sandbox.ReadFile(ctx, sb, "/workspace/AGENTS.md")
	if err != nil {
		existing = nil
	}

	section := fmt.Sprintf(`

## Product Context

**Philosophy:** %s

**Current direction:** %s

**Focus areas:** %s

**Avoid areas:** %s
`, pc.Philosophy, pc.Direction, strings.Join(pc.FocusAreas, ", "), strings.Join(pc.AvoidAreas, ", "))

	return s.sandbox.WriteFile(ctx, sb, "/workspace/AGENTS.md", append(existing, []byte(section)...))
}

// writePMDocumentsToWorkspace writes org-level PM documents (roadmap, philosophy,
// etc.) into /workspace/.pm-documents/ so the PM agent can read them.
func (s *Service) writePMDocumentsToWorkspace(ctx context.Context, sb *agent.Sandbox, docs []models.PMDocument) error {
	if len(docs) == 0 {
		return nil
	}

	// Write a manifest for easy discovery.
	var manifest strings.Builder
	manifest.WriteString("# PM Reference Documents\n\n")
	manifest.WriteString("The following documents provide product context, roadmap, and strategic information.\n\n")

	for i, doc := range docs {
		filename := fmt.Sprintf("%02d-%s.md", i+1, sanitizeFilename(doc.Title))
		filepath := fmt.Sprintf("/workspace/.pm-documents/%s", filename)

		header := fmt.Sprintf("# %s\n\n> Type: %s\n\n", doc.Title, doc.DocType)
		content := header + doc.Content

		if err := s.sandbox.WriteFile(ctx, sb, filepath, []byte(content)); err != nil {
			return fmt.Errorf("write PM document %q: %w", doc.Title, err)
		}

		manifest.WriteString(fmt.Sprintf("- **%s** (`%s`) — %s\n", doc.Title, filename, doc.DocType))
	}

	return s.sandbox.WriteFile(ctx, sb, "/workspace/.pm-documents/README.md", []byte(manifest.String()))
}

// sanitizeFilename converts a title into a safe filename slug.
func sanitizeFilename(title string) string {
	title = strings.ToLower(title)
	title = strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			return r
		}
		if r == ' ' {
			return '-'
		}
		return -1
	}, title)
	if len(title) > 60 {
		title = title[:60]
	}
	return title
}
