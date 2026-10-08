package providers

import (
	"bytes"
	"context"
	"encoding/binary"
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
	"sync"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"
)

// VideoAspectRatio is the aspect-ratio value Gemini Web sends from the
// /videos page (inner[0][9][6][0][3]).
type VideoAspectRatio int

const (
	VideoAspectLandscape VideoAspectRatio = 1 // 16:9, 1280x720
	VideoAspectPortrait  VideoAspectRatio = 2 // 9:16, 720x1280
)

const (
	geminiReadConversationRPC = "hNvQHb"
	// geminiVideoToolMode is inner[49] when the "Videos" tool is selected.
	geminiVideoToolMode = 11
	// geminiVideoCardField is the one-based JSPB field of candidate[12] that
	// holds the generated-video card.
	geminiVideoCardField = 60

	maxGeneratedVideoBytes     = 100 << 20
	maxConversationReadBytes   = 32 << 20
	defaultVideoPollInterval   = 10 * time.Second
	generatedVideoDownloadTime = 5 * time.Minute
)

// VideoConfig selects Gemini Web's native video-generation tool.
type VideoConfig struct {
	AspectRatio VideoAspectRatio
}

// WithVideoGeneration sends the request with the "Videos" tool enabled.
func WithVideoGeneration(aspect VideoAspectRatio) GenerateOption {
	return func(c *GenerateConfig) {
		if aspect != VideoAspectPortrait {
			aspect = VideoAspectLandscape
		}
		c.Video = &VideoConfig{AspectRatio: aspect}
	}
}

// Video is a generated video card returned by Gemini Web. URLs are bound to
// the Gemini session and must be downloaded with its cookies.
type Video struct {
	URL          string `json:"url"`
	ThumbnailURL string `json:"thumbnail_url,omitempty"`
	FileName     string `json:"file_name,omitempty"`
	MimeType     string `json:"mime_type,omitempty"`
	Width        int     `json:"width,omitempty"`
	Height       int     `json:"height,omitempty"`
	Duration     float64 `json:"duration,omitempty"` // seconds
	SizeBytes    int64   `json:"size_bytes,omitempty"`
}

// VideoResult is a completed and downloaded video generation.
type VideoResult struct {
	Video          Video
	Data           []byte
	ConversationID string
	Model          string
	Text           string
}

// VideoError codes returned by GenerateVideo.
const (
	VideoErrorQuota    = "quota_exceeded"
	VideoErrorRefused  = "refused"
	VideoErrorTimeout  = "timeout"
	VideoErrorNoVideo  = "no_video"
	VideoErrorUpstream = "upstream_error"
	VideoErrorDownload = "download_failed"
)

// VideoError is a categorized video-generation failure.
type VideoError struct {
	Code            string `json:"code"`
	Message         string `json:"message"`
	ProviderMessage string `json:"provider_message,omitempty"`
}

func (e *VideoError) Error() string { return e.Message }

var (
	videoPlaceholderRegex = regexp.MustCompile(`https?://googleusercontent\.com/generated_video_content/\d+`)
	videoLinkRegex        = regexp.MustCompile(`https?://\S+`)
)

// applyVideoInner turns a normal StreamGenerate payload into the payload the
// web client sends from the /videos page.
func applyVideoInner(inner []interface{}, aspect VideoAspectRatio) []interface{} {
	inner = applyWebClientInner(inner)
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
	videoOptions := make([]interface{}, 7)
	videoOptions[6] = []interface{}{[]interface{}{nil, nil, nil, int(aspect)}}
	message[9] = videoOptions
	inner[0] = message

	toolID := 16
	if aspect == VideoAspectPortrait {
		toolID = 17
	}
	inner[45] = nil // completion is read back from the saved conversation
	inner[49] = geminiVideoToolMode
	inner[55] = []interface{}{[]interface{}{toolID}}
	return inner
}

