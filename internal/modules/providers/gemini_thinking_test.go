package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"go.uber.org/zap"
)

func TestGenerateContentSendsThinkingLevel(t *testing.T) {
	for _, capacityField := range []int{12, 13} {
		for _, tc := range []struct {
			level string
			wire  int
		}{
			{"", 1}, {"low", 1}, {"medium", 4}, {"high", 2}, {" HIGH ", 2},
		} {
			t.Run(fmt.Sprintf("%s/capacity=%d", tc.level, capacityField), func(t *testing.T) {
				model := generationTestModels()[0]
				model.CapacityField = capacityField
				client := &Client{at: "test-token", cachedModels: []geminiModel{model}, log: zap.NewNop()}
				requests := 0
				stubGenerationTransport(t, func(r *http.Request, inner []interface{}) {
					requests++
					var header []interface{}
					if err := json.Unmarshal([]byte(r.Header.Get(geminiModelHeaderKey)), &header); err != nil {
						t.Fatal(err)
					}
					levelIndex := 15 + capacityField - 12
					if header[levelIndex] != float64(tc.wire) {
						t.Fatalf("thinking level %q: header=%v, want %d", tc.level, header[levelIndex], tc.wire)
					}
					// The captured processSession message uses protobuf field 81
					// (JSPB index 80) for the same level: medium is 4 in both.
					if inner[80] != float64(tc.wire) {
						t.Fatalf("thinking level %q: body=%v, want %d", tc.level, inner[80], tc.wire)
					}
					if header[4] != model.ModelID || inner[79] != float64(model.ModelNumber) {
						t.Fatal("thinking level changed the selected model")
					}
				})
				response, err := client.GenerateContent(context.Background(), "Hello", WithModel(model.ID), WithThinkingLevel(tc.level))
				if err != nil || response.Text != "ok" || requests != 1 {
					t.Fatalf("generation failed: requests=%d error=%v", requests, err)
				}
			})
		}
	}
}

func TestGenerateContentRejectsUnsupportedThinkingLevelBeforeSending(t *testing.T) {
	stubGenerationTransport(t, func(*http.Request, []interface{}) { t.Fatal("invalid level reached Google") })
	client := &Client{at: "test-token", cachedModels: generationTestModels(), log: zap.NewNop()}
	for _, level := range []string{"none", "minimal", "xhigh", "deep_think", "3", "invalid"} {
		_, err := client.GenerateContent(context.Background(), "Hello", WithThinkingLevel(level))
		if err == nil || !strings.Contains(err.Error(), "thinking level") {
			t.Fatalf("level %q should fail validation, got %v", level, err)
		}
	}
}
