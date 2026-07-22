package proxy

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"regexp"
	"strings"
	"sync"
)

var demoPlaceholderRE = regexp.MustCompile(`\b(?:PERSON|EMAIL|PHONE|KEY|HOST|ORG|ADDR|ID|SSN|CARD|JWT|PKEY|IP|MAC|CODE|MED|FIN|URLCRED)_\d+\b`)

// StartDemoUpstream runs an in-process mock OpenAI/Anthropic upstream for demos.
// Returns base URL like http://127.0.0.1:PORT/v1 and a shutdown func.
func StartDemoUpstream() (baseURL string, anthropicBase string, shutdown func(), err error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", "", nil, err
	}
	var mu sync.Mutex
	var last string

	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/models", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"object": "list",
			"data":   []any{map[string]any{"id": "cloak-demo-gpt", "object": "model"}},
		})
	})
	mux.HandleFunc("GET /_last", func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]string{"text": last})
	})
	handleChat := func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		last = string(body)
		mu.Unlock()
		var root map[string]any
		_ = json.Unmarshal(body, &root)
		text := collectDemoText(root)
		ph := demoPlaceholderRE.FindAllString(text, -1)
		reply := "No sensitive placeholders seen."
		if len(ph) > 0 {
			uniq := uniqueStrings(ph)
			reply = "Acknowledged placeholders: " + strings.Join(uniq, ", ") + "."
		}
		stream, _ := root["stream"].(bool)
		if stream {
			w.Header().Set("Content-Type", "text/event-stream")
			for _, ch := range reply {
				chunk := map[string]any{
					"id": "demo", "object": "chat.completion.chunk",
					"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": string(ch)}}},
				}
				b, _ := json.Marshal(chunk)
				_, _ = w.Write([]byte("data: " + string(b) + "\n\n"))
				if f, ok := w.(http.Flusher); ok {
					f.Flush()
				}
			}
			_, _ = w.Write([]byte("data: [DONE]\n\n"))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "chatcmpl-demo", "object": "chat.completion", "model": "cloak-demo-gpt",
			"choices": []any{map[string]any{
				"index": 0, "finish_reason": "stop",
				"message": map[string]any{"role": "assistant", "content": reply},
			}},
		})
	}
	mux.HandleFunc("POST /v1/chat/completions", handleChat)
	mux.HandleFunc("POST /v1/messages", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		last = string(body)
		mu.Unlock()
		var root map[string]any
		_ = json.Unmarshal(body, &root)
		text := collectDemoText(root)
		ph := demoPlaceholderRE.FindAllString(text, -1)
		reply := "ok"
		if len(ph) > 0 {
			reply = "Acknowledged placeholders: " + strings.Join(uniqueStrings(ph), ", ") + "."
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "msg_demo", "type": "message", "role": "assistant",
			"content": []any{map[string]any{"type": "text", "text": reply}},
			"stop_reason": "end_turn",
		})
	})

	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()

	addr := ln.Addr().String()
	baseURL = "http://" + addr + "/v1"
	anthropicBase = "http://" + addr
	shutdown = func() { _ = srv.Close() }
	return baseURL, anthropicBase, shutdown, nil
}

func collectDemoText(root map[string]any) string {
	var parts []string
	if msgs, ok := root["messages"].([]any); ok {
		for _, m := range msgs {
			mm, _ := m.(map[string]any)
			switch c := mm["content"].(type) {
			case string:
				parts = append(parts, c)
			}
		}
	}
	return strings.Join(parts, "\n")
}

func uniqueStrings(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}
