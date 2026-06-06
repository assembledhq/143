package mcp

import (
	"fmt"
	"sort"
	"strings"
)

// GenerateSkillsDoc produces a concise markdown skills document from the
// tool registry. This is injected into the agent's system prompt so the
// model knows what CLI tools are available and how to use them.
//
// The document is intentionally compact (~200-800 tokens depending on
// integrations) to maximize context window available for reasoning.
func GenerateSkillsDoc(tr *ToolRegistry) string {
	tools := tr.ListTools()
	if len(tools) == 0 {
		return ""
	}

	var b strings.Builder

	b.WriteString("# Integration Tools\n\n")
	b.WriteString("You have access to `143-tools`, a CLI for querying and managing external integrations.\n")
	b.WriteString("Use these tools to look up errors, tasks, documents, and messages from connected services.\n\n")
	b.WriteString("## Quick Reference\n\n")
	b.WriteString("```\n")
	b.WriteString("143-tools <tool_name> [--flag value ...]\n")
	b.WriteString("143-tools <tool_name> --help        # detailed usage\n")
	b.WriteString("143-tools --help                     # list all tools\n")
	b.WriteString("```\n\n")
	if hasToolPrefix(tools, "session_tabs_") {
		b.WriteString("When using session tab tools:\n")
		b.WriteString("- Use a new tab for parallel review/testing/investigation in the same branch.\n")
		b.WriteString("- Use a new session only when work needs an independent branch or PR.\n")
		b.WriteString("- Summarize why a tab was created in the message sent to it.\n")
		b.WriteString("- Inspect sibling output before assuming the other tab has finished.\n\n")
	}

	// Group by provider and generate examples.
	groups := groupToolsByProvider(tools)
	providerNames := make([]string, 0, len(groups))
	for name := range groups {
		providerNames = append(providerNames, name)
	}
	sort.Strings(providerNames)

	for _, provider := range providerNames {
		providerTools := groups[provider]
		title := provider
		if len(title) > 0 {
			title = strings.ToUpper(title[:1]) + title[1:]
		}
		b.WriteString(fmt.Sprintf("## %s\n\n", title))

		for _, tool := range providerTools {
			b.WriteString(fmt.Sprintf("**%s** — %s\n", tool.Name, tool.Description))

			// Generate an example command with typical flags.
			example := generateExample(tool)
			if example != "" {
				b.WriteString(fmt.Sprintf("```\n%s\n```\n", example))
			}
			b.WriteString("\n")
		}
	}

	// Add composability tips.
	b.WriteString("## Tips\n\n")
	b.WriteString(fmt.Sprintf("- Output is JSON. Pipe through `jq` for filtering: `%s`\n", exampleJQTip(tools)))
	b.WriteString("- Combine tools: find an error, then look up related tasks.\n")
	b.WriteString("- Use `--limit` to control result size and keep output manageable.\n")
	for _, tool := range tools {
		if strings.HasPrefix(tool.Name, "log_") {
			b.WriteString("- Log queries require a time bound: always provide `--since` or `--start_time`/`--end_time`.\n")
			break
		}
	}

	return b.String()
}

// generateExample creates a representative CLI invocation for a tool.
func generateExample(tool Tool) string {
	var parts []string
	parts = append(parts, "143-tools", tool.Name)

	// Add required flags with placeholder values.
	requiredSet := make(map[string]bool)
	for _, r := range tool.InputSchema.Required {
		requiredSet[r] = true
	}

	// Sort properties for stable output.
	propNames := make([]string, 0, len(tool.InputSchema.Properties))
	for name := range tool.InputSchema.Properties {
		propNames = append(propNames, name)
	}
	sort.Strings(propNames)

	for _, name := range propNames {
		prop := tool.InputSchema.Properties[name]

		if requiredSet[name] {
			parts = append(parts, "--"+displayCLIFlagName(name), exampleValue(name, prop))
		} else if isHighValueFlag(name) {
			// Include commonly useful optional flags in examples.
			parts = append(parts, "--"+displayCLIFlagName(name), exampleValue(name, prop))
		}
	}

	return strings.Join(parts, " ")
}

// exampleValue returns a representative placeholder value for a schema property.
func exampleValue(name string, prop SchemaProperty) string {
	if len(prop.Enum) > 0 {
		return prop.Enum[0]
	}
	switch prop.Type {
	case "number":
		if prop.Default != nil {
			return fmt.Sprintf("%v", prop.Default)
		}
		return "10"
	case "array":
		return "value1,value2"
	default:
		// Generate contextual placeholders.
		switch name {
		case "error_id":
			return "12345"
		case "task_id":
			return "ENG-123"
		case "doc_id":
			return "abc-123"
		case "message_id":
			return "msg-456"
		case "query":
			return "\"search terms\""
		case "title":
			return "\"Task title\""
		case "team_key":
			return "ENG"
		case "description":
			return "\"Description here\""
		default:
			return "\"value\""
		}
	}
}

// isHighValueFlag returns true for optional flags worth including in examples.
func isHighValueFlag(name string) bool {
	switch name {
	case "severity", "limit", "priority", "team", "message_file":
		return true
	}
	return false
}

func exampleJQTip(tools []Tool) string {
	for _, tool := range tools {
		if strings.Contains(tool.Name, "list_") || strings.Contains(tool.Name, "search_") {
			return fmt.Sprintf("143-tools %s | jq '.[].title // .[].id'", tool.Name)
		}
	}
	return "143-tools <tool_name> | jq '.'"
}

func hasToolPrefix(tools []Tool, prefix string) bool {
	for _, tool := range tools {
		if strings.HasPrefix(tool.Name, prefix) {
			return true
		}
	}
	return false
}
