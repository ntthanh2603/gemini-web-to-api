package gemini

import (
	"context"
	"testing"

	"gemini-web-to-api/internal/modules/gemini/dto"

	"go.uber.org/zap"
)

func TestStreamContentFinalChunkCarriesUsage(t *testing.T) {
	usage := &dto.UsageMetadata{PromptTokenCount: 3, CandidatesTokenCount: 1, TotalTokenCount: 4}
	resp := &dto.GeminiGenerateResponse{
		Candidates: []dto.Candidate{{
			Content:      dto.Content{Role: "model", Parts: []dto.Part{{Text: "Hi"}}},
			FinishReason: "STOP",
		}},
		UsageMetadata: usage,
	}
	var chunks []dto.GeminiGenerateResponse
	err := (&GeminiService{log: zap.NewNop()}).streamContent(context.Background(), resp, func(chunk dto.GeminiGenerateResponse) bool {
		chunks = append(chunks, chunk)
		return true
	})
	if err != nil {
		t.Fatal(err)
	}

	last := chunks[len(chunks)-1]
	if last.Candidates[0].FinishReason != "STOP" {
		t.Fatalf("last chunk is not the STOP chunk: %+v", last)
	}
	if last.UsageMetadata == nil || *last.UsageMetadata != *usage {
		t.Fatalf("final chunk usageMetadata = %+v, want %+v", last.UsageMetadata, usage)
	}
}
