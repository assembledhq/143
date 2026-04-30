package preview

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/rs/zerolog"
)

// =============================================================================
// Types
// =============================================================================

// DesignTokenMap holds extracted design tokens from a project's theme configuration.
// Tokens maps a human-readable token name (e.g. "bg-blue-500") to its resolved
// value (e.g. "#3b82f6"). Framework indicates which extraction strategy produced
// the tokens.
type DesignTokenMap struct {
	Tokens    map[string]string `json:"tokens"`
	Framework string            `json:"framework"` // "tailwind", "css-vars", "theme-file", ""
}

// TokenExtractor reads files from a sandbox and parses design tokens from
// various sources (Tailwind config, CSS custom properties, theme files).
//
// The readFile and listFiles functions are bound to a specific sandbox at
// construction time, so the extractor does not need to carry sandbox state.
type TokenExtractor struct {
	readFile  func(ctx context.Context, path string) ([]byte, error)
	listFiles func(ctx context.Context, pattern string) ([]string, error)
	logger    zerolog.Logger
}

// NewTokenExtractor creates a TokenExtractor. The readFile function should
// return the contents of a file inside the sandbox (or an error if not found).
// The listFiles function should return file paths matching a glob pattern.
//
// Example binding to a sandbox:
//
//	extractor := preview.NewTokenExtractor(
//	    func(ctx context.Context, path string) ([]byte, error) {
//	        return executor.ReadFile(ctx, sb, path)
//	    },
//	    func(ctx context.Context, pattern string) ([]string, error) {
//	        var out bytes.Buffer
//	        _, err := executor.Exec(ctx, sb, "find "+sb.WorkDir+" -name '*.css' -type f", &out, io.Discard)
//	        if err != nil { return nil, err }
//	        return strings.Split(strings.TrimSpace(out.String()), "\n"), nil
//	    },
//	    logger,
//	)
func NewTokenExtractor(
	readFile func(ctx context.Context, path string) ([]byte, error),
	listFiles func(ctx context.Context, pattern string) ([]string, error),
	logger zerolog.Logger,
) *TokenExtractor {
	return &TokenExtractor{
		readFile:  readFile,
		listFiles: listFiles,
		logger:    logger.With().Str("component", "token_extractor").Logger(),
	}
}

// =============================================================================
// Public API
// =============================================================================

// ExtractTokens tries all token extraction strategies in priority order and
// returns the first successful result. The order is:
//  1. Tailwind CSS config (tailwind.config.js/ts/mjs/cjs)
//  2. CSS custom properties (*.css files)
//  3. Theme files (theme.js, tokens.json, design-tokens.yaml, etc.)
//
// Returns an empty DesignTokenMap (not nil) if no tokens are found.
func (te *TokenExtractor) ExtractTokens(ctx context.Context) (*DesignTokenMap, error) {
	// 1. Try Tailwind config.
	tokens, err := te.extractTailwind(ctx)
	if err != nil {
		te.logger.Debug().Err(err).Msg("tailwind extraction failed")
	}
	if tokens != nil && len(tokens.Tokens) > 0 {
		te.logger.Info().Int("count", len(tokens.Tokens)).Msg("extracted tailwind tokens")
		return tokens, nil
	}

	// 2. Try CSS custom properties.
	tokens, err = te.extractCSSVars(ctx)
	if err != nil {
		te.logger.Debug().Err(err).Msg("css vars extraction failed")
	}
	if tokens != nil && len(tokens.Tokens) > 0 {
		te.logger.Info().Int("count", len(tokens.Tokens)).Msg("extracted css custom property tokens")
		return tokens, nil
	}

	// 3. Try theme files.
	tokens, err = te.extractThemeFile(ctx)
	if err != nil {
		te.logger.Debug().Err(err).Msg("theme file extraction failed")
	}
	if tokens != nil && len(tokens.Tokens) > 0 {
		te.logger.Info().Int("count", len(tokens.Tokens)).Msg("extracted theme file tokens")
		return tokens, nil
	}

	te.logger.Debug().Msg("no design tokens found")
	return &DesignTokenMap{Tokens: map[string]string{}, Framework: ""}, nil
}