// applyWebToolModelHeader advertises web-client tool features (video, canvas)
// in the model header, as gemini.google.com does.
func applyWebToolModelHeader(headers map[string]string) error {
	var header []interface{}
	if err := json.Unmarshal([]byte(headers[geminiModelHeaderKey]), &header); err != nil || len(header) < 9 {
		return fmt.Errorf("invalid Gemini model header")
	}
	header[8] = []int{4, 5, 6, 8, 16}
	encoded, err := json.Marshal(header)
	if err != nil {
		return err
	}
	headers[geminiModelHeaderKey] = string(encoded)
	return nil
}

// jspbField reads a zero-based field from a JSPB array, including fields
// stored in a trailing sparse map keyed by one-based field numbers.
func jspbField(value any, index int) any {
	a, ok := value.([]interface{})
	if !ok || index < 0 {
		return nil
	}
	if index < len(a) {
		if _, sparse := a[index].(map[string]interface{}); !sparse && a[index] != nil {
			return a[index]
		}
	}
	if len(a) > 0 {
		if sparse, ok := a[len(a)-1].(map[string]interface{}); ok {
			return sparse[strconv.Itoa(index+1)]
		}
	}
	return nil
}

// extractGeneratedVideos reads the structured generated-video card; arbitrary
// links in the text are never treated as generated videos.
func extractGeneratedVideos(candidate []interface{}) []Video {
	card := jspbField(jspbField(candidate, 12), geminiVideoCardField-1)
	if card == nil {
		return nil
	}
	var videos []Video
	seen := make(map[string]bool)
	var walk func(value any)
	walk = func(value any) {
		item, ok := value.([]interface{})
		if !ok {
			return
		}
		if video, ok := parseVideoItem(item); ok {
			if !seen[video.URL] {
				seen[video.URL] = true
				videos = append(videos, video)
			}
			return
		}
		for _, child := range item {
			walk(child)
		}
	}
	walk(card)
	return videos
}

// parseVideoItem recognizes [null,2,"video.mp4",…,[thumb,download,…],…,"video/mp4",…,[[…],w,h],…,size].
func parseVideoItem(item []interface{}) (Video, bool) {
	if len(item) < 8 {
		return Video{}, false
	}
	mimeType := ""
	for _, value := range item {
		if s, ok := value.(string); ok && strings.HasPrefix(s, "video/") {
			mimeType = s
			break
		}
	}
	urls, ok := item[7].([]interface{})
	if mimeType == "" || !ok || len(urls) < 2 {
		return Video{}, false
	}
	downloadURL, _ := urls[1].(string)
	if !trustedMediaURL(downloadURL) {
		return Video{}, false
	}
	video := Video{URL: downloadURL, MimeType: mimeType}
	if thumbnail, _ := urls[0].(string); trustedMediaURL(thumbnail) {
		video.ThumbnailURL = thumbnail
	}
	video.FileName, _ = item[2].(string)
	for _, value := range item[8:] {
		switch v := value.(type) {
		case []interface{}:
			// [[seconds, nanos], width, height]
			if len(v) == 3 {
				if duration, isArray := v[0].([]interface{}); isArray {
					width, okW := v[1].(float64)
					height, okH := v[2].(float64)
					if okW && okH {
						video.Width, video.Height = int(width), int(height)
					}
					if len(duration) == 2 {
						seconds, _ := duration[0].(float64)
						nanos, _ := duration[1].(float64)
						video.Duration = seconds + nanos/1e9
					}
				}
			}
		case float64:
			video.SizeBytes = int64(v)
		}
	}
	return video, true
}

func trustedMediaURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.User == nil && isTrustedGoogleMediaHost(u.Hostname())
}

// conversationIDFromPayload accepts both "c_…" and ["c_…","r_…"] layouts.
func conversationIDFromPayload(payload []interface{}) string {
	if len(payload) < 2 {
		return ""
	}
	switch v := payload[1].(type) {
	case string:
		return v
	case []interface{}:
		if len(v) > 0 {
			id, _ := v[0].(string)
			return id
		}
	}
	return ""
}

