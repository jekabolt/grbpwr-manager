package entity

// ⚠ MINIMAL STUB written by lane A2 (aiprov pricing) because lane A1's full internal/entity/ai.go
// was not in this tree yet. It holds ONLY the constants lane A2 uses, with the names and values of
// the contract in tmp/plans/ai-providers/06-BRIEFS-A.md; on merge, lane A1's file replaces it whole.

// Provider keys (VARCHAR vocab of ai_provider.provider_key and ai_usage_event.provider_key).
const (
	AIProviderOpenAI     = "openai"
	AIProviderAnthropic  = "anthropic"
	AIProviderGoogle     = "google"
	AIProviderOpenRouter = "openrouter"
	AIProviderApibost    = "apibost"
	AIProviderFal        = "fal"
	AIProviderMeshy      = "meshy"
	AIProviderRunblob    = "runblob"
	AIProviderRecraft    = "recraft"
)

// Cost sources (VARCHAR vocab of ai_usage_event.cost_source) that the price table can answer.
const (
	AICostTable = "table"
	AICostNone  = "none"
)
