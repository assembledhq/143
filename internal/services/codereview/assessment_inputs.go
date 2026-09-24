package codereview

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/google/uuid"
)

// ReviewInputManifestVersion changes whenever the meaning of a captured field changes.
const ReviewInputManifestVersion = 1

// ReviewChangedFile binds a path and its exact diff identity. PatchDigest must
// describe the fetched patch, including its availability; a missing patch is
// never represented by an empty digest.
type ReviewChangedFile struct {
	Path        string `json:"path"`
	Status      string `json:"status"`
	PatchDigest string `json:"patch_digest"`
}

type ReviewCodeInput struct {
	OrgID         uuid.UUID           `json:"org_id"`
	RepositoryID  uuid.UUID           `json:"repository_id"`
	PullRequestID uuid.UUID           `json:"pull_request_id"`
	HeadSHA       string              `json:"head_sha"`
	BaseSHA       string              `json:"base_sha"`
	BaseRef       string              `json:"base_ref"`
	Files         []ReviewChangedFile `json:"files"`
	FilesComplete bool                `json:"files_complete"`
}

// ReviewContractInput names every versioned dependency that can affect the
// review. Callers must hash rendered prompt content and repository instructions,
// rather than assuming a stable template name is sufficient.
type ReviewContractInput struct {
	PolicyID                 uuid.UUID `json:"policy_id"`
	PolicyVersion            int64     `json:"policy_version"`
	PolicyDigest             string    `json:"policy_digest"`
	RosterDigest             string    `json:"roster_digest"`
	ModelConfigurationDigest string    `json:"model_configuration_digest"`
	PromptContractVersion    string    `json:"prompt_contract_version"`
	PromptContentDigest      string    `json:"prompt_content_digest"`
	InstructionsDigest       string    `json:"instructions_digest"`
	ExternalInputsComplete   bool      `json:"external_inputs_complete"`
}

// ReviewVisualImage identifies captured content, not merely a URL. SourceText
// and AltText are evidence, not instructions or trusted PR intent.
type ReviewVisualImage struct {
	SourceID      string `json:"source_id"`
	SourceURL     string `json:"source_url"`
	SourceText    string `json:"source_text"`
	AltText       string `json:"alt_text"`
	ContentDigest string `json:"content_digest"`
}

type ReviewVisualInput struct {
	Images                   []ReviewVisualImage `json:"images"`
	CaptureComplete          bool                `json:"capture_complete"`
	SourceProvenanceComplete bool                `json:"source_provenance_complete"`
}

// ReviewRequestInput excludes request UUID, actor, and trigger envelope. A
// substantive objection or instruction is included and must change routing.
type ReviewRequestInput struct {
	SubstantiveText string `json:"substantive_text"`
	DisputeRouted   bool   `json:"dispute_routed"`
}

type ReviewGateInput struct {
	SnapshotDigest string `json:"snapshot_digest"`
	Complete       bool   `json:"complete"`
}

type ReviewInputCapture struct {
	Code        ReviewCodeInput     `json:"code"`
	Contract    ReviewContractInput `json:"contract"`
	Title       string              `json:"title"`
	Description string              `json:"description"`
	Visual      ReviewVisualInput   `json:"visual"`
	Request     ReviewRequestInput  `json:"request"`
	Gates       ReviewGateInput     `json:"gates"`
}

// ReviewInputManifest is serializable into an assessment's json.RawMessage.
// Each digest binds one named component; AllDigest binds their ordered tuple.
// Description is retained so a later capture can be audited without inventing
// a lossy interpretation of Markdown intent.
type ReviewInputManifest struct {
	InputVersion   int                 `json:"input_version"`
	ReuseEligible  bool                `json:"reuse_eligible"`
	Code           ReviewCodeInput     `json:"code"`
	Contract       ReviewContractInput `json:"contract"`
	Title          string              `json:"title"`
	Description    string              `json:"description"`
	Visual         ReviewVisualInput   `json:"visual"`
	Request        ReviewRequestInput  `json:"request"`
	Gates          ReviewGateInput     `json:"gates"`
	CodeDigest     string              `json:"code_digest"`
	ContractDigest string              `json:"contract_digest"`
	IntentDigest   string              `json:"intent_digest"`
	VisualDigest   string              `json:"visual_digest"`
	RequestDigest  string              `json:"request_digest"`
	GateDigest     string              `json:"gate_digest"`
	InputDigest    string              `json:"input_digest"`
}