func stripVideoPlaceholders(text string) string {
	return strings.TrimSpace(videoPlaceholderRegex.ReplaceAllString(text, ""))
}

// safeProviderMessage removes links (which may be signed) and bounds the size.
func safeProviderMessage(text string) string {
	text = strings.TrimSpace(videoLinkRegex.ReplaceAllString(text, ""))
	if runes := []rune(text); len(runes) > 500 {
		text = string(runes[:500])
	}
	return text
}

// classifyVideoReply recognizes replies that mean no video is coming.
func classifyVideoReply(text string) *VideoError {
	lower := strings.ToLower(text)
	switch {
	case strings.Contains(lower, "limit resets") || strings.Contains(lower, "reached your limit") ||
		strings.Contains(lower, "video limit") || strings.Contains(lower, "usage limit"):
		return &VideoError{Code: VideoErrorQuota, Message: "Gemini video generation limit reached; wait for the limit to reset", ProviderMessage: safeProviderMessage(text)}
	case strings.Contains(lower, "can't generate") || strings.Contains(lower, "cannot generate") ||
		strings.Contains(lower, "can't create") || strings.Contains(lower, "unable to generate") ||
		strings.Contains(lower, "can't help with"):
		return &VideoError{Code: VideoErrorRefused, Message: "Gemini declined to generate this video", ProviderMessage: safeProviderMessage(text)}
	}
	return nil
}

// GenerateVideo submits one video request with the native "Videos" tool, then
// polls the same conversation until the video card appears and downloads it.
// The prompt is never resubmitted, so one call consumes at most one quota unit.
func (c *Client) GenerateVideo(ctx context.Context, prompt, model string, aspect VideoAspectRatio, pollInterval time.Duration) (*VideoResult, error) {
	if pollInterval <= 0 {
		pollInterval = defaultVideoPollInterval
	}
	response, err := c.GenerateContent(ctx, prompt, WithModel(model), WithVideoGeneration(aspect))
	if err != nil {
		var modelErr *ModelSelectionError
		if errors.As(err, &modelErr) {
			return nil, err
		}
		return nil, &VideoError{Code: VideoErrorUpstream, Message: "Gemini could not start video generation: " + err.Error()}
	}
	result := &VideoResult{ConversationID: response.ConversationID, Model: response.Model, Text: stripVideoPlaceholders(response.Text)}
	c.log.Info("Video generation submitted",
		zap.String("conversation_id", result.ConversationID),
		zap.Int("videos", len(response.Videos)),
		zap.String("reply", safeProviderMessage(response.Text)))

	videos := response.Videos
	if len(videos) == 0 {
		if refusal := classifyVideoReply(response.Text); refusal != nil {
			return nil, refusal
		}
		if result.ConversationID == "" {
			return nil, &VideoError{Code: VideoErrorNoVideo, Message: "Gemini returned neither a video nor a conversation to poll", ProviderMessage: safeProviderMessage(response.Text)}
		}
		videos, err = c.waitForVideo(ctx, result, pollInterval)
		if err != nil {
			return nil, err
		}
	}

	result.Video = videos[0]
	c.mu.RLock()
	cookieHeader, authUser := c.cookieHeader, c.authUser
	c.mu.RUnlock()
	data, err := c.downloadGeneratedVideo(ctx, withAuthUser(result.Video.URL, authUser), cookieHeader)
	if err != nil {
		return nil, &VideoError{Code: VideoErrorDownload, Message: "Generated video could not be downloaded: " + err.Error() +
			"; it is still available in Gemini Web conversation " + result.ConversationID}
	}
	result.Data = data
	// The card in a StreamGenerate reply may omit the duration; read it from the file.
	if result.Video.Duration == 0 {
		result.Video.Duration = mp4Duration(data)
	}
	return result, nil
}

