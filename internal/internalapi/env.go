package internalapi

// CodingSessionIDEnvVar identifies the current coding session inside an agent
// sandbox. It is contextual routing metadata; authorization remains derived
// from the signed internal API token.
const CodingSessionIDEnvVar = "CODING_SESSION_ID"
