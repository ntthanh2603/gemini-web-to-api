package providers

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
)

const (
	testVideoDownloadURL = "https://contribution.usercontent.google.com/download?c=test&filename=video.mp4"
	testVideoThumbURL    = "https://lh3.googleusercontent.com/gg/test-thumbnail"
)

// videoTestCandidate mirrors the hNvQHb layout captured from Gemini Web: the
// card lives in candidate[12] as sparse JSPB field "60".
func videoTestCandidate(text string, withVideo bool) []interface{} {
	candidate := make([]interface{}, 13)
	candidate[0] = "rc_test"
	candidate[1] = []interface{}{text}
	candidate[8] = []interface{}{2.0}
	candidate[9] = "en"
	if withVideo {
		item := []interface{}{nil, 2.0, "video.mp4", nil, nil, "$token", nil,
			[]interface{}{testVideoThumbURL, testVideoDownloadURL, testVideoThumbURL + "=mm,22,18"},
			2.0, []interface{}{1790299368.0, 667185666.0}, nil, "video/mp4", nil, nil, nil, nil, nil,
			[]interface{}{[]interface{}{9.0, 999999000.0}, 1280.0, 720.0}, nil, nil, nil, nil, nil, nil, 4657944.0}
		prompt := []interface{}{"A cat", nil, []interface{}{nil, nil, "models/omni"}}
		card := []interface{}{[]interface{}{[]interface{}{[]interface{}{item}, prompt}}}
		candidate[12] = []interface{}{map[string]interface{}{"8": []interface{}{}, "60": card}}
	} else {
		candidate[12] = []interface{}{map[string]interface{}{"8": []interface{}{}}}
	}
	return candidate
}

func conversationTestBody(text string, withVideo bool) string {
	turn := []interface{}{
		[]interface{}{"c_test", "r_test"}, nil,
		[]interface{}{[]interface{}{"A cat"}, 2.0},
		[]interface{}{[]interface{}{videoTestCandidate(text, withVideo)}},
	}
	payload, _ := json.Marshal([]interface{}{[]interface{}{turn}})
	record, _ := json.Marshal([]interface{}{[]interface{}{"wrb.fr", "hNvQHb", string(payload), nil, nil, nil, "generic"}})
	return ")]}'\n\n123\n" + string(record) + "\n"
}

func TestExtractGeneratedVideosFromSparseCard(t *testing.T) {
	videos := extractGeneratedVideos(videoTestCandidate("Your video is ready!", true))
	if len(videos) != 1 {
		t.Fatalf("expected one video, got %#v", videos)
	}
	v := videos[0]
	if v.URL != testVideoDownloadURL || v.ThumbnailURL != testVideoThumbURL || v.MimeType != "video/mp4" ||
		v.Width != 1280 || v.Height != 720 || v.FileName != "video.mp4" || v.SizeBytes != 4657944 || v.Duration < 9.99 || v.Duration > 10 {
		t.Fatalf("unexpected video: %#v", v)
	}
}

func TestExtractGeneratedVideosIgnoresUntrustedAndTextLinks(t *testing.T) {
	candidate := videoTestCandidate("see https://evil.example/video.mp4", true)
	card := candidate[12].([]interface{})[0].(map[string]interface{})["60"]
	item := card.([]interface{})[0].([]interface{})[0].([]interface{})[0].([]interface{})[0].([]interface{})
	item[7].([]interface{})[1] = "https://evil.example/video.mp4"
	if videos := extractGeneratedVideos(candidate); len(videos) != 0 {
		t.Fatalf("untrusted video URL accepted: %#v", videos)
	}
	if videos := extractGeneratedVideos(videoTestCandidate("https://lh3.googleusercontent.com/x.mp4", false)); len(videos) != 0 {
		t.Fatalf("text link treated as video: %#v", videos)
	}
}

func TestParseConversationVideos(t *testing.T) {
	videos, text := parseConversationVideos([]byte(conversationTestBody("Your video is ready!\n\nhttp://googleusercontent.com/generated_video_content/1", true)))
	if len(videos) != 1 || !strings.HasPrefix(text, "Your video is ready!") {
		t.Fatalf("videos=%#v text=%q", videos, text)
	}
	if got := stripVideoPlaceholders(text); got != "Your video is ready!" {
		t.Fatalf("placeholder not stripped: %q", got)
	}
}