func (c *Client) waitForVideo(ctx context.Context, result *VideoResult, interval time.Duration) ([]Video, error) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil, &VideoError{Code: VideoErrorTimeout, Message: "Video was not ready before the deadline; it may still appear in Gemini Web conversation " + result.ConversationID}
		case <-ticker.C:
		}
		videos, text, err := c.readConversationVideos(ctx, result.ConversationID)
		if err != nil {
			c.log.Warn("Video status poll failed", zap.String("conversation_id", result.ConversationID), zap.Error(err))
			continue
		}
		if text != "" {
			result.Text = stripVideoPlaceholders(text)
		}
		if len(videos) > 0 {
			return videos, nil
		}
		if refusal := classifyVideoReply(text); refusal != nil {
			return nil, refusal
		}
		c.log.Debug("Video still generating", zap.String("conversation_id", result.ConversationID))
	}
}

// readConversationVideos reads the latest turns of a conversation (hNvQHb) and
// returns the generated videos and the newest reply text.
func (c *Client) readConversationVideos(ctx context.Context, conversationID string) ([]Video, string, error) {
	c.mu.RLock()
	token, cookieHeader, buildLabel, sessionID, language, authUser, generationID := c.at, c.cookieHeader, c.buildLabel, c.sessionID, c.language, c.authUser, c.generationID
	c.mu.RUnlock()
	if token == "" {
		return nil, "", errors.New("client not initialized")
	}
	if language == "" {
		language = "en"
	}
	payload, _ := json.Marshal([]interface{}{conversationID, 10, nil, 1, []int{1}, []int{4}, nil, 1})
	envelope, _ := json.Marshal([]interface{}{[]interface{}{[]interface{}{geminiReadConversationRPC, string(payload), nil, "generic"}}})
	query := url.Values{
		"rpcids":      {geminiReadConversationRPC},
		"hl":          {language},
		"_reqid":      {strconv.Itoa(rand.Intn(90000) + 10000)},
		"rt":          {"c"},
		"source-path": {geminiSourcePath(authUser) + "/" + strings.TrimPrefix(conversationID, "c_")},
	}
	if buildLabel != "" {
		query.Set("bl", buildLabel)
	}
	if sessionID != "" {
		query.Set("f.sid", sessionID)
	}
	form := url.Values{"at": {token}, "f.req": {string(envelope)}}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, geminiAccountURL(EndpointBatchExec, authUser)+"?"+query.Encode(), strings.NewReader(form.Encode()))
	if err != nil {
		return nil, "", err
	}
	for name, value := range DefaultHeaders {
		request.Header.Set(name, value)
	}
	request.Header.Set("Cookie", cookieHeader)
	batchHeader := make([]interface{}, 17)
	batchHeader[0] = 1
	batchHeader[8] = []int{4, 5, 6, 8, 16}
	batchHeader[16] = generationID
	headerJSON, _ := json.Marshal(batchHeader)
	request.Header.Set(geminiModelHeaderKey, string(headerJSON))
	request.Header.Set("x-goog-ext-73010989-jspb", "[0]")

	response, err := (&http.Client{Timeout: time.Minute}).Do(request)
	if err != nil {
		var requestErr *url.Error
		if errors.As(err, &requestErr) {
			err = requestErr.Err
		}
		return nil, "", fmt.Errorf("read conversation request failed: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("read conversation failed with HTTP status %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxConversationReadBytes+1))
	if err != nil {
		return nil, "", err
	}
	if len(body) > maxConversationReadBytes {
		return nil, "", errors.New("conversation response is too large")
	}
	videos, text := parseConversationVideos(body)
	return videos, text, nil
}

