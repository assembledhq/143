package integration

const maxInternalAPIErrorBodyBytes = 512

func truncateInternalAPIErrorBody(body []byte) string {
	if len(body) <= maxInternalAPIErrorBodyBytes {
		return string(body)
	}
	return string(body[:maxInternalAPIErrorBodyBytes]) + "...(truncated)"
}