func BuildReviewInputManifest(input ReviewInputCapture) (ReviewInputManifest, error) {
	// Sorting must not mutate the caller's snapshots; they may also be retained
	// as immutable source evidence for the assessment.
	input.Code.Files = append([]ReviewChangedFile(nil), input.Code.Files...)
	input.Visual.Images = append([]ReviewVisualImage(nil), input.Visual.Images...)
	if input.Code.OrgID == uuid.Nil || input.Code.RepositoryID == uuid.Nil || input.Code.PullRequestID == uuid.Nil ||
		strings.TrimSpace(input.Code.HeadSHA) == "" || strings.TrimSpace(input.Code.BaseSHA) == "" ||
		strings.TrimSpace(input.Code.BaseRef) == "" || !input.Code.FilesComplete {
		return ReviewInputManifest{}, errors.New("incomplete code input")
	}
	if strings.TrimSpace(input.Title) == "" {
		return ReviewInputManifest{}, errors.New("missing PR title")
	}
	if input.Contract.PolicyID == uuid.Nil || input.Contract.PolicyVersion < 1 ||
		input.Contract.PromptContractVersion == "" || !input.Contract.ExternalInputsComplete ||
		!allDigests(input.Contract.PolicyDigest, input.Contract.RosterDigest, input.Contract.ModelConfigurationDigest,
			input.Contract.PromptContentDigest, input.Contract.InstructionsDigest) {
		return ReviewInputManifest{}, errors.New("incomplete review contract")
	}
	if !input.Visual.CaptureComplete || !input.Visual.SourceProvenanceComplete || !validDigest(input.Gates.SnapshotDigest) || !input.Gates.Complete {
		return ReviewInputManifest{}, errors.New("incomplete visual evidence or live gates")
	}
	for _, file := range input.Code.Files {
		if file.Path == "" || file.Status == "" || !validDigest(file.PatchDigest) {
			return ReviewInputManifest{}, fmt.Errorf("incomplete changed file %q", file.Path)
		}
	}
	for _, img := range input.Visual.Images {
		if img.SourceID == "" || img.SourceURL == "" || !validDigest(img.ContentDigest) {
			return ReviewInputManifest{}, fmt.Errorf("incomplete visual image %q", img.SourceID)
		}
	}
	intent, err := normalizeReviewIntent(input.Description)
	reuseEligible := err == nil
	if err != nil {
		intent = input.Description
	}
	// GitHub's file order and evidence discovery order are not semantic inputs.
	sort.Slice(input.Code.Files, func(i, j int) bool { return input.Code.Files[i].Path < input.Code.Files[j].Path })
	sort.Slice(input.Visual.Images, func(i, j int) bool { return input.Visual.Images[i].SourceID < input.Visual.Images[j].SourceID })
	for i := 1; i < len(input.Code.Files); i++ {
		if input.Code.Files[i].Path == input.Code.Files[i-1].Path {
			return ReviewInputManifest{}, errors.New("duplicate changed file")
		}
	}
	for i := 1; i < len(input.Visual.Images); i++ {
		if input.Visual.Images[i].SourceID == input.Visual.Images[i-1].SourceID {
			return ReviewInputManifest{}, errors.New("duplicate visual source")
		}
	}
	m := ReviewInputManifest{InputVersion: ReviewInputManifestVersion, ReuseEligible: reuseEligible, Code: input.Code, Contract: input.Contract,
		Title: input.Title, Description: input.Description, Visual: input.Visual, Request: input.Request, Gates: input.Gates}
	m.CodeDigest = digestJSON(m.Code)
	m.ContractDigest = digestJSON(m.Contract)
	m.IntentDigest = digestJSON(struct {
		Title, NormalizedDescription string
		ReuseEligible                bool
	}{m.Title, intent, reuseEligible})
	m.VisualDigest = digestJSON(m.Visual)
	m.RequestDigest = digestJSON(m.Request)
	m.GateDigest = digestJSON(m.Gates)
	m.InputDigest = digestJSON([]string{m.CodeDigest, m.ContractDigest, m.IntentDigest, m.VisualDigest, m.RequestDigest, m.GateDigest})
	return m, nil
}

