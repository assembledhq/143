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

// RunCLI executes a tool call from command-line arguments, printing the result
// to stdout. This provides the same dispatch as the MCP server but via a
// simple CLI that agents already know how to use.
//
// Usage:
//
//	143-tools <tool_name> [--flag value ...]
//	143-tools sentry_list_errors --severity high --limit 10
//	143-tools linear_create_task --title "Fix bug" --team_key ENG
//	143-tools --help
//	143-tools <tool_name> --help
func RunCLI(ctx context.Context, tr *ToolRegistry, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		printCLIUsage(tr, stdout)
		return 0
	}

	toolName := args[0]
	flagArgs := args[1:]

	// Per-tool help.
	for _, a := range flagArgs {
		if a == "--help" || a == "-h" {
			printToolHelp(tr, toolName, stdout)
			return 0
		}
	}

	// Find the tool definition for validation.
	var tool *Tool
	for _, t := range tr.ListTools() {
		if t.Name == toolName {
			tool = &t
			break
		}
	}
	if tool == nil {
		writeCLIError(stderr, "UNKNOWN_TOOL", fmt.Sprintf("unknown tool %q", toolName))
		return 1
	}

	// Parse flags into a JSON object.
	argsJSON, err := parseFlagsToJSON(flagArgs, tool.InputSchema)
	if err != nil {
		writeCLIError(stderr, "INVALID_ARGUMENTS", fmt.Sprintf("%s. Run '143-tools %s --help' for usage.", err, toolName))
		return 1
	}

	// Check required fields.
	if err := checkRequired(argsJSON, tool.InputSchema.Required); err != nil {
		writeCLIError(stderr, "INVALID_ARGUMENTS", fmt.Sprintf("%s. Run '143-tools %s --help' for usage.", err, toolName))
		return 1
	}

	// Dispatch to the integration layer.
	rawJSON, err := json.Marshal(argsJSON)
	if err != nil {
		writeCLIError(stderr, "INVALID_ARGUMENTS", fmt.Sprintf("failed to marshal arguments: %s", err))
		return 1
	}
	result := tr.CallTool(ctx, toolName, rawJSON)

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

// printCLIUsage prints the top-level help listing all available tools.
func printCLIUsage(tr *ToolRegistry, w io.Writer) {
	tools := tr.ListTools()
	if len(tools) == 0 {
		fmt.Fprintln(w, "143-tools: no integrations configured")
		fmt.Fprintln(w, "Set environment variables (SENTRY_AUTH_TOKEN, LINEAR_ACCESS_TOKEN, etc.) to enable tools.")
		return
	}

	fmt.Fprintln(w, "Usage: 143-tools <tool> [--flag value ...]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Available tools:")
	fmt.Fprintln(w)

	// Group by provider prefix.
	groups := groupToolsByProvider(tools)
	providerNames := make([]string, 0, len(groups))
	for name := range groups {
		providerNames = append(providerNames, name)
	}
	sort.Strings(providerNames)

	for _, provider := range providerNames {
		fmt.Fprintf(w, "  %s:\n", provider)
		for _, tool := range groups[provider] {
			fmt.Fprintf(w, "    %-40s %s\n", tool.Name, tool.Description)
		}
		fmt.Fprintln(w)
	}

	fmt.Fprintln(w, "Run '143-tools <tool> --help' for detailed usage of a specific tool.")
}

// printToolHelp prints detailed help for a single tool.
func printToolHelp(tr *ToolRegistry, name string, w io.Writer) {
	for _, tool := range tr.ListTools() {
		if tool.Name == name {
			fmt.Fprintf(w, "Usage: 143-tools %s [flags]\n\n", tool.Name)
			fmt.Fprintf(w, "%s\n\n", tool.Description)

			if len(tool.InputSchema.Properties) > 0 {
				fmt.Fprintln(w, "Flags:")

				// Sort properties for stable output.
				propNames := make([]string, 0, len(tool.InputSchema.Properties))
				for name := range tool.InputSchema.Properties {
					propNames = append(propNames, name)
				}
				sort.Strings(propNames)

				requiredSet := make(map[string]bool)
				for _, r := range tool.InputSchema.Required {
					requiredSet[r] = true
				}

				for _, pName := range propNames {
					prop := tool.InputSchema.Properties[pName]
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
			return
		}
	}
	fmt.Fprintf(w, "unknown tool: %s\n", name)
}

// groupToolsByProvider groups tools by their provider prefix (e.g. "sentry", "linear").
func groupToolsByProvider(tools []Tool) map[string][]Tool {
	groups := make(map[string][]Tool)
	for _, tool := range tools {
		parts := strings.SplitN(tool.Name, "_", 2)
		provider := parts[0]
		groups[provider] = append(groups[provider], tool)
	}
	return groups
}

// MainCLI is the entry point for the 143-tools binary. It builds the
// integration registry from environment variables and runs the CLI.
func MainCLI() {
	reg := BuildRegistryFromEnv(os.Stderr)
	tr := NewToolRegistry(reg)
	code := RunCLI(context.Background(), tr, os.Args[1:], os.Stdout, os.Stderr)
	os.Exit(code)
}
