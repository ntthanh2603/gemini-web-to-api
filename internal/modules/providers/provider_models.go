package providers

// ModelInfo contains basic information about an AI model
type ModelInfo struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name,omitempty"`
	Created     int64  `json:"created"`
	OwnedBy     string `json:"owned_by"`
	Provider    string `json:"provider"` // "gemini", "claude", etc.
}