// ReverseMapValue looks up a computed CSS value in the token map and returns the
// matching token name. This is used when the inspector reads a computed style
// (e.g. "rgb(59, 130, 246)") and we want to display the token name ("blue-500").
//
// The property parameter (e.g. "background-color") is used to add context-aware
// prefixes when matching (e.g. "bg-" for Tailwind background utilities).
//
// Returns empty string if no match is found.
func ReverseMapValue(tokens *DesignTokenMap, property, computedValue string) string {
	if tokens == nil || len(tokens.Tokens) == 0 {
		return ""
	}

	normalized := normalizeColorValue(computedValue)

	// First pass: for Tailwind, prefer context-aware prefix match.
	if tokens.Framework == "tailwind" {
		prefix := tailwindPropertyPrefix(property)
		if prefix != "" {
			for name, value := range tokens.Tokens {
				if normalizeColorValue(value) == normalized && strings.HasPrefix(name, prefix) {
					return name
				}
			}
		}
	}

	// Second pass: fall back to any exact value match.
	for name, value := range tokens.Tokens {
		if normalizeColorValue(value) == normalized {
			return name
		}
	}

	return ""
}

// =============================================================================
// Tailwind extraction
// =============================================================================

var (
	// Paths to check for the Tailwind configuration file.
	tailwindConfigPaths = []string{
		"tailwind.config.js",
		"tailwind.config.ts",
		"tailwind.config.mjs",
		"tailwind.config.cjs",
	}

	// reThemeBlock matches the theme or theme.extend block in a Tailwind config.
	// It captures the outer braces of the theme object. Because we use regex
	// (not a JS parser), this is best-effort and handles the common formatting.
	reThemeExtend = regexp.MustCompile(`(?s)theme\s*:\s*\{[^}]*extend\s*:\s*\{(.+?)\n\s{2,4}\}`)
	reThemeBlock  = regexp.MustCompile(`(?s)theme\s*:\s*\{(.+?)\n\s{0,2}\}`)

	// reColorEntry matches "'key': 'value'" or "key: 'value'" or 'key: "value"'
	// within a colors/spacing/fontSize block.
	reKeyValue = regexp.MustCompile(`['"]?([\w-]+)['"]?\s*:\s*['"]([^'"]+)['"]`)

	// reSectionBlock matches a named section like `colors: { ... }` inside theme config.
	// NOTE: Uses [^}] which cannot handle nested braces (e.g. colors: { blue: { 500: "#..." } }).
	// A proper JS parser would be needed for nested Tailwind color objects. For now this
	// only extracts flat key-value sections, which covers the most common configurations.
	reSectionBlock = regexp.MustCompile(`(?s)(colors|spacing|fontSize|fontFamily|borderRadius|lineHeight)\s*:\s*\{([^}]+)\}`)
)

func (te *TokenExtractor) extractTailwind(ctx context.Context) (*DesignTokenMap, error) {
	var configContent []byte
	for _, path := range tailwindConfigPaths {
		data, err := te.readFile(ctx, path)
		if err != nil {
			continue
		}
		configContent = data
		break
	}
	if configContent == nil {
		return nil, nil
	}

	tokens := make(map[string]string)
	content := string(configContent)

	// Try to extract from theme.extend first, then theme.
	themeContent := ""
	if m := reThemeExtend.FindStringSubmatch(content); len(m) > 1 {
		themeContent = m[1]
	} else if m := reThemeBlock.FindStringSubmatch(content); len(m) > 1 {
		themeContent = m[1]
	}

	if themeContent == "" {
		// Fallback: scan the entire config for section blocks.
		themeContent = content
	}

	// Extract named sections and their key-value pairs.
	sectionMatches := reSectionBlock.FindAllStringSubmatch(themeContent, -1)
	for _, sm := range sectionMatches {
		section := sm[1] // e.g. "colors"
		body := sm[2]    // inner content

		kvMatches := reKeyValue.FindAllStringSubmatch(body, -1)
		for _, kv := range kvMatches {
			key := kv[1]
			value := kv[2]
			tokenName := section + "." + key
			tokens[tokenName] = value
		}
	}

	// Also scan for top-level color definitions that might use Tailwind's
	// flat format: e.g. "'primary': '#1a1a2e'"
	if len(tokens) == 0 {
		kvMatches := reKeyValue.FindAllStringSubmatch(themeContent, -1)
		for _, kv := range kvMatches {
			tokens[kv[1]] = kv[2]
		}
	}

	if len(tokens) == 0 {
		return nil, nil
	}

	return &DesignTokenMap{
		Tokens:    tokens,
		Framework: "tailwind",
	}, nil
}

// =============================================================================
// CSS custom properties extraction
// =============================================================================

var reCSSVar = regexp.MustCompile(`--([\w-]+)\s*:\s*([^;]+)`)