// parseConversationVideos decodes a batchexecute hNvQHb response. Turns are
// [[cid,rid], null, userMessage, [candidate…], …] with the newest turn first.
func parseConversationVideos(body []byte) ([]Video, string) {
	body = bytes.TrimPrefix(bytes.TrimSpace(body), []byte(")]}'"))
	decoder := json.NewDecoder(bytes.NewReader(body))
	var records [][]interface{}
	var collect func(value any)
	collect = func(value any) {
		a, ok := value.([]interface{})
		if !ok {
			return
		}
		if len(a) > 2 {
			if tag, _ := a[0].(string); tag == "wrb.fr" {
				records = append(records, a)
				return
			}
		}
		for _, child := range a {
			collect(child)
		}
	}
	for {
		var value any
		if decoder.Decode(&value) != nil {
			break
		}
		collect(value)
	}

	var videos []Video
	text := ""
	for _, record := range records {
		raw, _ := record[2].(string)
		var payload []interface{}
		if json.Unmarshal([]byte(raw), &payload) != nil || len(payload) == 0 {
			continue
		}
		turns, _ := payload[0].([]interface{})
		for _, rawTurn := range turns {
			turn, _ := rawTurn.([]interface{})
			if len(turn) < 4 {
				continue
			}
			replies, _ := turn[3].([]interface{})
			if len(replies) == 0 {
				continue
			}
			candidates, _ := replies[0].([]interface{})
			for _, rawCandidate := range candidates {
				candidate, ok := rawCandidate.([]interface{})
				if !ok {
					continue
				}
				videos = append(videos, extractGeneratedVideos(candidate)...)
				if text == "" && len(candidate) > 1 {
					if parts, ok := candidate[1].([]interface{}); ok && len(parts) > 0 {
						text, _ = parts[0].(string)
					}
				}
			}
			// Only the newest turn belongs to this generation.
			return videos, text
		}
	}
	return videos, text
}

func (c *Client) downloadGeneratedVideo(ctx context.Context, rawURL, cookieHeader string) ([]byte, error) {
	if !trustedMediaURL(rawURL) {
		return nil, fmt.Errorf("refusing generated-video download from untrusted host")
	}
	client := generatedImageHTTPClient(cookieHeader)
	client.Timeout = generatedVideoDownloadTime
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
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
		return nil, err
	}
	defer response.Body.Close()
	contentType := response.Header.Get("Content-Type")
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download returned HTTP %d", response.StatusCode)
	}
	if strings.HasPrefix(contentType, "text/") || strings.Contains(contentType, "json") {
		return nil, fmt.Errorf("download returned %s instead of a video", contentType)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxGeneratedVideoBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxGeneratedVideoBytes {
		return nil, fmt.Errorf("generated video exceeds %d byte limit", maxGeneratedVideoBytes)
	}
	if !isMP4(body) {
		return nil, fmt.Errorf("downloaded data is not an MP4 video (%s)", contentType)
	}
	return body, nil
}

