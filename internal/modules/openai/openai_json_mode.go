package openai

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"gemini-web-to-api/internal/commons/utils"
	"gemini-web-to-api/internal/modules/openai/dto"

	"go.uber.org/zap"
)

// JSON mode (OpenAI response_format) is best-effort: Gemini Web has no JSON
// mode of its own, so the reply shape is asked for in the prompt, the reply is
// validated afterwards, and at most one repair request is spent on it.

// buildJSONModePrompt wraps a chat prompt with the response_format
// instructions. It is the prompt-bridging counterpart of buildToolBridgePrompt.
func buildJSONModePrompt(basePrompt string, schema json.RawMessage) string {
	var b strings.Builder
	b.WriteString("You are an OpenAI-compatible assistant running behind a bridge to Gemini web.\n")
	b.WriteString("Respond with JSON only. Do not use markdown code fences and do not write any text outside the JSON.\n")
	if len(schema) > 0 {
		b.WriteString("The reply must be a single JSON value that matches this JSON Schema:\n")
		b.Write(schema)
		b.WriteString("\nSet every required property.\n")
	} else {
		b.WriteString("The reply must be a single JSON object, for example {\"answer\":\"...\"}.\n")
	}
	b.WriteString("\nConversation:\n")
	b.WriteString(basePrompt)
	return b.String()
}

// repairJSONPrompt asks for the same reply again, saying what was wrong with
// the first one. It is sent at most once per request: every attempt is another
// request on the user's Google account.
func repairJSONPrompt(basePrompt string, schema json.RawMessage, cause error) string {
	var b strings.Builder
	b.WriteString(basePrompt)
	b.WriteString("\n\nYour previous reply could not be used: ")
	b.WriteString(cause.Error())
	b.WriteString(". Reply again with JSON only, with no markdown code fences and no text outside the JSON")
	if len(schema) > 0 {
		b.WriteString(", matching this JSON Schema: ")
		b.Write(schema)
	} else {
		b.WriteString(", as a single JSON object")
	}
	b.WriteString(".")
	return b.String()
}

// extractJSONReply returns the JSON document in a model reply, dropping a
// markdown code fence and any prose around the JSON. It returns "" when the
// reply carries no valid JSON.
func extractJSONReply(text string) string {
	for _, candidate := range jsonReplyCandidates(text) {
		if json.Valid([]byte(candidate)) {
			return candidate
		}
	}
	return ""
}

// jsonReplyCandidates lists the substrings of a reply that could be its JSON,
// most literal first: the whole reply, the reply without its code fence, the
// first balanced object inside either (skipping objects that precede it), and
// escaped variants of all of them, because Gemini often writes raw newlines
// inside JSON strings.
func jsonReplyCandidates(text string) []string {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return nil
	}

	var candidates []string
	for _, raw := range []string{trimmed, utils.StripCodeFence(trimmed)} {
		if raw == "" {
			continue
		}
		candidates = append(candidates, raw)
		for start := strings.Index(raw, "{"); start >= 0; {
			if obj := utils.ExtractFirstJSONObject(raw[start:]); obj != "" {
				candidates = append(candidates, obj)
			}
			next := strings.Index(raw[start+1:], "{")
			if next < 0 {
				break
			}
			start += next + 1
		}
	}

	escaped := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		escaped = append(escaped, utils.EscapeJSONStringControlChars(candidate))
	}
	return append(candidates, escaped...)
}

// jsonSchemaShape is the part of a JSON Schema that response_format checks: the
// top-level type and the required properties. This is deliberately not a JSON
// Schema implementation.
type jsonSchemaShape struct {
	Type     string   `json:"type"`
	Required []string `json:"required"`
}

// validateJSONReply checks a reply against response_format and returns the
// cleaned JSON that should reach the client. json_object needs a JSON object;
// json_schema also needs the requested top-level type and required keys, but
// nothing deeper.
func validateJSONReply(text string, schema json.RawMessage) (string, error) {
	cleaned := extractJSONReply(text)
	if cleaned == "" {
		return "", errors.New("the reply is not valid JSON")
	}
	actual := jsonTypeOf(cleaned)
	if len(schema) == 0 {
		if actual != "object" {
			return "", fmt.Errorf("the reply is %s, not a JSON object", jsonTypeName(actual))
		}
		return cleaned, nil
	}

	var shape jsonSchemaShape
	if err := json.Unmarshal(schema, &shape); err != nil {
		// A schema this bridge cannot read is not a reason to reject the reply.
		return cleaned, nil
	}
	if want := strings.ToLower(strings.TrimSpace(shape.Type)); want != "" && !jsonTypeMatches(want, actual) {
		return "", fmt.Errorf("the reply is %s, want %s", jsonTypeName(actual), want)
	}
	if len(shape.Required) == 0 {
		return cleaned, nil
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(cleaned), &obj); err != nil {
		return "", fmt.Errorf("the reply is %s, want a JSON object with %s",
			jsonTypeName(actual), strings.Join(shape.Required, ", "))
	}
	for _, key := range shape.Required {
		if _, ok := obj[key]; !ok {
			return "", fmt.Errorf("the reply is missing required key %q", key)
		}
	}
	return cleaned, nil
}

// jsonTypeOf names the JSON type of a document: object, array, string, number,
// integer, boolean or null. It returns "" when doc is not JSON.
func jsonTypeOf(doc string) string {
	dec := json.NewDecoder(strings.NewReader(doc))
	dec.UseNumber()
	var value any
	if err := dec.Decode(&value); err != nil {
		return ""
	}
	switch v := value.(type) {
	case map[string]any:
		return "object"
	case []any:
		return "array"
	case string:
		return "string"
	case bool:
		return "boolean"
	case nil:
		return "null"
	case json.Number:
		if strings.ContainsAny(v.String(), ".eE") {
			return "number"
		}
		return "integer"
	}
	return ""
}

func jsonTypeName(name string) string {
	if name == "" {
		return "not JSON"
	}
	return "a JSON " + name
}

// jsonTypeMatches reports whether an actual JSON type satisfies a schema type:
// an integer also satisfies "number", never the other way round.
func jsonTypeMatches(want, actual string) bool {
	if want == actual {
		return true
	}
	return want == "number" && actual == "integer"
}

// ensureJSONReply validates a reply against response_format and, when it does
// not match, asks Gemini once to repair it through regenerate (so a tool-bridge
// request is repaired through the tool bridge). A repair is another request on
// the user's Google account, so a reply that is still invalid afterwards is
// returned raw, with a warning, instead of being retried again.
func (s *OpenAIService) ensureJSONReply(req dto.ChatCompletionRequest, prompt, text string, schema json.RawMessage, regenerate func(string) (string, error)) string {
	cleaned, err := validateJSONReply(text, schema)
	if err == nil {
		return cleaned
	}
	s.log.Debug("response_format reply was not valid JSON",
		zap.String("model", req.Model), zap.Error(err))

	repaired, regenErr := regenerate(repairJSONPrompt(prompt, schema, err))
	if regenErr != nil {
		s.log.Warn("response_format repair request failed; returning the raw reply",
			zap.String("model", req.Model), zap.Error(regenErr))
		return text
	}

	cleaned, retryErr := validateJSONReply(repaired, schema)
	if retryErr != nil {
		s.log.Warn("Gemini reply still does not match response_format after one repair attempt; returning the raw reply",
			zap.String("model", req.Model), zap.Error(retryErr))
		return text
	}
	return cleaned
}
