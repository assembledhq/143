package integration

import "github.com/assembledhq/143/internal/internalapi"

func internalAPIBaseURL(raw string) string {
	return internalapi.NormalizeInternalBaseURL(raw)
}
