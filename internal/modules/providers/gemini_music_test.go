package providers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
)

const testAudioURL = "https://contribution.usercontent.google.com/download?c=test&filename=track.mp3"

func TestApplyMusicInnerMatchesWebClient(t *testing.T) {
	// Payloads captured from gemini.google.com with the "Create music" tool.
	for _, tc := range []struct {
		config      MusicConfig
		wantOptions string
		wantChips   string
	}{
		{MusicConfig{}, "", ""},
		{MusicConfig{Length: "short", Vocals: "instrumental", Genre: "lo-fi"}, "[null,null,null,null,null,null,[null,null,[4,1]]]", "[[42,26,51]]"},
		{MusicConfig{Length: "standard"}, "[null,null,null,null,null,null,[null,null,[5]]]", "[[43]]"},
		{MusicConfig{Vocals: "vocals"}, "[null,null,null,null,null,null,[null,null,[null,2]]]", "[[27]]"},
		{MusicConfig{Genre: "pop"}, "[null,null,null,null,null,null,[]]", "[[44]]"},
	} {
		inner := applyMusicInner(buildGenerateInner("A song", nil, 3, "en", "req", true), tc.config)
		encoded, _ := json.Marshal(inner)
		var decoded []interface{}
		_ = json.Unmarshal(encoded, &decoded)
		if len(decoded) != geminiWebClientInnerLength || decoded[49] != float64(geminiMusicToolMode) || decoded[45] != nil {
			t.Fatalf("%+v: not a music request: [49]=%v [45]=%v", tc.config, decoded[49], decoded[45])
		}
		message := decoded[0].([]interface{})
		options, chips := "", ""
		if len(message) > 9 {
			raw, _ := json.Marshal(message[9])
			options = string(raw)
		}
		if decoded[55] != nil {
			raw, _ := json.Marshal(decoded[55])
			chips = string(raw)
		}
		if options != tc.wantOptions || chips != tc.wantChips {
			t.Fatalf("%+v: options=%s chips=%s, want %s %s", tc.config, options, chips, tc.wantOptions, tc.wantChips)
		}
	}
}

func TestMusicConfigValidate(t *testing.T) {
	config := MusicConfig{Length: " Short ", Vocals: "with vocals", Genre: "Lo-Fi"}
	if err := config.Validate(); err != nil || config.Length != "short" || config.Vocals != MusicVocalsOn || config.Genre != "lo-fi" {
		t.Fatalf("config=%+v err=%v", config, err)
	}
	for _, bad := range []MusicConfig{{Length: "long"}, {Vocals: "choir"}, {Genre: "polka"}} {
		var validationErr *MusicValidationError
		if err := bad.Validate(); !errors.As(err, &validationErr) {
			t.Fatalf("%+v accepted: %v", bad, err)
		}
	}
}

func audioTestCandidate(text string, withAudio bool) []interface{} {
	candidate := make([]interface{}, 13)
	candidate[0] = "rc_test"
	candidate[1] = []interface{}{text + " https://lh3.googleusercontent.com/not-audio.mp3"}
	if withAudio {
		item := []interface{}{nil, 2.0, "track.mp3", nil, nil, "$token", nil,
			[]interface{}{"https://lh3.googleusercontent.com/gg/cover", testAudioURL}, nil, nil, nil, "audio/mpeg"}
		candidate[12] = []interface{}{map[string]interface{}{"61": []interface{}{[]interface{}{item}}}}
	}
	return candidate
}

func TestExtractGeneratedAudio(t *testing.T) {
	audios := extractGeneratedAudio(audioTestCandidate("Here is your track", true))
	if len(audios) != 1 || audios[0].URL != testAudioURL || audios[0].MimeType != "audio/mpeg" || audios[0].FileName != "track.mp3" {
		t.Fatalf("audios = %#v", audios)
	}
	if audios := extractGeneratedAudio(audioTestCandidate("text only", false)); len(audios) != 0 {
		t.Fatalf("text link treated as audio: %#v", audios)
	}
}

func TestClassifyMusicReplyRecognizesGeminiFailures(t *testing.T) {
	// Replies observed from Gemini Web when the music tool did not run.
	for text, code := range map[string]string{
		"While I am currently unable to synthesize and output an audio file directly, here are lo-fi sources": VideoErrorRefused,
		"I seem to be encountering an error. Can I try something else for you?":                                VideoErrorUpstream,
		"I can create more videos as soon as your limit resets.":                                               VideoErrorQuota,
	} {
		if err := classifyMusicReply(text); err == nil || err.Code != code {
			t.Fatalf("%q classified as %v, want %s", text, err, code)
		}
	}
	if err := classifyMusicReply("Here is your lo-fi track!"); err != nil {
		t.Fatalf("success reply classified as failure: %v", err)
	}
}

func TestGenerateMusicPollsAndDownloads(t *testing.T) {
	original, interval := http.DefaultTransport, musicPollInterval
	t.Cleanup(func() { http.DefaultTransport, musicPollInterval = original, interval })
	musicPollInterval = time.Millisecond
	generates, polls := 0, 0
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
			if inner[49] != float64(geminiMusicToolMode) {
				t.Fatalf("not a music request: [49]=%v", inner[49])
			}
			payload, _ := json.Marshal([]interface{}{nil, []interface{}{"c_test", "r_test"}, nil, nil,
				[]interface{}{audioTestCandidate("Creating your track...", false)}})
			body, _ := json.Marshal([]interface{}{[]interface{}{"wrb.fr", nil, string(payload)}})
			return respond("application/json", string(body))
		case strings.Contains(r.URL.Path, "batchexecute"):
			polls++
			turn := []interface{}{[]interface{}{"c_test", "r_test"}, nil, []interface{}{[]interface{}{"A song"}},
				[]interface{}{[]interface{}{audioTestCandidate("Here is your track", true)}}}
			payload, _ := json.Marshal([]interface{}{[]interface{}{turn}})
			record, _ := json.Marshal([]interface{}{[]interface{}{"wrb.fr", "hNvQHb", string(payload)}})
			return respond("application/json", ")]}'\n"+string(record))
		case r.URL.Host == "contribution.usercontent.google.com":
			if r.URL.Query().Get("authuser") != "2" {
				t.Fatal("download must select the account slot")
			}
			return respond("audio/mpeg", "ID3-audio-bytes")
		}
		t.Fatalf("unexpected request %s", r.URL)
		return nil, nil
	})

	client := &Client{at: "test-token", cookieHeader: "c", cachedModels: generationTestModels(), language: "en", authUser: "2", log: zap.NewNop(), maxRetries: 3}
	result, err := client.GenerateMusic(context.Background(), "A song", "gemini-pro-music", MusicConfig{Vocals: "instrumental"})
	if err != nil {
		t.Fatal(err)
	}
	if generates != 1 || polls < 1 || string(result.Data) != "ID3-audio-bytes" || result.Audio.MimeType != "audio/mpeg" || result.ConversationID != "c_test" {
		t.Fatalf("generates=%d polls=%d result=%#v", generates, polls, result)
	}
}