func TestApplyVideoInnerMatchesWebClient(t *testing.T) {
	for _, tc := range []struct {
		aspect     VideoAspectRatio
		wantAspect string
		wantTool   string
	}{{VideoAspectLandscape, "1", "[[16]]"}, {VideoAspectPortrait, "2", "[[17]]"}} {
		inner := applyVideoInner(buildGenerateInner("A cat", nil, 3, "en", "req", true), tc.aspect)
		encoded, _ := json.Marshal(inner)
		var decoded []interface{}
		_ = json.Unmarshal(encoded, &decoded)
		if len(decoded) != geminiVideoInnerLength {
			t.Fatalf("length = %d", len(decoded))
		}
		message := decoded[0].([]interface{})
		if message[0] != "A cat" {
			t.Fatalf("prompt lost: %v", message)
		}
		aspect, _ := json.Marshal(message[9])
		if want := `[null,null,null,null,null,null,[[null,null,null,` + tc.wantAspect + `]]]`; string(aspect) != want {
			t.Fatalf("aspect options = %s, want %s", aspect, want)
		}
		tool, _ := json.Marshal(decoded[55])
		if decoded[49] != float64(11) || string(tool) != tc.wantTool || decoded[45] != nil {
			t.Fatalf("video tool flags wrong: [49]=%v [55]=%s [45]=%v", decoded[49], tool, decoded[45])
		}
	}
}

func TestGenerateVideoSubmitsOncePollsAndDownloads(t *testing.T) {
	original := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = original })
	generates, polls := 0, 0
	mp4 := append([]byte{0, 0, 0, 24}, []byte("ftypisom\x00\x00\x02\x00isomiso2")...)
	http.DefaultTransport = generationTestTransport(func(r *http.Request) (*http.Response, error) {
		respond := func(contentType, body string) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {contentType}},
				Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
		}
		switch {
		case strings.Contains(r.URL.Path, "StreamGenerate"):
			generates++
			_ = r.ParseForm()
			var outer []interface{}
			_ = json.Unmarshal([]byte(r.PostForm.Get("f.req")), &outer)
			var inner []interface{}
			_ = json.Unmarshal([]byte(outer[1].(string)), &inner)
			if len(inner) != geminiVideoInnerLength || inner[49] != float64(11) {
				t.Fatalf("not a video request: len=%d [49]=%v", len(inner), inner[49])
			}
			var header []interface{}
			_ = json.Unmarshal([]byte(r.Header.Get(geminiModelHeaderKey)), &header)
			if features, _ := json.Marshal(header[8]); string(features) != "[4,5,6,8,16]" {
				t.Fatalf("model header features = %s", features)
			}
			payload, _ := json.Marshal([]interface{}{nil, []interface{}{"c_test", "r_test"}, nil, nil,
				[]interface{}{videoTestCandidate("I'm generating your video. Check back in a few minutes.", false)}})
			body, _ := json.Marshal([]interface{}{[]interface{}{"wrb.fr", nil, string(payload)}})
			return respond("application/json", string(body))
		case strings.Contains(r.URL.Path, "batchexecute"):
			polls++
			_ = r.ParseForm()
			if r.URL.Query().Get("rpcids") != "hNvQHb" || !strings.Contains(r.PostForm.Get("f.req"), `\"c_test\"`) {
				t.Fatalf("unexpected poll: %s %s", r.URL.RawQuery, r.PostForm.Get("f.req"))
			}
			return respond("application/json", conversationTestBody("Your video is ready!", polls >= 2))
		case r.URL.Host == "contribution.usercontent.google.com":
			if r.Header.Get("Cookie") != "session=test-cookie" || r.URL.Query().Get("authuser") != "2" {
				t.Fatal("download must carry the Gemini session cookie and account slot")
			}
			return respond("video/mp4", string(mp4))
		}
		t.Fatalf("unexpected request %s", r.URL)
		return nil, nil
	})

	client := &Client{at: "test-token", cookieHeader: "session=test-cookie", cachedModels: generationTestModels(),
		language: "en", authUser: "2", log: zap.NewNop(), maxRetries: 3, defaultTemporary: true}
	result, err := client.GenerateVideo(context.Background(), "A cat", "gemini-pro", VideoAspectLandscape, time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if generates != 1 || polls != 2 {
		t.Fatalf("generates=%d polls=%d", generates, polls)
	}
	if result.ConversationID != "c_test" || string(result.Data) != string(mp4) || result.Video.Width != 1280 {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestGenerateVideoReportsQuotaWithoutPolling(t *testing.T) {
	original := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = original })
	http.DefaultTransport = generationTestTransport(func(r *http.Request) (*http.Response, error) {
		if !strings.Contains(r.URL.Path, "StreamGenerate") {
			t.Fatalf("unexpected request %s", r.URL)
		}
		payload, _ := json.Marshal([]interface{}{nil, []interface{}{"c_test"}, nil, nil,
			[]interface{}{videoTestCandidate("I can create more videos as soon as your limit resets. Check your usage in Settings.", false)}})
		body, _ := json.Marshal([]interface{}{[]interface{}{"wrb.fr", nil, string(payload)}})
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(body))), Request: r}, nil
	})
	client := &Client{at: "test-token", cookieHeader: "c", cachedModels: generationTestModels(), language: "en", log: zap.NewNop()}
	_, err := client.GenerateVideo(context.Background(), "A cat", "", VideoAspectLandscape, time.Millisecond)
	var videoErr *VideoError
	if !errors.As(err, &videoErr) || videoErr.Code != VideoErrorQuota {
		t.Fatalf("expected quota error, got %v", err)
	}
}

