package telemetry

func healthOrigin(origin string) string {
	switch origin {
	case "sweep", "queued", "direct":
		return origin
	default:
		return ""
	}
}
