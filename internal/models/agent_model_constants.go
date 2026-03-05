package models

const (
	PMModelOpus   = "opus"
	PMModelSonnet = "sonnet"
	PMModelHaiku  = "haiku"
)

var AvailablePMModels = []string{PMModelOpus, PMModelSonnet, PMModelHaiku}

const (
	ClaudeCodeModelOpus   = "claude-opus-4-6"
	ClaudeCodeModelSonnet = "claude-sonnet-4-5"
	ClaudeCodeModelHaiku  = "claude-haiku-4-5"
)

var AvailableClaudeCodeModels = []string{ClaudeCodeModelOpus, ClaudeCodeModelSonnet, ClaudeCodeModelHaiku}

const (
	GeminiCLIModelGemini3ProPreview   = "gemini-3-pro-preview"
	GeminiCLIModelGemini3FlashPreview = "gemini-3-flash-preview"
	GeminiCLIModelGemini25Pro         = "gemini-2.5-pro"
	GeminiCLIModelGemini25Flash       = "gemini-2.5-flash"
)

var AvailableGeminiCLIModels = []string{
	GeminiCLIModelGemini3ProPreview,
	GeminiCLIModelGemini3FlashPreview,
	GeminiCLIModelGemini25Pro,
	GeminiCLIModelGemini25Flash,
}

const (
	CodexModelGPT53Codex      = "gpt-5.3-codex"
	CodexModelGPT52Codex      = "gpt-5.2-codex"
	CodexModelGPT5Codex       = "gpt-5-codex"
	CodexModelGPT53CodexSpark = "gpt-5.3-codex-spark"
)

var AvailableCodexModels = []string{
	CodexModelGPT53Codex,
	CodexModelGPT52Codex,
	CodexModelGPT5Codex,
	CodexModelGPT53CodexSpark,
}
