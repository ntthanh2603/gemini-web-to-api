package providers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func modelRPCFixture(t *testing.T, statusCode any, tiers, capabilities []any, entries ...any) string {
	t.Helper()
	status := make([]any, 18)
	status[14], status[15], status[16], status[17] = statusCode, entries, tiers, capabilities
	body, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	record, err := json.Marshal([]any{[]any{"wrb.fr", "otAQ7b", string(body), nil, nil, nil, "generic"}})
	if err != nil {
		t.Fatal(err)
	}
	return string(record)
}

func modelRPCEntry(modelID, category, display string, number any) []any {
	entry := make([]any, 20)
	entry[0], entry[1], entry[11], entry[17] = modelID, category, display, number
	return entry
}

func TestParseGeminiModelsUsesAccountModelIdentity(t *testing.T) {
	flash := modelRPCEntry("account-flash-id", "Fast", "Gemini 3.8 Flash", 1)
	pro := modelRPCEntry("account-pro-id", "Pro", "Gemini 3.1 Pro", 3)
	frame := modelRPCFixture(t, 1000, []any{8}, nil, flash, pro)
	// The actual endpoint includes an XSSI guard, numeric frame lengths, and
	// unrelated RPC frames; a line-based or HTML model-name parser is insufficient.
	fixture := ")]}'\n\n42\n[[\"wrb.fr\",\"other-rpc\",\"[]\"]]\n" + fmt.Sprint(len(frame)) + "\n" + frame + "\n12\n[[\"di\",0]]\n"
	models, err := parseGeminiModels([]byte(fixture))
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 {
		t.Fatalf("expected two discovered models, got %#v", models)
	}
	for _, test := range []struct {
		request, want string
	}{
		{"", "account-flash-id"},
		{"gemini-fast", "account-flash-id"},
		{"gemini-3.8-flash", "account-flash-id"},
		{"gemini-pro", "account-pro-id"},
		{"gemini-advanced", "account-pro-id"},
		{"Gemini 3.1 Pro", "account-pro-id"},
		{"gemini-3.1-pro", "account-pro-id"},
		{"account-pro-id", "account-pro-id"},
	} {
		model, err := resolveGeminiModel(test.request, models)
		if err != nil || model.ModelID != test.want {
			t.Errorf("resolve %q: model=%#v, err=%v; want ID %q", test.request, model, err, test.want)
		}
	}
	if models[1].Capacity != 2 || models[1].CapacityField != 12 || models[1].ModelNumber != 3 {
		t.Fatalf("lost Pro selection metadata: %#v", models[1])
	}
	if models[1].OwnedBy != "google" || models[1].Provider != "gemini" || models[1].Created == 0 {
		t.Fatalf("invalid public model metadata: %#v", models[1].ModelInfo)
	}
}

func TestParseGeminiModelsReadsAccountCapacity(t *testing.T) {
	for _, test := range []struct {
		name                string
		tiers, capabilities []any
		capacity, field     int
	}{
		{"free", nil, nil, 1, 12},
		{"pro tier", []any{8}, nil, 2, 12},
		{"pro capability", nil, []any{19}, 2, 12},
		{"alternate pro tier", []any{16, 8}, nil, 3, 12},
		{"alternate pro capability", nil, []any{106, 19}, 3, 12},
		{"plus precedence", []any{16}, []any{115, 19}, 4, 12},
		{"field 13 first override", []any{21, 22, 16}, []any{115}, 1, 13},
		{"field 13 second override", []any{22, 8}, []any{115}, 2, 13},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := modelRPCFixture(t, 1000, test.tiers, test.capabilities, modelRPCEntry("id", "Pro", "Pro", 3))
			models, err := parseGeminiModels([]byte(fixture))
			if err != nil {
				t.Fatal(err)
			}
			if models[0].Capacity != test.capacity || models[0].CapacityField != test.field {
				t.Fatalf("capacity=%d, field=%d; want %d, %d", models[0].Capacity, models[0].CapacityField, test.capacity, test.field)
			}
		})
	}
}

