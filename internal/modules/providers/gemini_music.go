package providers

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"go.uber.org/zap"
)

const (
	// geminiMusicToolMode is inner[49] when the "Create music" tool is selected.
	geminiMusicToolMode = 21
	// MusicModelSuffix selects music generation on any model, e.g. "gemini-pro-music".
	MusicModelSuffix = "-music"

	maxGeneratedAudioBytes = 50 << 20
	musicJobTimeout        = 5 * time.Minute
)

// musicPollInterval is how often the conversation is read while waiting.
var musicPollInterval = defaultVideoPollInterval

// Music lengths and vocal modes, as offered by the Gemini Web music tool.
const (
	MusicLengthShort    = "short"
	MusicLengthStandard = "standard"

	MusicVocalsOn       = "vocals"
	MusicInstrumental   = "instrumental"
	musicVocalsFromText = ""
)

// musicLengthCodes and musicVocalCodes are [code in inner[0][9][6][2], chip ID in inner[55]].
var (
	musicLengthCodes = map[string][2]int{MusicLengthShort: {4, 42}, MusicLengthStandard: {5, 43}}
	musicVocalCodes  = map[string][2]int{MusicInstrumental: {1, 26}, MusicVocalsOn: {2, 27}}
	// MusicGenres maps the genre chips of the music tool to their IDs.
	MusicGenres = map[string]int{
		"pop": 44, "hip-hop": 45, "rock": 46, "k-pop": 40, "latin": 55, "electronic": 47,
		"r&b": 48, "country": 49, "afrobeats": 56, "reggae": 57, "jazz": 50, "classical": 52,
		"folk": 54, "lo-fi": 51, "acoustic": 53, "cinematic": 36, "ambient": 68,
	}
)

// MusicConfig selects Gemini Web's music tool. Empty fields leave the choice
// to Gemini, based on the prompt ("Custom" in the web UI).
type MusicConfig struct {
	Length string
	Vocals string
	Genre  string
}

// Validate normalizes the options and rejects unknown values.
func (m *MusicConfig) Validate() error {
	m.Length = strings.ToLower(strings.TrimSpace(m.Length))
	m.Vocals = strings.ToLower(strings.TrimSpace(m.Vocals))
	m.Genre = strings.ToLower(strings.TrimSpace(m.Genre))
	switch m.Vocals {
	case "vocals on", "vocal", "with vocals":
		m.Vocals = MusicVocalsOn
	case "none", "no vocals":
		m.Vocals = MusicInstrumental
	case "auto", "custom":
		m.Vocals = musicVocalsFromText
	}
	if m.Length == "auto" || m.Length == "custom" {
		m.Length = ""
	}
	if m.Genre == "auto" || m.Genre == "custom" {
		m.Genre = ""
	}
	if _, ok := musicLengthCodes[m.Length]; m.Length != "" && !ok {
		return &MusicValidationError{Message: "length must be short or standard"}
	}
	if _, ok := musicVocalCodes[m.Vocals]; m.Vocals != "" && !ok {
		return &MusicValidationError{Message: "vocals must be vocals or instrumental"}
	}
	if _, ok := MusicGenres[m.Genre]; m.Genre != "" && !ok {
		genres := make([]string, 0, len(MusicGenres))
		for genre := range MusicGenres {
			genres = append(genres, genre)
		}
		return &MusicValidationError{Message: "unsupported genre; use one of: " + strings.Join(genres, ", ")}
	}
	return nil
}

// MusicValidationError is a client error in a music request.
type MusicValidationError struct{ Message string }

func (e *MusicValidationError) Error() string { return e.Message }

// WithMusicGeneration sends the request with the "Create music" tool enabled.
func WithMusicGeneration(config MusicConfig) GenerateOption {
	return func(c *GenerateConfig) { c.Music = &config }
}

// Audio is a generated audio file returned by Gemini Web. URLs are bound to
// the Gemini session and must be downloaded with its cookies.
type Audio struct {
	URL      string  `json:"url"`
	FileName string  `json:"file_name,omitempty"`
	MimeType string  `json:"mime_type,omitempty"`
	Duration float64 `json:"duration,omitempty"`
}

// MusicResult is a completed and downloaded music generation.
type MusicResult struct {
	Audio          Audio
	Data           []byte
	ConversationID string
	Model          string
	Text           string
}

// splitMusicModel strips the music suffix from a requested model name.
func splitMusicModel(model string) (string, bool) {
	trimmed := strings.TrimSpace(model)
	if len(trimmed) > len(MusicModelSuffix) && strings.HasSuffix(strings.ToLower(trimmed), MusicModelSuffix) {
		return trimmed[:len(trimmed)-len(MusicModelSuffix)], true
	}
	return model, false
}

// IsMusicModel reports whether a model name selects music generation.
func IsMusicModel(model string) bool {
	_, music := splitMusicModel(model)
	return music
}

