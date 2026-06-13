package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
)

// CLINamespace is the first positional argument to 143-tools — a provider name
// or 143-owned category. Using a distinct type prevents accidental swaps with
// CLIAction and makes switch exhaustiveness checks meaningful.
type CLINamespace string

// CLIAction is the second positional argument to 143-tools — the operation to
// perform within the namespace. Using a distinct type prevents accidental swaps
// with CLINamespace.
type CLIAction string

// Fixed namespaces for 143-owned tools and the provider-agnostic logs category.
// Provider-derived namespaces such as "sentry" or "linear" are not declared as
// constants because they are derived from configured integrations at runtime.
const (
	NamespaceLogs    CLINamespace = "logs"
	NamespaceIssue   CLINamespace = "issue"
	NamespacePR      CLINamespace = "pr"
	NamespaceProject CLINamespace = "project"
	NamespaceTabs    CLINamespace = "session-tabs"
	NamespaceEval    CLINamespace = "eval"
)

// Fixed actions for the hardcoded 143-owned namespace mappings.
const (
	ActionCreate   CLIAction = "create"
	ActionGet      CLIAction = "get"
	ActionList     CLIAction = "list"
	ActionMessages CLIAction = "messages"
	ActionPropose  CLIAction = "propose"
	ActionSend     CLIAction = "send"
	ActionAdd      CLIAction = "add"
)

// RunCLI executes a tool call from command-line arguments, printing the result
// to stdout. This provides the same dispatch as the MCP server but via a
// simple CLI that agents already know how to use.
//
// Usage:
//
//	143-tools <namespace> <action> [--flag value ...]
//	143-tools sentry list_errors --severity high --limit 10
//	143-tools linear create_task --title "Fix bug" --team_key ENG
//	143-tools --help
//	143-tools <namespace> --help
//	143-tools <namespace> <action> --help
func RunCLI(ctx context.Context, tr ToolSource, args []string, stdout, stderr io.Writer) int {
	commands := buildCLICommands(tr.ListTools())
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		printCLIUsage(commands, stdout)
		return 0
	}

	// Convert raw CLI strings to typed values at the entry boundary.
	namespace := CLINamespace(args[0])
	if replacement, ok := replacementForOldFlatCommand(args[0], commands); ok {
		fmt.Fprintf(stderr, "error: %q is no longer supported. Use '%s [flags]'.\n\nRun '%s --help' for detailed usage.\n", namespace, replacement, replacement)
		return 1
	}

	if !hasNamespace(commands, namespace) {
		fmt.Fprintf(stderr, "error: unknown namespace %q.\n\nUsage: 143-tools <namespace> <action> [--flag value ...]\nRun '143-tools --help' to list available namespaces.\n", namespace)
		return 1
	}

	if len(args) == 2 && isHelpArg(args[1]) {
		printNamespaceHelp(commands, namespace, stdout)
		return 0
	}

	if len(args) < 2 {
		fmt.Fprintf(stderr, "error: missing action for namespace %q.\n\nUsage: 143-tools %s <action> [--flag value ...]\nRun '143-tools %s --help' to list available actions.\n", namespace, namespace, namespace)
		return 1
	}

	action := CLIAction(args[1])
	cmd, ok := findCLICommand(commands, namespace, action)
	if !ok {
		fmt.Fprintf(stderr, "error: unknown action %q for namespace %q.\n\nUsage: 143-tools %s <action> [--flag value ...]\nRun '143-tools %s --help' to list available actions.\n", action, namespace, namespace, namespace)
		return 1
	}

	flagArgs := args[2:]
	for _, a := range flagArgs {
		if isHelpArg(a) {
			printActionHelp(cmd, stdout)
			return 0
		}
	}

	// Parse flags into a JSON object.
	argsJSON, err := parseFlagsToJSON(flagArgs, cmd.Tool.InputSchema)
	if err != nil {
		writeCLIError(stderr, "INVALID_ARGUMENTS", fmt.Sprintf("%s. Usage: %s [flags]. Run '%s --help' for detailed usage.", err, cmd.Usage(), cmd.Usage()))
		return 1
	}

	// Check required fields.
	if err := checkRequired(argsJSON, cmd.Tool.InputSchema.Required); err != nil {
		writeCLIError(stderr, "INVALID_ARGUMENTS", fmt.Sprintf("%s. Usage: %s [flags]. Run '%s --help' for detailed usage.", err, cmd.Usage(), cmd.Usage()))
		return 1
	}

	// Dispatch to the integration layer.
	rawJSON, err := json.Marshal(argsJSON)
	if err != nil {
		writeCLIError(stderr, "INVALID_ARGUMENTS", fmt.Sprintf("failed to marshal arguments: %s", err))
		return 1
	}
	result := tr.CallTool(ctx, cmd.ToolName, rawJSON)

	if result.IsError {
		message := ""
		if len(result.Content) > 0 {
			message = result.Content[0].Text
		}
		code := "TOOL_ERROR"
		if apiCode, apiMessage, ok := extractAPIError(message); ok {
			code = apiCode
			message = apiMessage
		}
		writeCLIError(stderr, code, message)
		return 1
	}

	// Print output.
	for _, c := range result.Content {
		fmt.Fprintln(stdout, c.Text)
	}

	return 0
}

