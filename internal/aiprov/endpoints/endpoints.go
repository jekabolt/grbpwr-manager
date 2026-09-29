// Package endpoints is a leaf: it imports nothing, so the probe (which the dto and the dependency
// graph reach) and the transports (which aiprov reaches) can both read it without a cycle.
package endpoints

// Provider hosts and API roots — CONSTANTS, deliberately not configuration.
//
// A base URL that can be edited is a place a key can be sent: an admin (or a leaked write) pointing a
// provider at another host would hand every request's credential to that host. So no provider row,
// env variable or panel field names a base URL; the probe and every transport read the one below,
// and a test pins that the probe's hosts are exactly these (probe.TestNoProbeIsAPaidCall). The
// OpenRouter chat client keeps its legacy OPENROUTER_BASE_URL knob for the stand and defaults to
// OpenRouterAPIBase.
const (
	OpenAIHost     = "https://api.openai.com"
	AnthropicHost  = "https://api.anthropic.com"
	GoogleHost     = "https://generativelanguage.googleapis.com"
	OpenRouterHost = "https://openrouter.ai"
	ApibostHost    = "https://apibost.com"
	FalHost        = "https://api.fal.ai"
	RunblobHost    = "https://platform.runblob.io"

	// API roots the chat transports post under (chat/completions, messages, generateContent).
	OpenAIAPIBase     = OpenAIHost + "/v1"
	ApibostAPIBase    = ApibostHost + "/v1"
	OpenRouterAPIBase = OpenRouterHost + "/api/v1"
	AnthropicAPIBase  = AnthropicHost
	GeminiAPIBase     = GoogleHost
)
