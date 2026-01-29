package handlers

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"ai-bridges/internal/models"

	"go.uber.org/zap"
)

// buildPromptFromMessages constructs a unified prompt from messages
func buildPromptFromMessages(messages []models.Message, systemPrompt string) string {
	var promptBuilder strings.Builder

	if systemPrompt != "" {
		promptBuilder.WriteString(fmt.Sprintf("System: %s\n\n", systemPrompt))
	}

	for _, msg := range messages {
		role := "User"
		if strings.EqualFold(msg.Role, "assistant") || strings.EqualFold(msg.Role, "model") {
			role = "Model"
		} else if strings.EqualFold(msg.Role, "system") {
			role = "System"
		}
		promptBuilder.WriteString(fmt.Sprintf("%s: %s\n", role, msg.Content))
	}

	return strings.TrimSpace(promptBuilder.String())
}

// validateMessages validates that messages array is not empty and not all empty
func validateMessages(messages []models.Message) error {
	if len(messages) == 0 {
		return fmt.Errorf("messages array cannot be empty")
	}

	allEmpty := true
	for _, msg := range messages {
		if strings.TrimSpace(msg.Content) != "" {
			allEmpty = false
			break
		}
	}

	if allEmpty {
		return fmt.Errorf("all messages have empty content")
	}

	return nil
}

// validateGenerationRequest validates common generation request parameters
func validateGenerationRequest(model string, maxTokens int, temperature float32) error {
	if maxTokens < 0 {
		return fmt.Errorf("max_tokens must be non-negative")
	}

	if temperature < 0 || temperature > 2 {
		return fmt.Errorf("temperature must be between 0 and 2")
	}

	return nil
}

// marshalJSONSafely marshals JSON and logs errors instead of silently failing
func marshalJSONSafely(log *zap.Logger, v interface{}) []byte {
	data, err := json.Marshal(v)
	if err != nil {
		log.Error("Failed to marshal JSON", zap.Error(err), zap.Any("value", v))
		return []byte("{}")
	}
	return data
}

// sendStreamChunk writes a JSON chunk to the stream writer with error handling
func sendStreamChunk(w *bufio.Writer, log *zap.Logger, chunk interface{}) error {
	data := marshalJSONSafely(log, chunk)
	if _, err := w.Write(data); err != nil {
		log.Error("Failed to write chunk", zap.Error(err))
		return err
	}
	if _, err := w.Write([]byte("\n")); err != nil {
		log.Error("Failed to write newline", zap.Error(err))
		return err
	}
	if err := w.Flush(); err != nil {
		log.Error("Failed to flush writer", zap.Error(err))
		return err
	}
	return nil
}

// sendSSEChunk writes a Server-Sent Event chunk
func sendSSEChunk(w *bufio.Writer, log *zap.Logger, event string, chunk interface{}) error {
	data := marshalJSONSafely(log, chunk)
	if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, string(data)); err != nil {
		log.Error("Failed to write SSE chunk", zap.Error(err))
		return err
	}
	if err := w.Flush(); err != nil {
		log.Error("Failed to flush SSE writer", zap.Error(err))
		return err
	}
	return nil
}

// splitResponseIntoChunks simulates streaming by splitting response into chunks
func splitResponseIntoChunks(text string, delayMs int) []string {
	words := strings.Split(text, " ")
	var chunks []string
	for i, word := range words {
		content := word
		if i < len(words)-1 {
			content += " "
		}
		chunks = append(chunks, content)
	}
	return chunks
}

// sleepWithCancel sleeps for the specified duration or until context is cancelled
func sleepWithCancel(ctx context.Context, duration time.Duration) bool {
	select {
	case <-time.After(duration):
		return true
	case <-ctx.Done():
		return false
	}
}

// errorToResponse converts an error to a standardized error response
func errorToResponse(err error, errorType string) models.ErrorResponse {
	return models.ErrorResponse{
		Error: models.Error{
			Message: err.Error(),
			Type:    errorType,
		},
	}
}
