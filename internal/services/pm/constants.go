package pm

// defaultPMMaxTokens is the fallback PM token limit when org settings are unavailable.
// Prefer using settings.ContextLimits.PMMaxTokens from the org's parsed settings.
const defaultPMMaxTokens = 50_000