// applyMusicInner selects the music tool and its options, matching the web client:
// inner[0][9][6] = [null, null, [lengthCode, vocalsCode]] and inner[55] = [[chip IDs]].
func applyMusicInner(inner []interface{}, config MusicConfig) []interface{} {
	inner = applyWebClientInner(inner)
	inner[45] = nil // a failed stream is recovered from the saved conversation
	inner[49] = geminiMusicToolMode
	inner[67] = 0

	var codes []interface{}
	var chips []interface{}
	if length, ok := musicLengthCodes[config.Length]; ok {
		codes = append(codes, length[0])
		chips = append(chips, length[1])
	}
	if vocals, ok := musicVocalCodes[config.Vocals]; ok {
		if len(codes) == 0 {
			codes = append(codes, nil)
		}
		codes = append(codes, vocals[0])
		chips = append(chips, vocals[1])
	}
	if genre, ok := MusicGenres[config.Genre]; ok {
		chips = append(chips, genre)
	}
	if len(chips) == 0 {
		return inner
	}

	message, _ := inner[0].([]interface{})
	if len(message) < 10 {
		extended := make([]interface{}, 10)
		copy(extended, message)
		if len(message) < 2 {
			extended[1] = 0
		}
		extended[6] = 0
		message = extended
	}
	options := make([]interface{}, 7)
	if len(codes) > 0 {
		options[6] = []interface{}{nil, nil, codes}
	} else {
		options[6] = []interface{}{}
	}
	message[9] = options
	inner[0] = message
	inner[55] = []interface{}{chips}
	return inner
}

// extractGeneratedAudio finds generated audio cards anywhere in a candidate:
// arrays that carry an "audio/*" MIME type and trusted Google media URLs.
// Arbitrary links in the reply text are never treated as audio.
func extractGeneratedAudio(candidate []interface{}) []Audio {
	var audios []Audio
	seen := make(map[string]bool)
	var walk func(value any, depth int)
	walk = func(value any, depth int) {
		if depth > 12 {
			return
		}
		switch v := value.(type) {
		case map[string]interface{}:
			for _, child := range v {
				walk(child, depth+1)
			}
		case []interface{}:
			if audio, ok := parseAudioItem(v); ok {
				if !seen[audio.URL] {
					seen[audio.URL] = true
					audios = append(audios, audio)
				}
				return
			}
			for _, child := range v {
				walk(child, depth+1)
			}
		}
	}
	// candidate[1] is the reply text; skip it so text links are never used.
	for i, field := range candidate {
		if i != 1 {
			walk(field, 0)
		}
	}
	return audios
}

func parseAudioItem(item []interface{}) (Audio, bool) {
	mimeType := ""
	for _, value := range item {
		if s, ok := value.(string); ok && strings.HasPrefix(s, "audio/") && !strings.Contains(s, " ") {
			mimeType = s
			break
		}
	}
	if mimeType == "" {
		return Audio{}, false
	}
	var urls []string
	var collect func(value any)
	collect = func(value any) {
		switch v := value.(type) {
		case string:
			if trustedMediaURL(v) {
				urls = append(urls, v)
			}
		case []interface{}:
			for _, child := range v {
				collect(child)
			}
		}
	}
	for _, value := range item {
		collect(value)
	}
	if len(urls) == 0 {
		return Audio{}, false
	}
	audio := Audio{URL: urls[0], MimeType: mimeType}
	for _, candidate := range urls {
		// Prefer the authenticated download URL over thumbnails.
		if strings.Contains(candidate, "/download") || strings.Contains(candidate, "usercontent.google.com") {
			audio.URL = candidate
			break
		}
	}
	for _, value := range item {
		if s, ok := value.(string); ok && path.Ext(s) != "" && !strings.Contains(s, "/") && !strings.Contains(s, " ") {
			audio.FileName = s
			break
		}
	}
	return audio, true
}

// classifyMusicReply recognizes replies that mean no music is coming.
func classifyMusicReply(text string) *VideoError {
	if refusal := classifyVideoReply(text); refusal != nil {
		return refusal
	}
	lower := strings.ToLower(text)
	switch {
	case strings.Contains(lower, "unable to synthesize") || strings.Contains(lower, "can't create music") ||
		strings.Contains(lower, "cannot create music") || strings.Contains(lower, "unable to create music"):
		return &VideoError{Code: VideoErrorRefused, Message: "Gemini did not run its music tool for this request", ProviderMessage: safeProviderMessage(text)}
	case strings.Contains(lower, "encountering an error") || strings.Contains(lower, "something went wrong"):
		return &VideoError{Code: VideoErrorUpstream, Message: "Gemini Web reported an error while creating music", ProviderMessage: safeProviderMessage(text)}
	}
	return nil
}