// withAuthUser selects the Google multi-login account that owns the video;
// without it the download is authorized against account 0 and returns 403.
func withAuthUser(rawURL, authUser string) string {
	if authUser == "" {
		return rawURL
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	query := u.Query()
	query.Set("authuser", authUser)
	u.RawQuery = query.Encode()
	return u.String()
}

// isMP4 checks for the ISO BMFF "ftyp" box at the start of the file.
func isMP4(data []byte) bool {
	return len(data) >= 12 && string(data[4:8]) == "ftyp"
}

// mp4Duration returns the movie duration in seconds from moov/mvhd, or 0.
func mp4Duration(data []byte) float64 {
	moov := mp4Box(data, "moov")
	mvhd := mp4Box(moov, "mvhd")
	if len(mvhd) < 4 {
		return 0
	}
	var timescale, duration uint64
	switch mvhd[0] { // version
	case 0:
		if len(mvhd) < 20 {
			return 0
		}
		timescale = uint64(binary.BigEndian.Uint32(mvhd[12:16]))
		duration = uint64(binary.BigEndian.Uint32(mvhd[16:20]))
	case 1:
		if len(mvhd) < 32 {
			return 0
		}
		timescale = uint64(binary.BigEndian.Uint32(mvhd[20:24]))
		duration = binary.BigEndian.Uint64(mvhd[24:32])
	}
	if timescale == 0 {
		return 0
	}
	return float64(duration) / float64(timescale)
}

// mp4Box returns the payload of the first top-level box of the given type.
func mp4Box(data []byte, boxType string) []byte {
	for len(data) >= 8 {
		size := uint64(binary.BigEndian.Uint32(data[0:4]))
		header := uint64(8)
		switch size {
		case 0:
			size = uint64(len(data))
		case 1:
			if len(data) < 16 {
				return nil
			}
			size, header = binary.BigEndian.Uint64(data[8:16]), 16
		}
		if size < header || size > uint64(len(data)) {
			return nil
		}
		if string(data[4:8]) == boxType {
			return data[header:size]
		}
		data = data[size:]
	}
	return nil
}

// ---- Background video jobs (shared by the OpenAI and Gemini APIs) ----

// Video job states.
const (
	VideoJobInProgress = "in_progress"
	VideoJobCompleted  = "completed"
	VideoJobFailed     = "failed"

	maxVideoPromptBytes = 8000
	maxRetainedVideos   = 20
	videoJobTimeout     = 10 * time.Minute
	videoJobRetention   = time.Hour
)

var (
	ErrVideoBusy        = errors.New("a video is already being generated; wait for it to finish")
	ErrVideoJobNotFound = errors.New("video job not found")
	ErrVideoNotReady    = errors.New("video is not ready")
)

// VideoValidationError is a client error in a video request.
type VideoValidationError struct{ Message string }

func (e *VideoValidationError) Error() string { return e.Message }

// VideoJob is a snapshot of a background video generation.
type VideoJob struct {
	ID             string
	Status         string
	Model          string
	Prompt         string
	Aspect         VideoAspectRatio
	CreatedAt      time.Time
	CompletedAt    time.Time
	ConversationID string
	Video          Video
	Bytes          int
	Message        string
	Error          *VideoError
}

// Size returns "WIDTHxHEIGHT", using the generated video when known.
func (j VideoJob) Size() string {
	if j.Video.Width > 0 && j.Video.Height > 0 {
		return fmt.Sprintf("%dx%d", j.Video.Width, j.Video.Height)
	}
	if j.Aspect == VideoAspectPortrait {
		return "720x1280"
	}
	return "1280x720"
}

// ExpiresAt is when a finished job and its video are discarded.
func (j VideoJob) ExpiresAt() time.Time {
	if j.CompletedAt.IsZero() {
		return time.Time{}
	}
	return j.CompletedAt.Add(videoJobRetention)
}

// VideoGenerateFunc produces one video; it is GenerateVideo in production.
type VideoGenerateFunc func(ctx context.Context, prompt, model string, aspect VideoAspectRatio) (*VideoResult, error)

type videoJobEntry struct {
	job  VideoJob
	data []byte
}

// videoJobStore keeps jobs in memory. Gemini Web allows few videos per day,
// so only one job runs at a time.
type videoJobStore struct {
	generate VideoGenerateFunc
	timeout  time.Duration

	mu     sync.Mutex
	jobs   map[string]*videoJobEntry
	active int
	root   context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

func newVideoJobStore(generate VideoGenerateFunc) *videoJobStore {
	root, cancel := context.WithCancel(context.Background())
	return &videoJobStore{generate: generate, timeout: videoJobTimeout, jobs: make(map[string]*videoJobEntry), root: root, cancel: cancel}
}

func (c *Client) videoJobStore() *videoJobStore {
	c.videoJobsOnce.Do(func() {
		c.videoJobs = newVideoJobStore(func(ctx context.Context, prompt, model string, aspect VideoAspectRatio) (*VideoResult, error) {
			return c.GenerateVideo(ctx, prompt, model, aspect, 0)
		})
	})
	return c.videoJobs
}

// NewVideoTestClient returns a client that advertises models and runs video
// jobs with generate instead of Gemini Web. It is for tests in other packages.
func NewVideoTestClient(models []ModelInfo, generate VideoGenerateFunc) *Client {
	c := &Client{log: zap.NewNop(), stopRefresh: make(chan struct{})}
	for i, model := range models {
		c.cachedModels = append(c.cachedModels, geminiModel{ModelInfo: model, ModelID: fmt.Sprintf("%016d", i+1), Capacity: 1, CapacityField: 12, ModelNumber: i + 1})
	}
	c.videoJobsOnce.Do(func() { c.videoJobs = newVideoJobStore(generate) })
	return c
}

// ParseVideoAspect accepts an OpenAI-style size and/or a "16:9"/"9:16" ratio.
func ParseVideoAspect(size, aspectRatio string) (VideoAspectRatio, error) {
	var fromSize, fromRatio VideoAspectRatio
	switch strings.ToLower(strings.TrimSpace(size)) {
	case "":
	case "1280x720", "1920x1080", "1792x1024":
		fromSize = VideoAspectLandscape
	case "720x1280", "1080x1920", "1024x1792":
		fromSize = VideoAspectPortrait
	default:
		return 0, &VideoValidationError{Message: "size must be 1280x720 or 720x1280"}
	}
	switch strings.TrimSpace(aspectRatio) {
	case "":
	case "16:9":
		fromRatio = VideoAspectLandscape
	case "9:16":
		fromRatio = VideoAspectPortrait
	default:
		return 0, &VideoValidationError{Message: "aspect ratio must be 16:9 or 9:16"}
	}
	if fromSize != 0 && fromRatio != 0 && fromSize != fromRatio {
		return 0, &VideoValidationError{Message: "size and aspect ratio disagree"}
	}
	if fromSize == VideoAspectPortrait || fromRatio == VideoAspectPortrait {
		return VideoAspectPortrait, nil
	}
	return VideoAspectLandscape, nil
}

// StartVideoJob validates the request and generates the video in the background.
func (c *Client) StartVideoJob(prompt, model string, aspect VideoAspectRatio) (VideoJob, error) {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return VideoJob{}, &VideoValidationError{Message: "prompt is required"}
	}
	if len(prompt) > maxVideoPromptBytes {
		return VideoJob{}, &VideoValidationError{Message: fmt.Sprintf("prompt must be at most %d bytes", maxVideoPromptBytes)}
	}
	if aspect != VideoAspectPortrait {
		aspect = VideoAspectLandscape
	}
	// SDK defaults name the official video models; Gemini Web always uses its
	// own video model, so route those names to the account's default model.
	if lower := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(model), "models/")); strings.HasPrefix(lower, "sora") || strings.HasPrefix(lower, "veo") {
		model = ""
	}
	resolved, err := c.ResolveModel(model)
	if err != nil {
		return VideoJob{}, err
	}
	return c.videoJobStore().start(prompt, resolved.ID, aspect)
}

