package preview

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestPreviewToolAccessibilityViolations(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, html string
		expected   []a11yViolation
	}{
		{name: "empty document"},
		{name: "accessible controls", html: `<img src="hero.png" alt="Hero"><img alt=""><button>Save</button><a aria-label="Home"></a><button><img alt="Search"></button><input aria-label="Query"><input type="hidden">`},
		{name: "image needs alt", html: `<img src='hero.png'>`, expected: []a11yViolation{{Check: "missing_alt", Severity: "error", Element: `<img src="hero.png"`, Message: "Image is missing alt attribute. Screen readers cannot describe this image to users.", Fix: `Add an alt attribute with a meaningful description, or alt="" if the image is decorative.`}}},
		{name: "image without source", html: `<img>`, expected: []a11yViolation{{Check: "missing_alt", Severity: "error", Element: `<img`, Message: "Image is missing alt attribute. Screen readers cannot describe this image to users.", Fix: `Add an alt attribute with a meaningful description, or alt="" if the image is decorative.`}}},
		{name: "empty button", html: `<button><span></span></button>`, expected: []a11yViolation{{Check: "missing_aria", Severity: "error", Element: `<button>`, Message: "Button has no accessible label. Screen readers cannot identify its purpose.", Fix: "Add aria-label, aria-labelledby, or visible text content to the button."}}},
		{name: "empty self closing link", html: `<a/>`, expected: []a11yViolation{{Check: "missing_aria", Severity: "error", Element: `<a/>`, Message: "Link has no accessible label. Screen readers cannot identify its purpose.", Fix: "Add aria-label, aria-labelledby, or visible text content to the a."}}},
		{name: "unlabeled input", html: `<input type="text">`, expected: []a11yViolation{{Check: "missing_aria", Severity: "warning", Element: `<input type="text">`, Message: "Input has no accessible label. Screen readers cannot describe what this field is for.", Fix: "Add aria-label, a <label> element with matching for/id, or a placeholder attribute."}}},
		{name: "truncated markup is ignored", html: `<img <input <button`},
		{name: "input labeling heuristics", html: `<input type='hidden'><input id="username"><input placeholder="Query"><input title="Name"><input aria-labelledby="label"><button title="Save"/><a aria-label="Go"/>`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			snapshot := &DOMSnapshot{HTML: tt.html}
			actual := append(checkMissingAlt(snapshot), checkMissingARIA(snapshot)...)
			require.Equal(t, tt.expected, actual, "accessibility checks should identify exactly the unlabeled controls")
		})
	}
}

func TestPreviewToolContrastAnalysis(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, foreground, background, fontSize, fontWeight string
		expected                                           []a11yViolation
	}{
		{name: "high contrast", foreground: "#000", background: "#fff"},
		{name: "unsupported color skips heuristic", foreground: "inherit", background: "transparent"},
		{name: "missing background skips heuristic", foreground: "rgb(10,10,10)"},
		{name: "normal text fails", foreground: "rgb(0, 0, 0)", background: "#000000", fontSize: "16px", expected: []a11yViolation{{Check: "color_contrast", Severity: "error", Element: ".label", Message: "Contrast ratio 1.00:1 is below WCAG AA threshold (4.5:1 for normal text). Foreground: rgb(0, 0, 0), Background: #000000", Fix: "Increase contrast between text color and background color to at least 4.5:1"}}},
		{name: "large text threshold", foreground: "rgba(0,0,0,1)", background: "#000", fontSize: "24px", fontWeight: "400", expected: []a11yViolation{{Check: "color_contrast", Severity: "error", Element: ".label", Message: "Contrast ratio 1.00:1 is below WCAG AA threshold (3.0:1 for large text). Foreground: rgba(0,0,0,1), Background: #000", Fix: "Increase contrast between text color and background color to at least 3.0:1"}}},
		{name: "bold large text threshold", foreground: "rgba(0, 0, 0, 1)", background: "#000000", fontSize: "19px", fontWeight: "bold", expected: []a11yViolation{{Check: "color_contrast", Severity: "error", Element: ".label", Message: "Contrast ratio 1.00:1 is below WCAG AA threshold (3.0:1 for large text). Foreground: rgba(0, 0, 0, 1), Background: #000000", Fix: "Increase contrast between text color and background color to at least 3.0:1"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			actual := checkColorContrast(&DOMSnapshot{Styles: map[string]string{".label:color": tt.foreground, ".label:background-color": tt.background, ".label:font-size": tt.fontSize, ".label:font-weight": tt.fontWeight, "unstructured": "unused"}})
			require.Equal(t, tt.expected, actual, "contrast heuristics should preserve normal and large text thresholds")
		})
	}
}

func TestPreviewToolHTMLAttributeParsing(t *testing.T) {
	t.Parallel()
	tests := []struct{ name, tag, attribute, expected string }{
		{"double quoted", `<img src="hero.png">`, "src", "hero.png"},
		{"single quoted", `<img src='hero.png'>`, "src", "hero.png"},
		{"unquoted", `<img src=hero.png alt=Hero>`, "src", "hero.png"},
		{"missing", `<img alt="Hero">`, "src", ""},
		{"truncated value", `<img src=`, "src", ""},
		{"unclosed quote", `<img src="hero`, "src", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.expected, extractAttr(tt.tag, tt.attribute), "attribute parsing should tolerate truncated DOM snapshots")
		})
	}
}

func TestPreviewToolLargeTextThresholds(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, size, weight string
		expected           bool
	}{
		{"normal pixels", "23.99px", "400", false},
		{"large pixels", "24px", "400", true},
		{"large points", "18pt", "normal", true},
		{"normal points", "17.9pt", "400", false},
		{"large bold pixels", "18.66px", "700", true},
		{"large bold points", "14pt", "bold", true},
		{"bolder keyword", "19px", "bolder", true},
		{"numeric bold", "19px", "800", true},
		{"not bold enough", "19px", "600", false},
		{"invalid unit", "24rem", "bold", false},
		{"unit required", "24", "bold", false},
		{"invalid number", "bigpx", "bold", false},
		{"infinite", "+Infpx", "bold", false},
		{"not a number", "NaNpx", "bold", false},
		{"outer whitespace", " 24px ", "400", true},
		{"negative", "-24px", "bold", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.expected, isLargeTextSize(tt.size, tt.weight), "only supported finite CSS sizes meeting WCAG large-text thresholds should qualify")
		})
	}
}