type CLICommand struct {
	Namespace   CLINamespace
	Action      CLIAction
	ToolName    string
	Category    string
	Description string
	Tool        Tool
}

func (c CLICommand) Usage() string {
	return fmt.Sprintf("143-tools %s %s", c.Namespace, c.Action)
}

func buildCLICommands(tools []Tool) []CLICommand {
	commands := make([]CLICommand, 0, len(tools))
	for _, tool := range tools {
		namespace, action, ok := cliPathForTool(tool.Name)
		if !ok {
			continue
		}
		commands = append(commands, CLICommand{
			Namespace:   namespace,
			Action:      action,
			ToolName:    tool.Name,
			Category:    cliCategory(namespace, action),
			Description: tool.Description,
			Tool:        tool,
		})
	}
	sort.Slice(commands, func(i, j int) bool {
		if commands[i].Namespace == commands[j].Namespace {
			return commands[i].Action < commands[j].Action
		}
		return commands[i].Namespace < commands[j].Namespace
	})
	return commands
}

// cliPathForTool maps a flat tool registry name to its hierarchical CLI path.
// The fixed 143-owned mappings use named constants; provider-derived names use
// typed conversions from the split tool name prefix and suffix.
func cliPathForTool(name string) (CLINamespace, CLIAction, bool) {
	switch {
	case name == "create_pr":
		return NamespacePR, ActionCreate, true
	case name == "issue_create":
		return NamespaceIssue, ActionCreate, true
	case name == "project_propose":
		return NamespaceProject, ActionPropose, true
	case name == "session_tabs_list":
		return NamespaceTabs, ActionList, true
	case name == "session_tabs_get":
		return NamespaceTabs, ActionGet, true
	case name == "session_tabs_create":
		return NamespaceTabs, ActionCreate, true
	case name == "session_tabs_send":
		return NamespaceTabs, ActionSend, true
	case name == "session_tabs_messages":
		return NamespaceTabs, ActionMessages, true
	case name == "eval_add":
		return NamespaceEval, ActionAdd, true
	case strings.HasPrefix(name, "log_"):
		return NamespaceLogs, CLIAction(strings.TrimPrefix(name, "log_")), true
	default:
		parts := strings.SplitN(name, "_", 2)
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return "", "", false
		}
		return CLINamespace(parts[0]), CLIAction(parts[1]), true
	}
}

func cliCategory(namespace CLINamespace, action CLIAction) string {
	switch namespace {
	case NamespaceLogs:
		return "Logs"
	case NamespaceIssue:
		return "143 issues"
	case NamespacePR:
		return "143 pull requests"
	case NamespaceProject:
		return "143 projects"
	case NamespaceTabs:
		return "Session tabs"
	case NamespaceEval:
		return "Eval"
	}
	a := string(action)
	switch {
	case strings.Contains(a, "error"):
		return "Error tracking"
	case strings.Contains(a, "task"):
		return "Tasks"
	case strings.Contains(a, "document"):
		return "Documents"
	case strings.Contains(a, "pr_review") || strings.Contains(a, "recent_pr"):
		return "Code review"
	case strings.Contains(a, "flaky") || strings.Contains(a, "test"):
		return "CI test insights"
	case strings.Contains(a, "message") || strings.Contains(a, "thread"):
		return "Messages"
	default:
		return "Tools"
	}
}

