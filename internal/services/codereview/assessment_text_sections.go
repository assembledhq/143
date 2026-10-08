package codereview

import (
	"errors"
	"regexp"
	"strings"
)

type reviewEvidenceSection struct {
	Label   string
	Content string
}

type reviewMarkdownSegment struct {
	label  string
	header string
	text   string
}

var evidenceHeading = regexp.MustCompile(`^ {0,3}(#{1,6})[ \t]+([^\r\n]+?)[ \t]*\r?\n?$`)

func reviewEvidenceLabel(raw string) string {
	label := strings.ToLower(strings.TrimSpace(strings.TrimRight(strings.TrimSpace(raw), "#")))
	label = strings.TrimSuffix(label, ":")
	switch label {
	case "testing", "tests", "evidence", "validation", "test logs", "logs", "benchmarks", "benchmark results", "screenshots":
		return label
	default:
		return ""
	}
}

// splitReviewEvidenceSections treats ordinary Markdown evidence headings as
// source boundaries. Everything outside those sections stays byte-significant
// intent (apart from the existing image-node normalization). Ambiguous fences,
// HTML headings, duplicate evidence headings, and unsupported Markdown return
// an error so capture can retain the entire source as opaque intent.
func splitReviewEvidenceSections(raw string) (string, []reviewEvidenceSection, error) {
	segments := make([]reviewMarkdownSegment, 0, 8)
	current := reviewMarkdownSegment{}
	fenceChar, fenceSize := byte(0), 0
	seen := make(map[string]bool)
	flush := func() {
		if current.text != "" {
			segments = append(segments, current)
		}
		current = reviewMarkdownSegment{}
	}
	for _, line := range strings.SplitAfter(raw, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.Contains(line, "<!--") || strings.Contains(line, "-->") ||
			strings.HasPrefix(trimmed, "<") || strings.Contains(strings.ToLower(line), "<h") && strings.Contains(line, ">") {
			return "", nil, errors.New("HTML boundary is ambiguous")
		}
		if char, size, ok := reviewFence(trimmed); ok {
			if fenceChar == 0 {
				fenceChar, fenceSize = char, size
				if current.label == "" {
					return "", nil, errors.New("code fence outside evidence section")
				}
			} else if char == fenceChar && size >= fenceSize && strings.Trim(trimmed[size:], " \t") == "" {
				fenceChar, fenceSize = 0, 0
			}
			current.text += line
			continue
		}
		if fenceChar != 0 {
			current.text += line
			continue
		}
		if match := evidenceHeading.FindStringSubmatch(line); match != nil {
			flush()
			label := reviewEvidenceLabel(match[2])
			if label != "" {
				if seen[label] {
					return "", nil, errors.New("duplicate evidence heading")
				}
				seen[label] = true
				current = reviewMarkdownSegment{label: label, header: line, text: line}
				continue
			}
			current.text = line
			continue
		}
		// Setext headings can reclassify preceding text and are not safely split.
		if trimmed != "" && (strings.Trim(trimmed, "=") == "" || strings.Trim(trimmed, "-") == "") {
			return "", nil, errors.New("setext heading is ambiguous")
		}
		current.text += line
	}
	if fenceChar != 0 {
		return "", nil, errors.New("unclosed evidence code fence")
	}
	flush()
	sections := make([]reviewEvidenceSection, 0)
	var intent strings.Builder
	for i, segment := range segments {
		if segment.label == "" {
			normalized, err := normalizeReviewIntentPlain(segment.text)
			if err != nil {
				return "", nil, err
			}
			intent.WriteString(normalized)
			continue
		}
		sections = append(sections, reviewEvidenceSection{Label: segment.label, Content: segment.text})
		tail := true
		for _, later := range segments[i+1:] {
			if later.label == "" && strings.TrimSpace(later.text) != "" {
				tail = false
				break
			}
		}
		if !tail {
			intent.WriteString(segment.header)
			intent.WriteString("[evidence-section]\n")
		}
	}
	return intent.String(), sections, nil
}

func reviewFence(trimmed string) (byte, int, bool) {
	if len(trimmed) < 3 || (trimmed[0] != '`' && trimmed[0] != '~') {
		return 0, 0, false
	}
	n := 0
	for n < len(trimmed) && trimmed[n] == trimmed[0] {
		n++
	}
	return trimmed[0], n, n >= 3
}
