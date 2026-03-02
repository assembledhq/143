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