// GenerateMusic submits one music request with the native "Create music" tool,
// waits for the audio (polling the conversation if the stream ends early) and
// downloads it. The prompt is never resubmitted.
func (c *Client) GenerateMusic(ctx context.Context, prompt, model string, config MusicConfig) (*MusicResult, error) {
	if strings.TrimSpace(prompt) == "" {
		return nil, &MusicValidationError{Message: "prompt is required"}
	}
	if err := config.Validate(); err != nil {
		return nil, err
	}
	model, _ = splitMusicModel(model)
	ctx, cancel := context.WithTimeout(ctx, musicJobTimeout)
	defer cancel()

	response, err := c.GenerateContent(ctx, prompt, WithModel(model), WithMusicGeneration(config))
	if err != nil {
		var modelErr *ModelSelectionError
		if errors.As(err, &modelErr) {
			return nil, err
		}
		return nil, &VideoError{Code: VideoErrorUpstream, Message: "Gemini could not start music generation: " + err.Error()}
	}
	result := &MusicResult{ConversationID: response.ConversationID, Model: response.Model, Text: response.Text}
	c.log.Info("Music generation submitted",
		zap.String("conversation_id", result.ConversationID),
		zap.Int("audios", len(response.Audios)),
		zap.String("reply", safeProviderMessage(response.Text)))

	audios := response.Audios
	if len(audios) == 0 {
		if refusal := classifyMusicReply(response.Text); refusal != nil {
			return nil, refusal
		}
		if result.ConversationID == "" {
			return nil, &VideoError{Code: VideoErrorNoVideo, Message: "Gemini returned no music", ProviderMessage: safeProviderMessage(response.Text)}
		}
		audios, err = c.waitForAudio(ctx, result)
		if err != nil {
			return nil, err
		}
	}

	result.Audio = audios[0]
	c.mu.RLock()
	cookieHeader, authUser := c.cookieHeader, c.authUser
	c.mu.RUnlock()
	data, mimeType, err := c.downloadGeneratedAudio(ctx, withAuthUser(result.Audio.URL, authUser), cookieHeader)
	if err != nil {
		return nil, &VideoError{Code: VideoErrorDownload, Message: "Generated music could not be downloaded: " + err.Error() +
			"; it is still available in Gemini Web conversation " + result.ConversationID}
	}
	if result.Audio.MimeType == "" {
		result.Audio.MimeType = mimeType
	}
	result.Data = data
	return result, nil
}

func (c *Client) waitForAudio(ctx context.Context, result *MusicResult) ([]Audio, error) {
	ticker := time.NewTicker(musicPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil, &VideoError{Code: VideoErrorTimeout, Message: "Music was not ready before the deadline; it may still appear in Gemini Web conversation " + result.ConversationID}
		case <-ticker.C:
		}
		candidates, text, err := c.readConversationCandidates(ctx, result.ConversationID)
		if err != nil {
			c.log.Warn("Music status poll failed", zap.String("conversation_id", result.ConversationID), zap.Error(err))
			continue
		}
		if text != "" {
			result.Text = text
		}
		var audios []Audio
		for _, candidate := range candidates {
			audios = append(audios, extractGeneratedAudio(candidate)...)
		}
		if len(audios) > 0 {
			return audios, nil
		}
		if refusal := classifyMusicReply(text); refusal != nil {
			return nil, refusal
		}
	}
}

func (c *Client) downloadGeneratedAudio(ctx context.Context, rawURL, cookieHeader string) ([]byte, string, error) {
	if !trustedMediaURL(rawURL) {
		return nil, "", fmt.Errorf("refusing generated-audio download from untrusted host")
	}
	client := generatedImageHTTPClient(cookieHeader)
	client.Timeout = generatedVideoDownloadTime
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, "", err
	}
	request.Header.Set("User-Agent", DefaultHeaders["User-Agent"])
	request.Header.Set("Referer", "https://gemini.google.com/")
	if cookieHeader != "" {
		request.Header.Set("Cookie", cookieHeader)
	}
	response, err := client.Do(request)
	if err != nil {
		var requestErr *url.Error
		if errors.As(err, &requestErr) {
			err = requestErr.Err
		}
		return nil, "", err
	}
	defer response.Body.Close()
	contentType := response.Header.Get("Content-Type")
	if response.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("download returned HTTP %d", response.StatusCode)
	}
	if strings.HasPrefix(contentType, "text/") || strings.Contains(contentType, "json") {
		return nil, "", fmt.Errorf("download returned %s instead of audio", contentType)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxGeneratedAudioBytes+1))
	if err != nil {
		return nil, "", err
	}
	if len(body) > maxGeneratedAudioBytes {
		return nil, "", fmt.Errorf("generated audio exceeds %d byte limit", maxGeneratedAudioBytes)
	}
	return body, strings.TrimSpace(strings.Split(contentType, ";")[0]), nil
}