func TestDownloadGeneratedVideoRejectsNonVideo(t *testing.T) {
	original := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = original })
	http.DefaultTransport = generationTestTransport(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/html"}},
			Body: io.NopCloser(strings.NewReader("<html>login</html>")), Request: r}, nil
	})
	client := &Client{log: zap.NewNop()}
	if _, err := client.downloadGeneratedVideo(context.Background(), testVideoDownloadURL, ""); err == nil {
		t.Fatal("HTML response accepted as video")
	}
	if _, err := client.downloadGeneratedVideo(context.Background(), "https://evil.example/v.mp4", ""); err == nil {
		t.Fatal("untrusted host accepted")
	}
}

type fakeVideoRun struct {
	release chan struct{}
	model   string
	aspect  VideoAspectRatio
	err     error
}

func (f *fakeVideoRun) generate(ctx context.Context, prompt, model string, aspect VideoAspectRatio) (*VideoResult, error) {
	f.model, f.aspect = model, aspect
	select {
	case <-f.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if f.err != nil {
		return nil, f.err
	}
	return &VideoResult{Video: Video{MimeType: "video/mp4", Width: 720, Height: 1280, Duration: 9.99}, Data: []byte("mp4"), ConversationID: "c_test", Model: model}, nil
}

func waitForVideoJob(t *testing.T, c *Client, id, status string) VideoJob {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		job, err := c.GetVideoJob(id)
		if err != nil {
			t.Fatal(err)
		}
		if job.Status == status {
			return job
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("job %s never reached %s", id, status)
	return VideoJob{}
}

func TestVideoJobLifecycle(t *testing.T) {
	run := &fakeVideoRun{release: make(chan struct{})}
	c := NewVideoTestClient([]ModelInfo{{ID: "gemini-flash"}, {ID: "gemini-pro"}}, run.generate)
	defer c.videoJobStore().close()

	job, err := c.StartVideoJob(" A cat ", "veo-3.0-generate-001", VideoAspectPortrait)
	if err != nil || job.Status != VideoJobInProgress || job.Prompt != "A cat" || job.Model != "gemini-flash" || job.Size() != "720x1280" {
		t.Fatalf("job=%#v err=%v", job, err)
	}
	if _, err := c.StartVideoJob("Another", "", VideoAspectLandscape); !errors.Is(err, ErrVideoBusy) {
		t.Fatalf("concurrent job must be rejected, got %v", err)
	}
	if _, _, err := c.VideoJobContent(job.ID); !errors.Is(err, ErrVideoNotReady) {
		t.Fatalf("content before completion: %v", err)
	}
	if err := c.DeleteVideoJob(job.ID); !errors.Is(err, ErrVideoNotReady) {
		t.Fatalf("running job must not be deleted, got %v", err)
	}

	close(run.release)
	done := waitForVideoJob(t, c, job.ID, VideoJobCompleted)
	if run.aspect != VideoAspectPortrait || done.ConversationID != "c_test" || done.Bytes != 3 || done.ExpiresAt().IsZero() {
		t.Fatalf("unexpected completed job: %#v", done)
	}
	if _, data, err := c.VideoJobContent(job.ID); err != nil || string(data) != "mp4" {
		t.Fatalf("content=%q err=%v", data, err)
	}
	if jobs := c.ListVideoJobs(); len(jobs) != 1 || jobs[0].ID != job.ID {
		t.Fatalf("list = %#v", jobs)
	}
	if err := c.DeleteVideoJob(job.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := c.GetVideoJob(job.ID); !errors.Is(err, ErrVideoJobNotFound) {
		t.Fatalf("deleted job still present: %v", err)
	}
}

func TestVideoJobFailureKeepsProviderError(t *testing.T) {
	run := &fakeVideoRun{release: make(chan struct{}), err: &VideoError{Code: VideoErrorQuota, Message: "limit", ProviderMessage: "limit resets"}}
	close(run.release)
	c := NewVideoTestClient([]ModelInfo{{ID: "gemini-pro"}}, run.generate)
	defer c.videoJobStore().close()
	job, err := c.StartVideoJob("A cat", "gemini-pro", VideoAspectLandscape)
	if err != nil {
		t.Fatal(err)
	}
	failed := waitForVideoJob(t, c, job.ID, VideoJobFailed)
	if failed.Error == nil || failed.Error.Code != VideoErrorQuota || failed.Message != "limit resets" {
		t.Fatalf("unexpected failure: %#v", failed)
	}
}

func TestVideoJobValidation(t *testing.T) {
	c := NewVideoTestClient([]ModelInfo{{ID: "gemini-pro"}}, (&fakeVideoRun{release: make(chan struct{})}).generate)
	defer c.videoJobStore().close()
	var validationErr *VideoValidationError
	if _, err := c.StartVideoJob("  ", "", VideoAspectLandscape); !errors.As(err, &validationErr) {
		t.Fatalf("empty prompt accepted: %v", err)
	}
	var modelErr *ModelSelectionError
	if _, err := c.StartVideoJob("A cat", "unknown-model", VideoAspectLandscape); !errors.As(err, &modelErr) {
		t.Fatalf("unknown model accepted: %v", err)
	}
	for _, tc := range []struct {
		size, ratio string
		want        VideoAspectRatio
		ok          bool
	}{
		{"", "", VideoAspectLandscape, true},
		{"720x1280", "", VideoAspectPortrait, true},
		{"", "9:16", VideoAspectPortrait, true},
		{"1792x1024", "16:9", VideoAspectLandscape, true},
		{"640x480", "", 0, false},
		{"", "4:3", 0, false},
		{"1280x720", "9:16", 0, false},
	} {
		got, err := ParseVideoAspect(tc.size, tc.ratio)
		if (err == nil) != tc.ok || tc.ok && got != tc.want {
			t.Fatalf("ParseVideoAspect(%q,%q) = %v, %v", tc.size, tc.ratio, got, err)
		}
	}
}

func mp4TestBox(boxType string, payload []byte) []byte {
	box := make([]byte, 8, 8+len(payload))
	binary.BigEndian.PutUint32(box, uint32(8+len(payload)))
	copy(box[4:], boxType)
	return append(box, payload...)
}

func TestMP4DurationReadsMovieHeader(t *testing.T) {
	mvhd := make([]byte, 100)
	binary.BigEndian.PutUint32(mvhd[12:16], 1000)  // timescale
	binary.BigEndian.PutUint32(mvhd[16:20], 10000) // duration
	file := append(mp4TestBox("ftyp", []byte("isom\x00\x00\x02\x00")), mp4TestBox("moov", mp4TestBox("mvhd", mvhd))...)
	if got := mp4Duration(file); got != 10 {
		t.Fatalf("duration = %v, want 10", got)
	}
	if got := mp4Duration(file[:20]); got != 0 {
		t.Fatalf("truncated file duration = %v, want 0", got)
	}
}
