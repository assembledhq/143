package agent

import "errors"

// NPMSetupError contains only validated, credential-safe installer diagnostics.
// Raw package-manager output must never be included in this error.
type NPMSetupError struct {
	Code             string           `json:"code"`
	Category         string           `json:"category"`
	NodeVersion      string           `json:"node_version"`
	NPMVersion       string           `json:"npm_version"`
	PhaseDurationsMS map[string]int64 `json:"phase_durations_ms"`
}

func (e *NPMSetupError) Error() string {
	return "npm dependency setup rejected incompatible project inputs"
}

// IsTerminalNPMSetupError survives wrapping and joined cleanup failures.
func IsTerminalNPMSetupError(err error) bool {
	var setup *NPMSetupError
	return errors.As(err, &setup) && setup.Category == "permanent" && (setup.Code == "EUSAGE" || setup.Code == "EBADENGINE" || setup.Code == "ELOCKVERIFY")
}