func isHelpArg(arg string) bool {
	return arg == "--help" || arg == "-h"
}

func hasNamespace(commands []CLICommand, namespace CLINamespace) bool {
	for _, cmd := range commands {
		if cmd.Namespace == namespace {
			return true
		}
	}
	return false
}

func findCLICommand(commands []CLICommand, namespace CLINamespace, action CLIAction) (CLICommand, bool) {
	for _, cmd := range commands {
		if cmd.Namespace == namespace && cmd.Action == action {
			return cmd, true
		}
	}
	return CLICommand{}, false
}

func replacementForOldFlatCommand(input string, commands []CLICommand) (string, bool) {
	for _, cmd := range commands {
		if cmd.ToolName == input {
			return cmd.Usage(), true
		}
	}
	return "", false
}

// parseFlagsToJSON converts --key value pairs into a map, coercing types
// based on the tool's input schema.
func parseFlagsToJSON(args []string, schema ToolSchema) (map[string]any, error) {
	result := make(map[string]any)

	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !strings.HasPrefix(arg, "--") {
			return nil, fmt.Errorf("unexpected argument %q (flags must start with --)", arg)
		}
		key := normalizeCLIFlagName(strings.TrimPrefix(arg, "--"))
		prop, hasProp := schema.Properties[key]

		if hasProp && prop.Type == "boolean" && (i+1 >= len(args) || strings.HasPrefix(args[i+1], "--")) {
			result[key] = true
			continue
		}
		if i+1 >= len(args) {
			return nil, fmt.Errorf("flag --%s requires a value", key)
		}
		i++
		value := args[i]

		// Coerce based on schema type.
		if hasProp {
			switch prop.Type {
			case "number":
				var n json.Number
				n = json.Number(value)
				if _, err := n.Float64(); err != nil {
					return nil, fmt.Errorf("flag --%s: expected number, got %q", key, value)
				}
				f, _ := n.Float64()
				result[key] = f
			case "array":
				// Accept comma-separated values: --states triage,backlog,in_progress
				parts := strings.Split(value, ",")
				result[key] = parts
			case "boolean":
				b, err := strconv.ParseBool(value)
				if err != nil {
					return nil, fmt.Errorf("flag --%s: expected boolean, got %q", key, value)
				}
				result[key] = b
			default:
				result[key] = value
			}
		} else {
			// Unknown flag — pass as string, let the tool handle validation.
			result[key] = value
		}
	}

	return result, nil
}

// checkRequired validates that all required fields are present.
func checkRequired(args map[string]any, required []string) error {
	for _, r := range required {
		if _, ok := args[r]; !ok {
			return fmt.Errorf("missing required flag: --%s", displayCLIFlagName(r))
		}
	}
	return nil
}

func normalizeCLIFlagName(key string) string {
	return strings.ReplaceAll(key, "-", "_")
}

func displayCLIFlagName(key string) string {
	return strings.ReplaceAll(key, "_", "-")
}

func writeCLIError(w io.Writer, code, message string) {
	payload := map[string]any{
		"error": map[string]string{
			"code":    code,
			"message": message,
		},
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		fmt.Fprintf(w, `{"error":{"code":"%s","message":"%s"}}`+"\n", code, strings.ReplaceAll(message, `"`, `'`))
		return
	}
	fmt.Fprintln(w, string(raw))
}

func extractAPIError(message string) (string, string, bool) {
	idx := strings.Index(message, `{"error":`)
	if idx < 0 {
		return "", "", false
	}
	var payload struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(message[idx:]), &payload); err != nil {
		return "", "", false
	}
	if payload.Error.Code == "" {
		return "", "", false
	}
	if payload.Error.Message == "" {
		payload.Error.Message = message
	}
	return payload.Error.Code, payload.Error.Message, true
}