func digestJSON(v any) string {
	b, _ := json.Marshal(v) // only fixed, JSON-serializable input structs reach this helper
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

var hexDigest = regexp.MustCompile(`^[0-9a-f]{64}$`)

func validDigest(v string) bool { return hexDigest.MatchString(v) }
func allDigests(v ...string) bool {
	for _, d := range v {
		if !validDigest(d) {
			return false
		}
	}
	return true
}

// normalizeReviewIntent recognizes only simple Markdown image nodes. A whole
// image-only line can be inserted or removed without changing intent. An inline
// image can be replaced at the same position. Everything else, including prose,
// caption lines, code fences, HTML, escapes, references and malformed images,
// is either retained byte-for-byte or rejected. This deliberately grants no
// semantic interpretation to image alt text.
func normalizeReviewIntent(description string) (string, error) {
	var out strings.Builder
	for _, line := range strings.SplitAfter(description, "\n") {
		if strings.HasPrefix(line, "    ") || strings.HasPrefix(line, "\t") {
			return "", errors.New("indented Markdown code or ambiguous image")
		}
		if strings.ContainsAny(line, "`\\") || strings.Contains(line, "<img") || strings.Contains(line, "![][") {
			return "", errors.New("unsupported Markdown construct")
		}
		body := strings.TrimSuffix(line, "\n")
		body = strings.TrimSuffix(body, "\r")
		normalized, count, err := replaceSimpleImages(body)
		if err != nil {
			return "", err
		}
		if count == 1 && strings.TrimSpace(normalized) == "[image]" {
			continue
		}
		out.WriteString(normalized)
		if strings.HasSuffix(line, "\r\n") {
			out.WriteString("\r\n")
		} else if strings.HasSuffix(line, "\n") {
			out.WriteByte('\n')
		}
	}
	return out.String(), nil
}

func replaceSimpleImages(line string) (string, int, error) {
	var out strings.Builder
	count := 0
	for i := 0; i < len(line); {
		if line[i] != '!' {
			out.WriteByte(line[i])
			i++
			continue
		}
		if i+1 >= len(line) || line[i+1] != '[' {
			return "", 0, errors.New("ambiguous image marker")
		}
		altEnd := strings.IndexByte(line[i+2:], ']')
		if altEnd < 0 {
			return "", 0, errors.New("unclosed image alt text")
		}
		altEnd += i + 2
		if strings.ContainsAny(line[i+2:altEnd], "[]\\\n") || altEnd+1 >= len(line) || line[altEnd+1] != '(' {
			return "", 0, errors.New("unsupported image alt text or reference")
		}
		urlEnd := strings.IndexByte(line[altEnd+2:], ')')
		if urlEnd < 0 {
			return "", 0, errors.New("unclosed image URL")
		}
		urlEnd += altEnd + 2
		url := line[altEnd+2 : urlEnd]
		if url == "" || strings.ContainsAny(url, "()<>\\\n\t ") {
			return "", 0, errors.New("unsupported image URL")
		}
		out.WriteString("[image]")
		count++
		i = urlEnd + 1
	}
	return out.String(), count, nil
}
