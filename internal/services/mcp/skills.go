package mcp

import (
	"fmt"
	"strings"
)

// GenerateSkillsDoc produces a compact markdown skills document from the tool
// registry. It is injected into the agent's prompt, so it intentionally teaches
// discovery rather than listing every command schema.
func GenerateSkillsDoc(tr *ToolRegistry) string {
	commands := buildCLICommands(tr.ListTools())
	if len(commands) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("# Integration Tools\n\n")
	b.WriteString("You have access to `143-tools`, a CLI for querying and managing external integrations.\n")
	b.WriteString("Use these tools to look up errors, tasks, documents, logs, messages, and 143 workflow actions.\n\n")

	b.WriteString("## Quick Reference\n\n")
	b.WriteString("```bash\n")
	b.WriteString("143-tools <namespace> <action> [--flag value ...]\n")
	b.WriteString("143-tools <namespace> --help        # list actions in a namespace\n")
	b.WriteString("143-tools <namespace> <action> --help # detailed usage\n")
	b.WriteString("143-tools --help                     # list namespaces\n")
	b.WriteString("```\n\n")

	b.WriteString("## Configured Namespaces\n\n")
	for _, namespace := range cliNamespaceOrder(commands) {
		nsCommands := commandsForNamespace(commands, namespace)
		if len(nsCommands) == 0 {
			continue
		}
		actions := make([]string, 0, len(nsCommands))
		for _, cmd := range nsCommands {
			actions = append(actions, string(cmd.Action))
		}
		b.WriteString(fmt.Sprintf("- `%s`: %s (`%s`)\n", namespace, nsCommands[0].Category, strings.Join(actions, "`, `")))
	}

	examples := skillsExamples(commands)
	if len(examples) > 0 {
		b.WriteString("\n## Examples\n\n")
		b.WriteString("```bash\n")
		for _, example := range examples {
			b.WriteString(example)
			b.WriteString("\n")
		}
		b.WriteString("```\n")
	}

	b.WriteString("\n## Tips\n\n")
	b.WriteString("- Output is JSON. Pipe through `jq` when scanning result sets.\n")
	b.WriteString("- Run `143-tools <namespace> --help` before using unfamiliar commands.\n")
	b.WriteString("- Use `--limit` to keep result size manageable.\n")
	if hasCommand(commands, NamespaceLogs, "query") {
		b.WriteString("- Log queries require a time bound: provide `--since` or `--start_time`/`--end_time`.\n")
	}

	return b.String()
}

func skillsExamples(commands []CLICommand) []string {
	candidates := []struct {
		namespace CLINamespace
		action    CLIAction
		example   string
	}{
		{"sentry", "list_errors", "143-tools sentry list_errors --severity critical --limit 20"},
		{"linear", "get_task", "143-tools linear get_task --task_id ENG-123"},
		{NamespaceLogs, "query", "143-tools logs query --provider victorialogs --query 'service:api AND level:error' --since 1h --limit 100"},
		{"notion", "search_documents", "143-tools notion search_documents --query \"webhook retry policy\" --limit 10"},
		{"github", "list_recent_prs", "143-tools github list_recent_prs --state merged --limit 20"},
		{"circleci", "list_flaky_tests", "143-tools circleci list_flaky_tests --limit 25"},
		{"slack", "search_messages", "143-tools slack search_messages --query \"checkout timeout\" --limit 10"},
		{NamespacePR, ActionCreate, "143-tools pr create --draft false"},
	}

	examples := make([]string, 0, 5)
	for _, candidate := range candidates {
		if hasCommand(commands, candidate.namespace, candidate.action) {
			examples = append(examples, candidate.example)
		}
		if len(examples) == 5 {
			break
		}
	}
	if len(examples) > 0 {
		return examples
	}
	for _, cmd := range commands {
		examples = append(examples, cmd.Usage())
		if len(examples) == 2 {
			break
		}
	}
	return examples
}

func hasCommand(commands []CLICommand, namespace CLINamespace, action CLIAction) bool {
	_, ok := findCLICommand(commands, namespace, action)
	return ok
}
