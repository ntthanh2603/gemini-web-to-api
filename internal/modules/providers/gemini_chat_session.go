package providers

import "context"

// GeminiChatSession implements ChatSession interface for Gemini.
type GeminiChatSession struct {
	client   *Client
	model    string
	metadata *SessionMetadata
	history  []Message
}

// SendMessage sends a message in the chat session.
func (s *GeminiChatSession) SendMessage(ctx context.Context, message string, options ...GenerateOption) (*Response, error) {
	// Apply the session model before per-message options so an explicit override
	// still works, and share the same model selection and upload path as the APIs.
	opts := append([]GenerateOption{WithModel(s.model)}, options...)
	response, err := s.client.generateContent(ctx, message, s.buildMetadata(), opts...)
	if err != nil {
		return nil, err
	}

	if response.Metadata != nil {
		if cid, ok := response.Metadata["cid"].(string); ok && cid != "" {
			if s.metadata == nil {
				s.metadata = &SessionMetadata{}
			}
			s.metadata.ConversationID = cid
		}
		if rid, ok := response.Metadata["rid"].(string); ok && rid != "" {
			if s.metadata == nil {
				s.metadata = &SessionMetadata{}
			}
			s.metadata.ResponseID = rid
		}
		if rcid, ok := response.Metadata["rcid"].(string); ok && rcid != "" {
			if s.metadata == nil {
				s.metadata = &SessionMetadata{}
			}
			s.metadata.ChoiceID = rcid
		}
	}

	s.history = append(s.history, Message{
		Role:    "user",
		Content: message,
	})
	s.history = append(s.history, Message{
		Role:    "model",
		Content: response.Text,
	})

	return response, nil
}

// GetMetadata returns session metadata.
func (s *GeminiChatSession) GetMetadata() *SessionMetadata {
	if s.metadata == nil {
		return &SessionMetadata{
			Model: s.model,
		}
	}
	s.metadata.Model = s.model
	return s.metadata
}

// GetHistory returns conversation history.
func (s *GeminiChatSession) GetHistory() []Message {
	return s.history
}

// Clear clears the conversation history.
func (s *GeminiChatSession) Clear() {
	s.history = []Message{}
	s.metadata = nil
}

func (s *GeminiChatSession) buildMetadata() []interface{} {
	if s.metadata == nil {
		return []interface{}{nil, nil, nil}
	}

	return []interface{}{
		s.metadata.ConversationID,
		s.metadata.ResponseID,
		s.metadata.ChoiceID,
	}
}
