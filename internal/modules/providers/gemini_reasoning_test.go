package providers

import (
	"encoding/json"
	"strings"
	"testing"

	"go.uber.org/zap"
)

// thinkingLabelFrame builds one StreamGenerate frame of the shape Gemini sends
// while it is thinking: a small payload whose only content is the status label
// at payload[2]["7"][5][0], with an empty rc_ text at payload[2]["7"][0].
func thinkingLabelFrame(label string) []interface{} {
	return []interface{}{
		nil,
		[]interface{}{"c_conversation", "r_response"},
		map[string]interface{}{
			"7": []interface{}{"", nil, nil, nil, nil, []interface{}{label}},
		},
	}
}

// thinkingTextFrame builds an empty rc_ text frame: the thinking phase streams
// these between label frames and they carry no answer text.
func thinkingTextFrame(text string) []interface{} {
	return []interface{}{
		nil,
		[]interface{}{"c_conversation", "r_response"},
		map[string]interface{}{
			"7": []interface{}{text},
		},
	}
}

// answerFrame builds a classic candidate frame carrying the final answer.
func answerFrame(text string) []interface{} {
	return []interface{}{
		nil,
		"c_conversation",
		nil,
		nil,
		[]interface{}{
			[]interface{}{"rc_answer", []interface{}{text}},
		},
	}
}

// wireStream renders payloads as the newline-delimited body parseResponse reads.
func wireStream(t *testing.T, payloads ...[]interface{}) string {
	t.Helper()
	lines := make([]string, 0, len(payloads))
	for _, payload := range payloads {
		lines = append(lines, buildWireLine(t, payload))
	}
	return strings.Join(lines, "\n")
}

func TestParseResponseCollectsThinkingStatusLabels(t *testing.T) {
	stream := wireStream(t,
		thinkingLabelFrame("Analyzing travel's impact on finances"),
		thinkingLabelFrame("Analyzing travel's impact on finances"), // repeated frame
		thinkingTextFrame(""),
		thinkingLabelFrame("Analyzing travel's impact on finances"), // repeat after empty frame
		thinkingLabelFrame("Comparing the two itineraries"),
		answerFrame("The cheaper option is the second itinerary."),
	)

	client := &Client{log: zap.NewNop()}
	resp, err := client.parseResponse(stream)
	if err != nil {
		t.Fatalf("parseResponse returned error: %v", err)
	}

	want := "Analyzing travel's impact on finances\nComparing the two itineraries"
	if resp.ReasoningText != want {
		t.Fatalf("ReasoningText = %q, want %q", resp.ReasoningText, want)
	}
	if resp.Text != "The cheaper option is the second itinerary." {
		t.Fatalf("Text = %q, want the streamed answer", resp.Text)
	}
}

func TestParseResponseFallsBackToCtrlMarkersWithoutLabels(t *testing.T) {
	stream := wireStream(t,
		answerFrame("<ctrl94>thought\nWeighing the options\n<ctrl95>The answer is 42."),
	)

	client := &Client{log: zap.NewNop()}
	resp, err := client.parseResponse(stream)
	if err != nil {
		t.Fatalf("parseResponse returned error: %v", err)
	}

	if resp.ReasoningText != "Weighing the options" {
		t.Fatalf("ReasoningText = %q, want the <ctrl94>/<ctrl95> trace", resp.ReasoningText)
	}
	if resp.Text != "The answer is 42." {
		t.Fatalf("Text = %q, want the marker text stripped", resp.Text)
	}
}

func TestParseResponsePrefersLabelsOverCtrlMarkers(t *testing.T) {
	stream := wireStream(t,
		thinkingLabelFrame("Reading the question"),
		answerFrame("<ctrl94>thought\nlegacy trace\n<ctrl95>Answer."),
	)

	client := &Client{log: zap.NewNop()}
	resp, err := client.parseResponse(stream)
	if err != nil {
		t.Fatalf("parseResponse returned error: %v", err)
	}

	if resp.ReasoningText != "Reading the question" {
		t.Fatalf("ReasoningText = %q, want the streamed label", resp.ReasoningText)
	}
	if resp.Text != "Answer." {
		t.Fatalf("Text = %q, want the marker text stripped", resp.Text)
	}
}

func TestParseResponseWithoutThinkingLeavesReasoningEmpty(t *testing.T) {
	stream := wireStream(t, answerFrame("Hello!"))

	client := &Client{log: zap.NewNop()}
	resp, err := client.parseResponse(stream)
	if err != nil {
		t.Fatalf("parseResponse returned error: %v", err)
	}

	if resp.ReasoningText != "" {
		t.Fatalf("ReasoningText = %q, want empty for a non-thinking response", resp.ReasoningText)
	}
	if resp.Text != "Hello!" {
		t.Fatalf("Text = %q, want %q", resp.Text, "Hello!")
	}
}

func TestReasoningLabelsDropsOnlyConsecutiveDuplicates(t *testing.T) {
	var labels reasoningLabels
	for _, label := range []string{
		"  Planning  ",
		"Planning",
		"",
		"Planning", // consecutive duplicate after a blank: dropped
		"Searching",
		"Planning", // non-consecutive repeat: kept, it is a new step
	} {
		labels.add(label)
	}

	want := "Planning\nSearching\nPlanning"
	if got := labels.text(); got != want {
		t.Fatalf("labels = %q, want %q", got, want)
	}
}

func TestExtractReasoningLabelIgnoresFramesWithoutALabel(t *testing.T) {
	tests := map[string][]interface{}{
		"empty payload":        {},
		"classic candidates":   answerFrame("Answer."),
		"rc_ text only":        thinkingTextFrame("hello"),
		"short field list":     {nil, nil, map[string]interface{}{"7": []interface{}{"", nil, nil, nil, nil}}},
		"label block empty":    {nil, nil, map[string]interface{}{"7": []interface{}{"", nil, nil, nil, nil, []interface{}{}}}},
		"label not a string":   {nil, nil, map[string]interface{}{"7": []interface{}{"", nil, nil, nil, nil, []interface{}{42.0}}}},
		"field 7 not an array": {nil, nil, map[string]interface{}{"7": "not-an-array"}},
		"payload[2] is a list": {nil, nil, []interface{}{"x"}},
	}
	for name, payload := range tests {
		if got := extractReasoningLabel(payload); got != "" {
			t.Fatalf("%s: label = %q, want empty", name, got)
		}
	}

	payload := thinkingLabelFrame(" Examining the image ")
	if got := extractReasoningLabel(payload); got != "Examining the image" {
		t.Fatalf("label = %q, want trimmed %q", got, "Examining the image")
	}

	// Sanity-check the synthetic wire shape itself: the fields the extractor
	// reads must survive a JSON round trip, as they do on the real stream.
	raw, err := json.Marshal(thinkingLabelFrame("Label"))
	if err != nil {
		t.Fatal(err)
	}
	var decoded []interface{}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if got := extractReasoningLabel(decoded); got != "Label" {
		t.Fatalf("label after round trip = %q, want %q", got, "Label")
	}
}
