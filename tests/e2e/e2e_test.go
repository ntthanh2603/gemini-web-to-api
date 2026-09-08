package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

type model struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
}

type modelList struct {
	Data []model `json:"data"`
}

const liveRequestPause = 2 * time.Second

func TestLiveAPIContracts(t *testing.T) {
	if os.Getenv("E2E") != "1" {
		t.Skip("set E2E=1 or run 'task e2e' to use the real Gemini Web account")
	}

	root := repositoryRoot(t)
	if _, err := os.Stat(filepath.Join(root, ".env")); err != nil {
		t.Skip("root .env is required for live Gemini E2E tests")
	}
	baseURL, stop := startServer(t, root)
	defer stop()

	// The provider itself permits a Gemini Web request to take up to five
	// minutes. Keep the harness timeout slightly longer so it observes the
	// server's result instead of abandoning an in-flight upstream request.
	client := &http.Client{Timeout: 6 * time.Minute}
	models := waitForModels(t, client, baseURL)
	if len(models) == 0 {
		t.Fatal("model registry is empty")
	}
	for _, item := range models {
		if strings.TrimSpace(item.DisplayName) == "" {
			t.Errorf("model %q has no display_name from Gemini Web", item.ID)
		}
	}

	t.Run("model lookup", func(t *testing.T) {
		for _, item := range models {
			var got model
			getJSON(t, client, baseURL+"/openai/v1/models/"+item.ID, http.StatusOK, &got)
			if got.ID != item.ID || got.DisplayName != item.DisplayName {
				t.Fatalf("lookup = %#v, list entry = %#v", got, item)
			}
		}
	})

	t.Run("invalid model fails before stream", func(t *testing.T) {
		body := map[string]any{"model": "model-that-does-not-exist", "stream": true,
			"messages": []map[string]string{{"role": "user", "content": "hello"}}}
		resp := postJSON(t, client, baseURL+"/openai/v1/chat/completions", body)
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400; body=%s", resp.StatusCode, readBody(resp.Body))
		}
		if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
			t.Fatal("invalid model incorrectly opened an SSE response")
		}
	})

	t.Run("OpenAI generation routes every discovered model", func(t *testing.T) {
		for _, item := range models {
			item := item
			t.Run(item.ID, func(t *testing.T) {
				time.Sleep(liveRequestPause)
				var got struct {
					Model          string `json:"model"`
					RequestedModel string `json:"requested_model"`
					Choices        []struct {
						Message struct {
							Content string `json:"content"`
						} `json:"message"`
					} `json:"choices"`
				}
				prompt := "Reply with one short word: OK"
				resp := postJSON(t, client, baseURL+"/openai/v1/chat/completions", map[string]any{
					"model": item.ID, "messages": []map[string]string{{"role": "user", "content": prompt}},
				})
				defer resp.Body.Close()
				data := readBody(resp.Body)
				if resp.StatusCode != http.StatusOK {
					t.Fatalf("status=%d body=%s", resp.StatusCode, data)
				}
				if err := json.Unmarshal([]byte(data), &got); err != nil {
					t.Fatalf("decode completion: %v; body=%s", err, data)
				}
				if !containsModel(models, got.Model) || got.RequestedModel != item.ID || len(got.Choices) != 1 || strings.TrimSpace(got.Choices[0].Message.Content) == "" {
					t.Fatalf("invalid completion for %s: %#v", item.ID, got)
				}
				if got.Model != item.ID {
					t.Logf("Gemini routed %s to %s", item.ID, got.Model)
				}
			})
		}
	})

	if pro, ok := findModelByDisplay(models, "pro"); ok {
		t.Run("advanced alias resolves to pro", func(t *testing.T) {
			time.Sleep(liveRequestPause)
			var got struct {
				Model          string `json:"model"`
				RequestedModel string `json:"requested_model"`
			}
			resp := postJSON(t, client, baseURL+"/openai/v1/chat/completions", map[string]any{
				"model": "gemini-advanced", "messages": []map[string]string{{"role": "user", "content": "Reply: OK"}},
			})
			defer resp.Body.Close()
			data := readBody(resp.Body)
			if resp.StatusCode != http.StatusOK || json.Unmarshal([]byte(data), &got) != nil {
				t.Fatalf("advanced alias: status=%d body=%s", resp.StatusCode, data)
			}
			if got.RequestedModel != "gemini-advanced" || !containsModel(models, got.Model) {
				t.Fatalf("advanced alias response = %#v", got)
			}
			if got.Model != pro.ID {
				t.Logf("Gemini routed Pro alias to %s because %s was unavailable", got.Model, pro.ID)
			}
		})
	}

	t.Run("OpenAI SSE", func(t *testing.T) {
		time.Sleep(liveRequestPause)
		body := map[string]any{"model": models[0].ID, "stream": true,
			"messages": []map[string]string{{"role": "user", "content": "Reply: STREAM OK"}}}
		resp := postJSON(t, client, baseURL+"/openai/v1/chat/completions", body)
		defer resp.Body.Close()
		data := readBody(resp.Body)
		if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") ||
			!strings.Contains(data, `"chat.completion.chunk"`) || !strings.Contains(data, "data: [DONE]") {
			t.Fatalf("invalid OpenAI SSE: status=%d type=%q body=%s", resp.StatusCode, resp.Header.Get("Content-Type"), data)
		}
	})

	t.Run("Gemini native JSON and SSE", func(t *testing.T) {
		time.Sleep(liveRequestPause)
		body := map[string]any{"contents": []map[string]any{{"role": "user", "parts": []map[string]string{{"text": "Reply: GEMINI OK"}}}}}
		var generated struct {
			Candidates []json.RawMessage `json:"candidates"`
		}
		postJSONInto(t, client, baseURL+"/gemini/v1beta/models/"+models[0].ID+":generateContent", body, http.StatusOK, &generated)
		if len(generated.Candidates) == 0 {
			t.Fatal("Gemini native response has no candidates")
		}
		resp := postJSON(t, client, baseURL+"/gemini/v1beta/models/"+models[0].ID+":streamGenerateContent?alt=sse", body)
		defer resp.Body.Close()
		data := readBody(resp.Body)
		if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") || !strings.Contains(data, "data: {") {
			t.Fatalf("invalid Gemini SSE: status=%d type=%q body=%s", resp.StatusCode, resp.Header.Get("Content-Type"), data)
		}
	})

	t.Run("Anthropic message", func(t *testing.T) {
		time.Sleep(liveRequestPause)
		var got struct {
			Model   string `json:"model"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		}
		postJSONInto(t, client, baseURL+"/claude/v1/messages", map[string]any{
			"model": models[0].ID, "max_tokens": 32,
			"messages": []map[string]string{{"role": "user", "content": "Reply: CLAUDE OK"}},
		}, http.StatusOK, &got)
		if got.Model != models[0].ID || len(got.Content) == 0 || strings.TrimSpace(got.Content[0].Text) == "" {
			t.Fatalf("invalid Anthropic response: %#v", got)
		}
	})
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate E2E test source")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

func startServer(t *testing.T, root string) (string, func()) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()

	exe := filepath.Join(t.TempDir(), "gemini-e2e-server")
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	build := exec.Command("go", "build", "-o", exe, "./cmd/server")
	build.Dir = root
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build E2E server: %v\n%s", err, output)
	}

	logPath := filepath.Join(t.TempDir(), "server.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), fmt.Sprintf("PORT=%d", port), "RATE_LIMIT_ENABLED=false")
	cmd.Stdout, cmd.Stderr = logFile, logFile
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		t.Fatal(err)
	}
	stop := func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
			_, _ = cmd.Process.Wait()
		}
		_ = logFile.Close()
		if t.Failed() {
			if logs, err := os.ReadFile(logPath); err == nil {
				t.Logf("server log:\n%s", logs)
			}
		}
	}
	return fmt.Sprintf("http://127.0.0.1:%d", port), stop
}

func waitForModels(t *testing.T, client *http.Client, baseURL string) []model {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/openai/v1/models", nil)
		resp, err := client.Do(req)
		cancel()
		if err == nil {
			var result modelList
			decodeErr := json.NewDecoder(resp.Body).Decode(&result)
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK && decodeErr == nil && len(result.Data) > 0 {
				return result.Data
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("server did not publish a non-empty model registry within 30 seconds")
	return nil
}

func postJSON(t *testing.T, client *http.Client, url string, value any) *http.Response {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	return resp
}

func postJSONInto(t *testing.T, client *http.Client, url string, input any, wantStatus int, output any) {
	t.Helper()
	resp := postJSON(t, client, url, input)
	defer resp.Body.Close()
	data := readBody(resp.Body)
	if resp.StatusCode != wantStatus {
		t.Fatalf("POST %s: status=%d want=%d body=%s", url, resp.StatusCode, wantStatus, data)
	}
	if err := json.Unmarshal([]byte(data), output); err != nil {
		t.Fatalf("decode POST %s: %v; body=%s", url, err, data)
	}
}

func getJSON(t *testing.T, client *http.Client, url string, wantStatus int, output any) {
	t.Helper()
	resp, err := client.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data := readBody(resp.Body)
	if resp.StatusCode != wantStatus {
		t.Fatalf("GET %s: status=%d want=%d body=%s", url, resp.StatusCode, wantStatus, data)
	}
	if err := json.Unmarshal([]byte(data), output); err != nil {
		t.Fatalf("decode GET %s: %v; body=%s", url, err, data)
	}
}

func readBody(reader io.Reader) string {
	data, _ := io.ReadAll(reader)
	return string(data)
}

func findModelByDisplay(models []model, value string) (model, bool) {
	value = strings.ToLower(value)
	for _, item := range models {
		if strings.Contains(strings.ToLower(item.DisplayName), value) {
			return item, true
		}
	}
	return model{}, false
}

func containsModel(models []model, id string) bool {
	for _, item := range models {
		if item.ID == id {
			return true
		}
	}
	return false
}