func TestParseGeminiModelsHandlesAlternateRPCFields(t *testing.T) {
	entry := modelRPCEntry("alternate-id", "", "", nil)
	entry[9], entry[10], entry[19] = 3, "Pro", "Gemini 3 Pro"
	fixture := modelRPCFixture(t, nil, nil, nil, nil, []any{}, []any{123}, entry, entry)
	models, err := parseGeminiModels([]byte(fixture))
	if err != nil || len(models) != 1 {
		t.Fatalf("models=%#v, error=%v", models, err)
	}
	if models[0].ID != "gemini-3-pro" || models[0].ModelNumber != 3 {
		t.Fatalf("did not read fallback fields: %#v", models[0])
	}
	var root any
	if err := json.Unmarshal([]byte(fixture), &root); err != nil {
		t.Fatal(err)
	}
	pretty, _ := json.MarshalIndent(root, "", "  ")
	if _, err := parseGeminiModels(pretty); err != nil {
		t.Fatalf("multiline JSON failed: %v", err)
	}
}

func TestParseGeminiModelsRejectsUnavailableSessions(t *testing.T) {
	for _, status := range []int{1014, 1016, 1021, 1033, 1040, 1042, 1054, 1057, 1060, 9999} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			fixture := modelRPCFixture(t, status, []any{8}, nil,
				modelRPCEntry("flash-id", "Flash", "Flash", 1), modelRPCEntry("pro-id", "Pro", "Pro", 3))
			if models, err := parseGeminiModels([]byte(fixture)); err == nil || len(models) != 0 {
				t.Fatalf("unavailable session advertised selectable models: %#v, %v", models, err)
			}
		})
	}
}

func TestParseGeminiModelsRejectsMissingOrMalformedDiscovery(t *testing.T) {
	for name, fixture := range map[string]string{
		"empty":          "",
		"HTML":           "<html>gemini-pro gemini-flash</html>",
		"wrong RPC":      `[["wrb.fr","another-rpc","[]"]]`,
		"denied RPC":     `[["wrb.fr","otAQ7b",null,null,null,[7]]]`,
		"null body":      `[["wrb.fr","otAQ7b",null]]`,
		"broken body":    `[["wrb.fr","otAQ7b","not json"]]`,
		"missing models": modelRPCFixture(t, 1000, nil, nil),
		"invalid status": modelRPCFixture(t, "1000", nil, nil, modelRPCEntry("id", "Pro", "Pro", 3)),
	} {
		t.Run(name, func(t *testing.T) {
			if models, err := parseGeminiModels([]byte(fixture)); err == nil || len(models) != 0 {
				t.Fatalf("expected discovery error, got %#v, %v", models, err)
			}
		})
	}
}

func TestParseGeminiModelsRejectsInvalidModelNumbers(t *testing.T) {
	for _, number := range []any{0, -1, "3", 3.5, false} {
		fixture := modelRPCFixture(t, 1000, nil, nil, modelRPCEntry("pro-id", "Pro", "Pro", number))
		if models, err := parseGeminiModels([]byte(fixture)); err == nil || len(models) != 0 {
			t.Errorf("invalid model number %v must not be selectable: %#v, %v", number, models, err)
		}
	}
	fixture := modelRPCFixture(t, 1000, nil, nil, modelRPCEntry("flash-id", "Flash", "Flash", nil))
	if models, err := parseGeminiModels([]byte(fixture)); err != nil || len(models) != 1 || models[0].ModelNumber != 1 {
		t.Fatalf("omitted model number must use protocol default: %#v, %v", models, err)
	}
}

func TestResolveGeminiModelNeverFallsBackFromPro(t *testing.T) {
	models, err := parseGeminiModels([]byte(modelRPCFixture(t, 1000, nil, nil,
		modelRPCEntry("flash-id", "Fast", "Gemini 3.8 Flash", 1))))
	if err != nil {
		t.Fatal(err)
	}
	for _, request := range []string{"gemini-advanced", "gemini-pro", "gemini-3-pro-image-preview", "gemini-unknown", "gemini-99-flash"} {
		if got, err := resolveGeminiModel(request, models); err == nil || !strings.Contains(err.Error(), "gemini-3.8-flash") {
			t.Errorf("%q must fail and list available models: got %#v, %v", request, got, err)
		} else {
			var selectionErr *ModelSelectionError
			if !errors.As(err, &selectionErr) {
				t.Errorf("%q returned %T; want ModelSelectionError", request, err)
			}
		}
	}
	if _, err := resolveGeminiModel("", nil); err == nil {
		t.Fatal("empty registry must fail closed")
	}
}