// printCLIUsage prints compact top-level help listing namespaces.
func printCLIUsage(commands []CLICommand, w io.Writer) {
	if len(commands) == 0 {
		fmt.Fprintln(w, "143-tools: no integrations configured")
		fmt.Fprintln(w, "Set environment variables (SENTRY_AUTH_TOKEN, LINEAR_ACCESS_TOKEN, etc.) to enable tools.")
		return
	}

	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  143-tools <namespace> <action> [--flag value ...]")
	fmt.Fprintln(w, "  143-tools <namespace> --help")
	fmt.Fprintln(w, "  143-tools <namespace> <action> --help")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Namespaces:")

	for _, namespace := range cliNamespaceOrder(commands) {
		nsCommands := commandsForNamespace(commands, namespace)
		actions := make([]string, 0, len(nsCommands))
		for _, cmd := range nsCommands {
			actions = append(actions, string(cmd.Action))
		}
		category := nsCommands[0].Category
		fmt.Fprintf(w, "  %-10s %s: %s\n", namespace, category, strings.Join(actions, ", "))
	}

	fmt.Fprintln(w)
	fmt.Fprintln(w, "Run '143-tools <namespace> --help' for namespace-specific commands.")
}

func printNamespaceHelp(commands []CLICommand, namespace CLINamespace, w io.Writer) {
	nsCommands := commandsForNamespace(commands, namespace)
	if len(nsCommands) == 0 {
		fmt.Fprintf(w, "unknown namespace: %s\n", namespace)
		return
	}
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintf(w, "  143-tools %s <action> [--flag value ...]\n", namespace)
	fmt.Fprintf(w, "  143-tools %s <action> --help\n", namespace)
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Actions:")
	for _, cmd := range nsCommands {
		fmt.Fprintf(w, "  %-28s %s\n", cmd.Action, cmd.Description)
	}
	fmt.Fprintln(w)
	fmt.Fprintf(w, "Run '143-tools %s <action> --help' for detailed usage.\n", namespace)
}

func printActionHelp(cmd CLICommand, w io.Writer) {
	fmt.Fprintf(w, "Usage: %s [flags]\n\n", cmd.Usage())
	fmt.Fprintf(w, "%s\n\n", cmd.Description)

	if len(cmd.Tool.InputSchema.Properties) == 0 {
		return
	}
	fmt.Fprintln(w, "Flags:")

	propNames := make([]string, 0, len(cmd.Tool.InputSchema.Properties))
	for name := range cmd.Tool.InputSchema.Properties {
		propNames = append(propNames, name)
	}
	sort.Strings(propNames)

	requiredSet := make(map[string]bool)
	for _, r := range cmd.Tool.InputSchema.Required {
		requiredSet[r] = true
	}

	for _, pName := range propNames {
		prop := cmd.Tool.InputSchema.Properties[pName]
		req := ""
		if requiredSet[pName] {
			req = " (required)"
		}
		typeHint := prop.Type
		if len(prop.Enum) > 0 {
			typeHint = strings.Join(prop.Enum, "|")
		}
		if prop.Type == "array" {
			typeHint = "comma-separated"
		}
		fmt.Fprintf(w, "  --%-20s %-20s %s%s\n", displayCLIFlagName(pName), typeHint, prop.Description, req)
	}
}

func cliNamespaceOrder(commands []CLICommand) []CLINamespace {
	seen := make(map[CLINamespace]bool)
	namespaces := make([]CLINamespace, 0)
	for _, cmd := range commands {
		if seen[cmd.Namespace] {
			continue
		}
		seen[cmd.Namespace] = true
		namespaces = append(namespaces, cmd.Namespace)
	}
	sort.Slice(namespaces, func(i, j int) bool {
		return namespaces[i] < namespaces[j]
	})
	return namespaces
}

func commandsForNamespace(commands []CLICommand, namespace CLINamespace) []CLICommand {
	var result []CLICommand
	for _, cmd := range commands {
		if cmd.Namespace == namespace {
			result = append(result, cmd)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Action < result[j].Action
	})
	return result
}

// MainCLI is the entry point for the 143-tools binary. It builds the
// integration registry from environment variables and runs the CLI.
func MainCLI() {
	reg := BuildRegistryFromEnv(os.Stderr)
	tr := NewToolRegistry(reg)
	code := RunCLI(context.Background(), tr, os.Args[1:], os.Stdout, os.Stderr)
	os.Exit(code)
}