// GetVideoJob returns the current state of a job.
func (c *Client) GetVideoJob(id string) (VideoJob, error) {
	return c.videoJobStore().get(id)
}

// VideoJobContent returns the MP4 bytes of a completed job.
func (c *Client) VideoJobContent(id string) (VideoJob, []byte, error) {
	return c.videoJobStore().content(id)
}

// ListVideoJobs returns retained jobs, newest first.
func (c *Client) ListVideoJobs() []VideoJob {
	return c.videoJobStore().list()
}

// DeleteVideoJob discards a finished job and its video.
func (c *Client) DeleteVideoJob(id string) error {
	return c.videoJobStore().delete(id)
}

func (s *videoJobStore) start(prompt, model string, aspect VideoAspectRatio) (VideoJob, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(time.Now())
	if s.root.Err() != nil {
		return VideoJob{}, errors.New("server is shutting down")
	}
	if s.active > 0 {
		return VideoJob{}, ErrVideoBusy
	}
	// Lowercase hex only: google-genai extracts file names with [a-z0-9]+.
	id := strings.ReplaceAll(uuid.NewString(), "-", "")
	entry := &videoJobEntry{job: VideoJob{ID: id, Status: VideoJobInProgress, Model: model, Prompt: prompt, Aspect: aspect, CreatedAt: time.Now()}}
	s.jobs[id] = entry
	s.active++
	s.wg.Add(1)
	go s.run(id, prompt, model, aspect)
	return entry.job, nil
}

