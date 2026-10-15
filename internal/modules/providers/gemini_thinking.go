package providers

import (
	"fmt"
	"strings"
)

// ParseThinkingLevel maps the public effort names to the Gemini Web header and body enum.
// The three values match the user's 2026-10-15 Gemini 3.6 Flash captures.
// Value 3 is asynchronous Deep Think, not the high setting.
func ParseThinkingLevel(level string) (int, error) {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "", "low":
		return 1, nil
	case "medium":
		return 4, nil
	case "high":
		return 2, nil
	default:
		return 0, fmt.Errorf("unsupported thinking level %q: use low, medium, or high", level)
	}
}

// WithThinkingLevel selects the Gemini Web thinking level for one request.
func WithThinkingLevel(level string) GenerateOption {
	return func(c *GenerateConfig) {
		c.ThinkingLevel = level
	}
}