func TestResolveGeminiModelRejectsAmbiguousAliases(t *testing.T) {
	models := []geminiModel{
		{ModelInfo: ModelInfo{ID: "gemini-pro-a"}, ModelID: "first", Aliases: []string{"gemini-pro"}},
		{ModelInfo: ModelInfo{ID: "gemini-pro-b"}, ModelID: "second", Aliases: []string{"gemini-pro"}},
	}
	for _, request := range []string{"gemini-pro", "gemini-advanced"} {
		if _, err := resolveGeminiModel(request, models); err == nil || !strings.Contains(err.Error(), "ambiguous") {
			t.Fatalf("%q must be ambiguous, got %v", request, err)
		}
	}
	if model, err := resolveGeminiModel("first", models); err != nil || model.ModelID != "first" {
		t.Fatalf("exact model ID must disambiguate: %#v, %v", model, err)
	}
}

func TestResolveGeminiModelPrefersCanonicalNamesOverDisplayAliases(t *testing.T) {
	models := []geminiModel{
		{ModelInfo: ModelInfo{ID: "gemini-pro"}, ModelID: "pro-id"},
		{ModelInfo: ModelInfo{ID: "gemini-thinking"}, ModelID: "thinking-id", Aliases: []string{"gemini-pro", "pro-id"}},
	}
	for _, request := range []string{"gemini-pro", "gemini-advanced", "pro-id"} {
		if model, err := resolveGeminiModel(request, models); err != nil || model.ModelID != "pro-id" {
			t.Errorf("canonical selection %q: %#v, %v", request, model, err)
		}
	}
	fixture := modelRPCFixture(t, 1000, nil, nil, modelRPCEntry("thinking-id", "Thinking", "Gemini 3 Pro", 2))
	discovered, err := parseGeminiModels([]byte(fixture))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := resolveGeminiModel("gemini-advanced", discovered); err == nil {
		t.Fatal("display version must not override an explicit non-Pro category")
	}
}

func TestResolveGeminiModelDoesNotInventLegacyAliases(t *testing.T) {
	models := []geminiModel{
		{ModelInfo: ModelInfo{ID: "gemini-pro"}, ModelID: "pro"},
		{ModelInfo: ModelInfo{ID: "gemini-fast"}, ModelID: "flash", Aliases: []string{"gemini-flash"}},
	}
	for _, request := range []string{
		"gemini-3-pro-image-preview",
		"gemini-3-pro-image-preview-11-2025",
		"gemini-3.1-flash-image-preview",
	} {
		if _, err := resolveGeminiModel(request, models); err == nil {
			t.Fatalf("undiscovered legacy alias %q must not be invented", request)
		}
	}

	discovered := geminiModel{
		ModelInfo: ModelInfo{ID: "gemini-image"},
		ModelID:   "dynamic-image-id",
		Aliases:   []string{"gemini-3-pro-image-preview-11-2025"},
	}
	if got, err := resolveGeminiModel("gemini-3-pro-image-preview-11-2025", append(models, discovered)); err != nil || got.ModelID != discovered.ModelID {
		t.Fatalf("alias returned by Gemini must remain selectable: %#v, %v", got, err)
	}
}

type modelDiscoveryTransport func(*http.Request) (*http.Response, error)

