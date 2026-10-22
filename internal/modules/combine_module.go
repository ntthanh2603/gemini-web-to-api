package modules

import (
	"gemini-web-to-api/internal/modules/admin"
	"gemini-web-to-api/internal/modules/claude"
	"gemini-web-to-api/internal/modules/gemini"
	"gemini-web-to-api/internal/modules/openai"
	"gemini-web-to-api/internal/modules/providers"
	"go.uber.org/fx"
)

var Module = fx.Options(
	gemini.Module,
	claude.Module,
	openai.Module,
	providers.Module,
	admin.Module,
)