func (te *TokenExtractor) extractCSSVars(ctx context.Context) (*DesignTokenMap, error) {
	files, err := te.listFiles(ctx, "*.css")
	if err != nil {
		return nil, fmt.Errorf("list css files: %w", err)
	}

	tokens := make(map[string]string)

	for _, file := range files {
		if file == "" {
			continue
		}

		// Skip node_modules and build output directories.
		if strings.Contains(file, "node_modules/") ||
			strings.Contains(file, ".next/") ||
			strings.Contains(file, "dist/") ||
			strings.Contains(file, "build/") {
			continue
		}

		data, err := te.readFile(ctx, file)
		if err != nil {
			te.logger.Debug().Err(err).Str("file", file).Msg("skipping unreadable css file")
			continue
		}

		matches := reCSSVar.FindAllStringSubmatch(string(data), -1)
		for _, m := range matches {
			name := "--" + m[1]
			value := strings.TrimSpace(m[2])
			tokens[name] = value
		}
	}

	if len(tokens) == 0 {
		return nil, nil
	}

	return &DesignTokenMap{
		Tokens:    tokens,
		Framework: "css-vars",
	}, nil
}

// =============================================================================
// Theme file extraction
// =============================================================================

var (
	// Known theme file paths to probe.
	themeFilePaths = []string{
		"theme.js",
		"theme.ts",
		"src/theme.js",
		"src/theme.ts",
		"src/styles/theme.js",
		"src/styles/theme.ts",
		"tokens.json",
		"design-tokens.json",
		"src/tokens.json",
		"src/design-tokens.json",
		"design-tokens.yaml",
		"design-tokens.yml",
		"src/design-tokens.yaml",
		"src/design-tokens.yml",
		"styles/tokens.json",
		"styles/design-tokens.json",
	}

	// reJSONKeyValue matches "key": "value" in JSON files.
	reJSONKeyValue = regexp.MustCompile(`"([\w.-]+)"\s*:\s*"([^"]+)"`)

	// reYAMLKeyValue matches key: value in YAML files (simple single-line).
	reYAMLKeyValue = regexp.MustCompile(`^(\s*)([\w.-]+)\s*:\s*['"]?([^'"#\n]+?)['"]?\s*$`)
)

func (te *TokenExtractor) extractThemeFile(ctx context.Context) (*DesignTokenMap, error) {
	for _, path := range themeFilePaths {
		data, err := te.readFile(ctx, path)
		if err != nil {
			continue
		}

		tokens := make(map[string]string)

		if strings.HasSuffix(path, ".json") {
			matches := reJSONKeyValue.FindAllStringSubmatch(string(data), -1)
			for _, m := range matches {
				tokens[m[1]] = m[2]
			}
		} else if strings.HasSuffix(path, ".yaml") || strings.HasSuffix(path, ".yml") {
			lines := strings.Split(string(data), "\n")
			for _, line := range lines {
				m := reYAMLKeyValue.FindStringSubmatch(line)
				if m != nil {
					tokens[m[2]] = m[3]
				}
			}
		} else {
			// JS/TS theme files: extract key-value pairs.
			matches := reKeyValue.FindAllStringSubmatch(string(data), -1)
			for _, m := range matches {
				tokens[m[1]] = m[2]
			}
		}

		if len(tokens) > 0 {
			return &DesignTokenMap{
				Tokens:    tokens,
				Framework: "theme-file",
			}, nil
		}
	}

	return nil, nil
}

// =============================================================================
// Helpers
// =============================================================================

// normalizeColorValue converts color values to a canonical form for comparison.
// Handles hex shorthand (#fff -> #ffffff) and trims whitespace.
func normalizeColorValue(v string) string {
	v = strings.TrimSpace(strings.ToLower(v))

	// Expand 3-char hex to 6-char: #abc -> #aabbcc.
	if len(v) == 4 && v[0] == '#' {
		v = "#" + string(v[1]) + string(v[1]) + string(v[2]) + string(v[2]) + string(v[3]) + string(v[3])
	}

	// Normalize rgb() whitespace: "rgb( 59, 130 , 246 )" -> "rgb(59,130,246)".
	if strings.HasPrefix(v, "rgb") {
		v = strings.ReplaceAll(v, " ", "")
	}

	return v
}

// tailwindPropertyPrefix maps a CSS property name to the Tailwind utility prefix
// used in class names.
func tailwindPropertyPrefix(property string) string {
	switch property {
	case "background-color":
		return "bg-"
	case "color":
		return "text-"
	case "border-color":
		return "border-"
	case "outline-color":
		return "outline-"
	case "fill":
		return "fill-"
	case "stroke":
		return "stroke-"
	case "font-size":
		return "text-"
	case "padding", "padding-top", "padding-right", "padding-bottom", "padding-left":
		return "p"
	case "margin", "margin-top", "margin-right", "margin-bottom", "margin-left":
		return "m"
	case "gap":
		return "gap-"
	case "border-radius":
		return "rounded-"
	case "line-height":
		return "leading-"
	default:
		return ""
	}
}