func (s *videoJobStore) run(id, prompt, model string, aspect VideoAspectRatio) {
	defer s.wg.Done()
	ctx, cancel := context.WithTimeout(s.root, s.timeout)
	defer cancel()

	var result *VideoResult
	var err error
	func() {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("video worker panic: %v", r)
			}
		}()
		result, err = s.generate(ctx, prompt, model, aspect)
	}()

	s.mu.Lock()
	defer s.mu.Unlock()
	s.active--
	entry, ok := s.jobs[id]
	if !ok {
		return
	}
	job := &entry.job
	job.CompletedAt = time.Now()
	if err != nil {
		var videoErr *VideoError
		if !errors.As(err, &videoErr) {
			videoErr = &VideoError{Code: VideoErrorUpstream, Message: err.Error()}
		}
		job.Status = VideoJobFailed
		job.Error = videoErr
		job.Message = videoErr.ProviderMessage
		return
	}
	entry.data = result.Data
	job.Status = VideoJobCompleted
	job.ConversationID = result.ConversationID
	job.Video = result.Video
	job.Bytes = len(result.Data)
	job.Message = result.Text
	if result.Model != "" {
		job.Model = result.Model
	}
}

func (s *videoJobStore) get(id string) (VideoJob, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(time.Now())
	entry, ok := s.jobs[id]
	if !ok {
		return VideoJob{}, ErrVideoJobNotFound
	}
	return entry.job, nil
}

func (s *videoJobStore) content(id string) (VideoJob, []byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(time.Now())
	entry, ok := s.jobs[id]
	if !ok {
		return VideoJob{}, nil, ErrVideoJobNotFound
	}
	if entry.job.Status != VideoJobCompleted {
		return entry.job, nil, ErrVideoNotReady
	}
	return entry.job, entry.data, nil
}

func (s *videoJobStore) list() []VideoJob {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(time.Now())
	jobs := make([]VideoJob, 0, len(s.jobs))
	for _, entry := range s.jobs {
		jobs = append(jobs, entry.job)
	}
	sort.Slice(jobs, func(i, j int) bool { return jobs[i].CreatedAt.After(jobs[j].CreatedAt) })
	return jobs
}

func (s *videoJobStore) delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.jobs[id]
	if !ok {
		return ErrVideoJobNotFound
	}
	if entry.job.Status == VideoJobInProgress {
		return ErrVideoNotReady
	}
	delete(s.jobs, id)
	return nil
}

func (s *videoJobStore) close() {
	s.cancel()
	s.wg.Wait()
}

// pruneLocked drops expired jobs and, beyond the cap, the oldest finished ones.
func (s *videoJobStore) pruneLocked(now time.Time) {
	for id, entry := range s.jobs {
		if expires := entry.job.ExpiresAt(); !expires.IsZero() && now.After(expires) {
			delete(s.jobs, id)
		}
	}
	for len(s.jobs) > maxRetainedVideos {
		oldestID := ""
		var oldest time.Time
		for id, entry := range s.jobs {
			if entry.job.CompletedAt.IsZero() {
				continue
			}
			if oldestID == "" || entry.job.CompletedAt.Before(oldest) {
				oldestID, oldest = id, entry.job.CompletedAt
			}
		}
		if oldestID == "" {
			return
		}
		delete(s.jobs, oldestID)
	}
}
