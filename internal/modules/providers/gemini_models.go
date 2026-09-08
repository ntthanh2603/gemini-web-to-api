package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ModelSelectionError reports a model name that cannot be resolved against the
// registry returned by the signed-in Gemini Web account. Callers should expose
// this as a client error (HTTP 400), rather than an upstream service failure.
type ModelSelectionError struct {
	Message string
}

func (e *ModelSelectionError) Error() string { return e.Message }

const (
	geminiModelHeaderKey = "x-goog-ext-525001261-jspb"
	geminiUserStatusRPC  = "otAQ7b"
	maxModelResponseSize = 8 << 20
)

// geminiModel keeps the public name together with the selection data returned
// for this account by GetUserStatus. Public names alone cannot select a model.
type geminiModel struct {
	ModelInfo
	ModelID       string
	Capacity      int
	CapacityField int
	ModelNumber   int
	Aliases       []string
}

var (
	geminiVersionPattern = regexp.MustCompile(`\b(\d+)(?:\.\d+)?\b`)
	geminiSlugSeparator  = regexp.MustCompile(`[^a-z0-9.]+`)
)

func fetchGeminiModels(ctx context.Context, client *http.Client, token, cookieHeader, buildLabel, sessionID, language, generationID, authUser string) ([]geminiModel, error) {
	if client == nil {
		return nil, errors.New("Gemini model discovery requires an HTTP client")
	}
	if token == "" || cookieHeader == "" {
		return nil, errors.New("Gemini model discovery requires an authenticated session")
	}
	if language == "" {
		language = "en"
	}
	query := url.Values{
		"rpcids":      {geminiUserStatusRPC},
		"hl":          {language},
		"_reqid":      {strconv.Itoa(rand.Intn(90000) + 10000)},
		"rt":          {"c"},
		"source-path": {geminiSourcePath(authUser)},
	}
	if buildLabel != "" {
		query.Set("bl", buildLabel)
	}
	if sessionID != "" {
		query.Set("f.sid", sessionID)
	}
	form := url.Values{
		"at":    {token},
		"f.req": {`[[["otAQ7b","[]",null,"generic"]]]`},
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, geminiAccountURL(EndpointBatchExec, authUser)+"?"+query.Encode(), strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("create Gemini model discovery request: %w", err)
	}
	for name, value := range DefaultHeaders {
		request.Header.Set(name, value)
	}
	request.Header.Set("Cookie", cookieHeader)
	batchHeader := make([]any, 17)
	batchHeader[0] = 1
	batchHeader[8] = []int{4, 5, 6, 8}
	batchHeader[16] = generationID
	headerJSON, _ := json.Marshal(batchHeader)
	request.Header.Set(geminiModelHeaderKey, string(headerJSON))
	request.Header.Set("x-goog-ext-73010989-jspb", "[0]")
	response, err := client.Do(request)
	if err != nil {
		// url.Error includes the session query parameters; keep only its cause.
		var requestErr *url.Error
		if errors.As(err, &requestErr) {
			err = requestErr.Err
		}
		return nil, fmt.Errorf("Gemini model discovery request failed: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Gemini model discovery failed with HTTP status %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxModelResponseSize+1))
	if err != nil {
		return nil, fmt.Errorf("read Gemini model discovery response: %w", err)
	}
	if len(body) > maxModelResponseSize {
		return nil, errors.New("Gemini model discovery response is too large")
	}
	return parseGeminiModels(body)
}

// parseGeminiModels accepts both ordinary batchexecute JSON and the length-
// prefixed frames following Google's XSSI guard. Frame lengths are JSON numbers;
// decoding values instead of splitting lines also supports multiline frames.
func parseGeminiModels(body []byte) ([]geminiModel, error) {
	body = bytes.TrimSpace(body)
	body = bytes.TrimPrefix(body, []byte(")]}'"))
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var records [][]any
	for {
		var value any
		if err := decoder.Decode(&value); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, errors.New("invalid Gemini model discovery response")
		}
		collectGeminiModelRecords(value, &records)
	}
	var models []geminiModel
	seen := make(map[string]bool)
	publicNames := make(map[string]bool)
	for _, record := range records {
		if reject, ok := geminiArrayValue(record, 5).([]any); ok {
			if code, ok := geminiInteger(geminiArrayValue(reject, 0)); ok && code != 0 {
				return nil, fmt.Errorf("Gemini model discovery RPC rejected the session (code %d)", code)
			}
		}
		var status []any
		switch value := geminiArrayValue(record, 2).(type) {
		case string:
			bodyDecoder := json.NewDecoder(strings.NewReader(value))
			bodyDecoder.UseNumber()
			if err := bodyDecoder.Decode(&status); err != nil {
				return nil, errors.New("invalid Gemini user status response")
			}
		case []any:
			status = value
		default:
			return nil, errors.New("Gemini model discovery returned no user status")
		}
		if rawStatus := geminiArrayValue(status, 14); rawStatus != nil {
			code, ok := geminiInteger(rawStatus)
			if !ok {
				return nil, errors.New("invalid Gemini account status")
			}
			if code != 1000 {
				return nil, fmt.Errorf("Gemini account cannot select models (status %d); verify access and cookies on Gemini Web", code)
			}
		}
		capacity, capacityField := geminiAccountCapacity(geminiArrayValue(status, 16), geminiArrayValue(status, 17))
		entries, _ := geminiArrayValue(status, 15).([]any)
		for _, entry := range entries {
			fields, ok := entry.([]any)
			if !ok {
				continue
			}
			modelID := geminiFirstString(fields, 0)
			if modelID == "" || seen[modelID] {
				continue
			}
			category := geminiFirstString(fields, 1, 10)
			display := geminiFirstString(fields, 11, 19, 1)
			name, aliases := geminiModelNames(modelID, category, display)
			if publicNames[name] {
				suffix := modelID
				if len(suffix) > 8 {
					suffix = suffix[:8]
				}
				name += "-" + strings.ToLower(suffix)
			}
			rawNumber := geminiArrayValue(fields, 17)
			if rawNumber == nil {
				rawNumber = geminiArrayValue(fields, 9)
			}
			modelNumber := 1 // Protocol default when the RPC omits the field.
			if rawNumber != nil {
				var valid bool
				modelNumber, valid = geminiInteger(rawNumber)
				if !valid || modelNumber <= 0 {
					continue
				}
			}
			models = append(models, geminiModel{
				ModelInfo: ModelInfo{ID: name, DisplayName: display, Created: time.Now().Unix(), OwnedBy: "google", Provider: "gemini"},
				ModelID:   modelID, Capacity: capacity, CapacityField: capacityField,
				ModelNumber: modelNumber, Aliases: aliases,
			})
			seen[modelID] = true
			publicNames[name] = true
		}
	}
	if len(models) == 0 {
		return nil, errors.New("Gemini model discovery returned no available models")
	}
	return models, nil
}

func collectGeminiModelRecords(value any, records *[][]any) {
	items, ok := value.([]any)
	if !ok {
		return
	}
	if len(items) >= 2 {
		marker, _ := items[0].(string)
		rpcID, _ := items[1].(string)
		if marker == "wrb.fr" || marker == "er" {
			if rpcID == geminiUserStatusRPC {
				*records = append(*records, items)
			}
			return
		}
	}
	for _, item := range items {
		collectGeminiModelRecords(item, records)
	}
}

// These flag priorities match Gemini-API's AvailableModel.compute_capacity:
// https://github.com/HanaokaYuzu/Gemini-API/blob/master/src/gemini_webapi/types/availablemodel.py
// The flags, rather than a caller's model name, determine account capacity.
func geminiAccountCapacity(tierValue, capabilityValue any) (int, int) {
	tiers, _ := tierValue.([]any)
	capabilities, _ := capabilityValue.([]any)
	has := func(flags []any, want int) bool {
		for _, flag := range flags {
			if number, ok := geminiInteger(flag); ok && number == want {
				return true
			}
		}
		return false
	}
	switch {
	case has(tiers, 21):
		return 1, 13
	case has(tiers, 22):
		return 2, 13
	case has(capabilities, 115):
		return 4, 12
	case has(tiers, 16) || has(capabilities, 106):
		return 3, 12
	case has(tiers, 8) || has(capabilities, 19):
		return 2, 12
	default:
		return 1, 12
	}
}

func geminiModelNames(modelID, category, display string) (string, []string) {
	aliases := make(map[string]bool)
	add := func(value string) {
		if value = strings.ToLower(strings.TrimSpace(value)); value != "" {
			aliases[value] = true
		}
	}
	slug := func(value string) string {
		value = geminiSlugSeparator.ReplaceAllString(strings.ToLower(strings.TrimSpace(value)), "-")
		return strings.Trim(value, "-")
	}
	prefix := func(value string) string {
		if strings.HasPrefix(value, "gemini-") {
			return value
		}
		return "gemini-" + value
	}
	add(modelID)
	for _, value := range []string{category, display} {
		if value == "" {
			continue
		}
		add(value)
		add(slug(value))
		add(prefix(strings.TrimPrefix(slug(value), "gemini-")))
	}
	if category != "" {
		if version := geminiVersionPattern.FindStringSubmatch(display); len(version) > 1 {
			add("gemini-" + version[0] + "-" + slug(category))
			add("gemini-" + version[1] + "-" + slug(category))
		}
	}
	nameSource := display
	if nameSource == "" {
		nameSource = category
	}
	name := prefix(strings.TrimPrefix(slug(nameSource), "gemini-"))
	if nameSource == "" {
		name = prefix(strings.ToLower(modelID))
	}
	add(name)
	result := make([]string, 0, len(aliases))
	for alias := range aliases {
		result = append(result, alias)
	}
	sort.Strings(result)
	return name, result
}

func resolveGeminiModel(requested string, models []geminiModel) (geminiModel, error) {
	if len(models) == 0 {
		return geminiModel{}, &ModelSelectionError{Message: "no Gemini models available; refresh the authenticated session"}
	}
	requested = strings.ToLower(strings.TrimSpace(requested))
	if requested == "" {
		return models[0], nil
	}
	lookup := requested
	if lookup == "gemini-advanced" {
		lookup = "gemini-pro"
	}
	matching := func(match func(string) bool) []geminiModel {
		var found []geminiModel
		for _, model := range models {
			for _, name := range append([]string{model.ID, model.ModelID}, model.Aliases...) {
				if match(strings.ToLower(name)) {
					found = append(found, model)
					break
				}
			}
		}
		return found
	}
	// Public canonical names and internal IDs take precedence over potentially
	// shared display aliases, so every unambiguous listed model remains usable.
	var matches []geminiModel
	for _, model := range models {
		if strings.EqualFold(model.ID, lookup) || strings.EqualFold(model.ModelID, lookup) {
			matches = append(matches, model)
		}
	}
	if len(matches) == 0 {
		matches = matching(func(name string) bool { return name == lookup })
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	available := make([]string, 0, len(models))
	for _, model := range models {
		available = append(available, model.ID)
	}
	if len(matches) > 1 {
		return geminiModel{}, &ModelSelectionError{Message: fmt.Sprintf("ambiguous Gemini model %q; available models: %s", requested, strings.Join(available, ", "))}
	}
	return geminiModel{}, &ModelSelectionError{Message: fmt.Sprintf("unsupported Gemini model %q; available models: %s", requested, strings.Join(available, ", "))}
}

func geminiArrayValue(values []any, index int) any {
	if index < len(values) {
		return values[index]
	}
	return nil
}

func geminiFirstString(values []any, indexes ...int) string {
	for _, index := range indexes {
		if value, ok := geminiArrayValue(values, index).(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func geminiInteger(value any) (int, bool) {
	switch number := value.(type) {
	case json.Number:
		result, err := strconv.Atoi(string(number))
		return result, err == nil
	case int:
		return number, true
	default:
		return 0, false
	}
}
