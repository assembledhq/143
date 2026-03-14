package pm

import (
	"context"
	"encoding/json"
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

// writeSlackThreadFiles writes full Slack thread data to the sandbox so the PM can drill down.
func (s *Service) writeSlackThreadFiles(ctx context.Context, sb *agent.Sandbox, threads []slackThreadData) error {
	if len(threads) == 0 {
		return nil
	}

	readme := `# Slack Thread Files

This directory contains full Slack thread data for threads identified as actionable.
Each file is named {channel_name}-{thread_ts}.json and contains the raw messages.

Use these files to drill down into specific threads when the summary in
.pm-context.json is not enough to make a decision.
`
	if err := s.sandbox.WriteFile(ctx, sb, "/workspace/.slack-threads/README.md", []byte(readme)); err != nil {
		return fmt.Errorf("write slack threads README: %w", err)
	}

	for _, t := range threads {
		path := fmt.Sprintf("/workspace/.slack-threads/%s-%s.json", t.ChannelName, t.ThreadTS)
		data, err := json.MarshalIndent(map[string]any{
			"channel":  t.ChannelName,
			"thread":   t.ThreadTS,
			"messages": json.RawMessage(t.Messages),
		}, "", "  ")
		if err != nil {
			s.logger.Warn().Err(err).Str("thread", t.ThreadTS).Msg("failed to marshal slack thread")
			continue
		}
		if err := s.sandbox.WriteFile(ctx, sb, path, data); err != nil {
			s.logger.Warn().Err(err).Str("path", path).Msg("failed to write slack thread file")
			continue
		}
	}
	return nil
}
