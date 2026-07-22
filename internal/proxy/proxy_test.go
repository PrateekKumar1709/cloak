package proxy

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/PrateekKumar1709/cloak/internal/config"
	"github.com/PrateekKumar1709/cloak/internal/detect"
)

func TestChatCompletionsRedactAndRehydrate(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if bytes.Contains(body, []byte("alice@example.com")) {
			t.Errorf("upstream saw raw email: %s", body)
		}
		if !bytes.Contains(body, []byte("EMAIL_")) {
			t.Errorf("upstream missing placeholder: %s", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "chatcmpl-test",
			"choices": []any{
				map[string]any{
					"index": 0,
					"message": map[string]any{
						"role":    "assistant",
						"content": "I will email EMAIL_1 shortly.",
					},
					"finish_reason": "stop",
				},
			},
		})
	}))
	defer upstream.Close()

	cfg := config.Default()
	cfg.Upstream.BaseURL = upstream.URL + "/v1"
	cfg.Upstream.APIKey = "test"
	cfg.Lemonade.Enabled = false
	cfg.Audit = false
	cfg.Persist = false

	srv, err := New(cfg, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	payload := map[string]any{
		"model": "gpt-test",
		"messages": []any{
			map[string]any{"role": "user", "content": "Write to alice@example.com please"},
		},
	}
	raw, _ := json.Marshal(payload)
	resp, err := http.Post(ts.URL+"/v1/chat/completions", "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		t.Fatalf("status %d: %s", resp.StatusCode, out)
	}
	if !bytes.Contains(out, []byte("alice@example.com")) {
		t.Fatalf("response not rehydrated: %s", out)
	}
	if bytes.Contains(out, []byte("EMAIL_1")) {
		t.Fatalf("placeholder leaked to client: %s", out)
	}
}

func TestBlockPolicy(t *testing.T) {
	cfg := config.Default()
	cfg.Lemonade.Enabled = false
	cfg.Audit = false
	cfg.Upstream.BaseURL = "http://127.0.0.1:1/v1"
	srv, err := New(cfg, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	payload := `{"model":"x","messages":[{"role":"user","content":"key AKIAIOSFODNN7EXAMPLE"}]}`
	resp, err := http.Post(ts.URL+"/v1/chat/completions", "application/json", strings.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == 200 {
		t.Fatalf("expected block, got 200: %s", body)
	}
	if !bytes.Contains(body, []byte("blocked")) && !bytes.Contains(body, []byte("AWS_KEY")) {
		t.Fatalf("unexpected error body: %s", body)
	}
}

func TestStreamingRehydrate(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		chunks := []string{"Hello ", "PERSON", "_1", "!"}
		for _, c := range chunks {
			evt := map[string]any{
				"id": "c", "object": "chat.completion.chunk",
				"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": c}}},
			}
			b, _ := json.Marshal(evt)
			_, _ = w.Write([]byte("data: " + string(b) + "\n\n"))
			fl.Flush()
		}
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer upstream.Close()

	cfg := config.Default()
	cfg.Upstream.BaseURL = upstream.URL + "/v1"
	cfg.Upstream.APIKey = "test"
	cfg.Lemonade.Enabled = false
	cfg.Audit = false

	srv, err := New(cfg, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	// seed entity map for the session header the request will use
	header := "stream-test"
	sid := "hdr:" + header
	srv.entities.GetOrCreate(sid)
	_ = srv.entities.Assign(sid, "Bob", detect.CatPerson)

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	payload := `{"model":"x","stream":true,"messages":[{"role":"user","content":"hi there"}]}`
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/v1/chat/completions", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Cloak-Session", header)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if !bytes.Contains(raw, []byte("Bob")) {
		t.Fatalf("expected rehydrated stream, got %s", raw)
	}
}