func (f modelDiscoveryTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestFetchGeminiModelsSendsAuthenticatedRPC(t *testing.T) {
	fixture := modelRPCFixture(t, 1000, []any{8}, nil, modelRPCEntry("account-pro", "Pro", "Gemini 3 Pro", 3))
	client := &http.Client{Transport: modelDiscoveryTransport(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodPost || request.URL.Scheme+"://"+request.URL.Host+request.URL.Path != EndpointBatchExec {
			t.Fatalf("unexpected endpoint: %s %s", request.Method, request.URL)
		}
		query := request.URL.Query()
		for key, want := range map[string]string{"rpcids": "otAQ7b", "hl": "vi", "rt": "c", "source-path": "/app", "bl": "build-label", "f.sid": "session-id"} {
			if got := query.Get(key); got != want {
				t.Errorf("query %s=%q, want %q", key, got, want)
			}
		}
		if query.Get("_reqid") == "" {
			t.Error("missing request ID")
		}
		if err := request.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if request.PostForm.Get("at") != "secret-token" || request.PostForm.Get("f.req") != `[[["otAQ7b","[]",null,"generic"]]]` {
			t.Error("incorrect RPC form or authentication token")
		}
		for key, want := range map[string]string{"Cookie": "__Secure-1PSID=secret-cookie", "Origin": "https://gemini.google.com", "X-Same-Domain": "1", "x-goog-ext-73010989-jspb": "[0]"} {
			if request.Header.Get(key) != want {
				t.Errorf("incorrect %s header", key)
			}
		}
		var header []any
		if err := json.Unmarshal([]byte(request.Header.Get(geminiModelHeaderKey)), &header); err != nil || len(header) != 17 {
			t.Fatalf("invalid batch selector header: %#v, %v", header, err)
		}
		if header[4] != nil || !reflect.DeepEqual(header[8], []any{float64(4), float64(5), float64(6), float64(8)}) || header[16] != "client-session-id" {
			t.Fatalf("batch selector must omit model and use a separate client UUID: %#v", header)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(fixture)), Header: make(http.Header)}, nil
	})}
	models, err := fetchGeminiModels(context.Background(), client, "secret-token", "__Secure-1PSID=secret-cookie", "build-label", "session-id", "vi", "client-session-id", "")
	if err != nil || len(models) != 1 || models[0].ModelID != "account-pro" {
		t.Fatalf("fetch models=%#v, error=%v", models, err)
	}
}

func TestFetchGeminiModelsUsesConfiguredAccountSlot(t *testing.T) {
	fixture := modelRPCFixture(t, 1000, nil, nil, modelRPCEntry("slot-model", "Flash", "3.8 Flash", 1))
	client := &http.Client{Transport: modelDiscoveryTransport(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/u/2/_/BardChatUi/data/batchexecute" {
			t.Fatalf("request path=%q, want account slot path", request.URL.Path)
		}
		if got := request.URL.Query().Get("source-path"); got != "/u/2/app" {
			t.Fatalf("source-path=%q, want /u/2/app", got)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(fixture)), Header: make(http.Header)}, nil
	})}
	models, err := fetchGeminiModels(context.Background(), client, "token", "cookie=value", "", "", "en", "session", "2")
	if err != nil || len(models) != 1 || models[0].ID != "gemini-3.8-flash" {
		t.Fatalf("models=%#v, error=%v", models, err)
	}
}

func TestGeminiAccountRouting(t *testing.T) {
	for _, test := range []struct {
		authUser, endpoint, sourcePath string
	}{
		{"", EndpointBatchExec, "/app"},
		{"2", "https://gemini.google.com/u/2/_/BardChatUi/data/batchexecute", "/u/2/app"},
	} {
		if got := geminiAccountURL(EndpointBatchExec, test.authUser); got != test.endpoint {
			t.Fatalf("geminiAccountURL(%q)=%q, want %q", test.authUser, got, test.endpoint)
		}
		if got := geminiSourcePath(test.authUser); got != test.sourcePath {
			t.Fatalf("geminiSourcePath(%q)=%q, want %q", test.authUser, got, test.sourcePath)
		}
	}
}

func TestFetchGeminiModelsReportsFailuresWithoutSessionValues(t *testing.T) {
	for _, status := range []int{401, 403, 500} {
		client := &http.Client{Transport: modelDiscoveryTransport(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader("secret-response"))}, nil
		})}
		_, err := fetchGeminiModels(context.Background(), client, "secret-token", "secret-cookie", "", "secret-session", "", "client-session-id", "")
		if err == nil || !strings.Contains(err.Error(), fmt.Sprint(status)) || strings.Contains(err.Error(), "secret") {
			t.Fatalf("HTTP error must expose status only: %v", err)
		}
	}
	client := &http.Client{Transport: modelDiscoveryTransport(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("network unavailable")
	})}
	_, err := fetchGeminiModels(context.Background(), client, "secret-token", "secret-cookie", "", "secret-session", "", "client-session-id", "")
	if err == nil || strings.Contains(err.Error(), "secret") || !strings.Contains(err.Error(), "network unavailable") {
		t.Fatalf("transport error must omit session query: %v", err)
	}
}
