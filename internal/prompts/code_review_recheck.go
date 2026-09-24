package prompts

// CodeReviewRecheckPromptData contains immutable baseline evidence and a fresh
// evidence capture. The renderer escapes delimiters in every untrusted field.
type CodeReviewRecheckPromptData struct {
	BaselineID     string
	InputDigest    string
	Baseline       string
	Requirements   string
	TextEvidence   string
	VisualEvidence []CodeReviewVisualEvidencePromptData
}

func CodeReviewRecheckPrompt(data CodeReviewRecheckPromptData) string {
	data.BaselineID = sanitizeUntrustedXML(data.BaselineID)
	data.InputDigest = sanitizeUntrustedXML(data.InputDigest)
	data.Baseline = sanitizeUntrustedXML(data.Baseline)
	data.Requirements = sanitizeUntrustedXML(data.Requirements)
	data.TextEvidence = sanitizeUntrustedXML(data.TextEvidence)
	data.VisualEvidence = sanitizeCodeReviewVisualEvidence(data.VisualEvidence)
	return render("code_review_recheck.template", data)
}
