package providers

import "gemini-web-to-api/internal/commons/utils"

// TokenCounts holds the token usage reported to API clients for one response.
type TokenCounts struct {
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
}

// CountTokens returns the token usage of this response to prompt. Completion
// covers everything the model generated, reasoning included, as OpenAI and
// Anthropic count it.
//
// Token-count priority, highest first, per count:
//  1. Real usageMetadata from the wire (when Google sends it). A count the
//     wire leaves out falls back below instead of being reported as zero.
//  2. An exact tokenizer, when configured (GEMINI_TOKENIZER_MODEL; future).
//  3. The utils.EstimateTokens heuristic, always available with zero config.
func (r *Response) CountTokens(prompt string) TokenCounts {
	var wire UsageMetadata
	if r.Usage != nil {
		wire = *r.Usage
	}
	counts := TokenCounts{
		PromptTokens:     int(wire.PromptTokens),
		CompletionTokens: int(wire.CompletionTokens),
		TotalTokens:      int(wire.TotalTokens),
	}
	if counts.PromptTokens == 0 {
		counts.PromptTokens = utils.EstimateTokens(prompt)
	}
	if counts.CompletionTokens == 0 {
		counts.CompletionTokens = utils.EstimateTokens(r.Text) + utils.EstimateTokens(r.ReasoningText)
	}
	if counts.TotalTokens == 0 {
		counts.TotalTokens = counts.PromptTokens + counts.CompletionTokens
	}
	return counts
}
