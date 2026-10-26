package dto

import (
	"encoding/json"
	"testing"
)

const toolsCamel = `{
	"contents":[{"role":"user","parts":[{"text":"call ping"}]}],
	"tools":[{"functionDeclarations":[{"name":"ping","description":"ping tool","parameters":{"type":"object","properties":{"a":{"type":"string"}}}}]}],
	"toolConfig":{"functionCallingConfig":{"mode":"ANY","allowedFunctionNames":["ping"]}}
}`

const toolsSnake = `{
	"contents":[{"role":"user","parts":[{"text":"call ping"}]}],
	"tools":[{"function_declarations":[{"name":"ping","description":"ping tool","parameters":{"type":"object","properties":{"a":{"type":"string"}}}}]}],
	"tool_config":{"function_calling_config":{"mode":"ANY","allowed_function_names":["ping"]}}
}`

func TestGeminiGenerateRequestReadsBothToolSpellings(t *testing.T) {
	for name, body := range map[string]string{"camelCase": toolsCamel, "snake_case": toolsSnake} {
		t.Run(name, func(t *testing.T) {
			var req GeminiGenerateRequest
			if err := json.Unmarshal([]byte(body), &req); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(req.Tools) != 1 {
				t.Fatalf("expected 1 tool, got %d", len(req.Tools))
			}
			declarations := req.Tools[0].FunctionDeclarations
			if len(declarations) != 1 {
				t.Fatalf("expected 1 function declaration, got %d", len(declarations))
			}
			if declarations[0].Name != "ping" {
				t.Errorf("got name %q, want %q", declarations[0].Name, "ping")
			}
			if declarations[0].Description != "ping tool" {
				t.Errorf("got description %q, want %q", declarations[0].Description, "ping tool")
			}
			if len(declarations[0].Parameters) == 0 {
				t.Error("expected parameters to be read")
			}
			if req.ToolConfig == nil || req.ToolConfig.FunctionCallingConfig == nil {
				t.Fatal("expected tool config to be read")
			}
			config := req.ToolConfig.FunctionCallingConfig
			if config.Mode != "ANY" {
				t.Errorf("got mode %q, want %q", config.Mode, "ANY")
			}
			if len(config.AllowedFunctionNames) != 1 || config.AllowedFunctionNames[0] != "ping" {
				t.Errorf("got allowed names %v, want [ping]", config.AllowedFunctionNames)
			}
		})
	}
}

func TestFunctionDeclarationFallsBackToJSONSchema(t *testing.T) {
	for name, body := range map[string]string{
		"camelCase":  `{"name":"ping","parametersJsonSchema":{"type":"object","properties":{"a":{"type":"string"}}}}`,
		"snake_case": `{"name":"ping","parameters_json_schema":{"type":"object","properties":{"a":{"type":"string"}}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			var declaration FunctionDeclaration
			if err := json.Unmarshal([]byte(body), &declaration); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(declaration.Parameters) == 0 {
				t.Fatal("expected parametersJsonSchema to fill parameters")
			}
		})
	}
}

func TestToolMarshalsWithSnakeCase(t *testing.T) {
	tool := Tool{
		FunctionDeclarations: []FunctionDeclaration{{Name: "ping"}},
	}
	encoded, err := json.Marshal(tool)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &raw); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := raw["function_declarations"]; !ok {
		t.Errorf("expected function_declarations in output, got %s", encoded)
	}
}
