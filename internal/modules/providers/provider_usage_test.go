package providers

import (
	"testing"

	"gemini-web-to-api/internal/commons/utils"
)

func TestResponseCountTokens(t *testing.T) {
	prompt := "User: Hello there"
	text := "General Kenobi"
	reasoning := "The user quoted a film."
	estPrompt := utils.EstimateTokens(prompt)
	estCompletion := utils.EstimateTokens(text) + utils.EstimateTokens(reasoning)

	tests := []struct {
		name  string
		usage *UsageMetadata
		want  TokenCounts
	}{
		{
			name:  "no wire usage estimates everything, reasoning included",
			usage: nil,
			want:  TokenCounts{estPrompt, estCompletion, estPrompt + estCompletion},
		},
		{
			name:  "full wire usage wins",
			usage: &UsageMetadata{PromptTokens: 11, CompletionTokens: 7, TotalTokens: 30},
			want:  TokenCounts{11, 7, 30},
		},
		{
			name:  "missing candidates count falls back to the estimate",
			usage: &UsageMetadata{PromptTokens: 11, TotalTokens: 30},
			want:  TokenCounts{11, estCompletion, 30},
		},
		{
			name:  "missing total is derived from the other counts",
			usage: &UsageMetadata{PromptTokens: 11, CompletionTokens: 7},
			want:  TokenCounts{11, 7, 18},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := &Response{Text: text, ReasoningText: reasoning, Usage: tt.usage}
			if got := resp.CountTokens(prompt); got != tt.want {
				t.Fatalf("CountTokens = %+v, want %+v", got, tt.want)
			}
		})
	}
}
