package proxy

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/PrateekKumar1709/cloak/internal/audit"
	"github.com/PrateekKumar1709/cloak/internal/entmap"
	"github.com/PrateekKumar1709/cloak/internal/metrics"
)

func (s *Server) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 32<<20))
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}

	clientID := r.Header.Get("X-Cloak-Client")
	if clientID == "" {
		clientID = r.UserAgent()
	}
	sessionHeader := r.Header.Get("X-Cloak-Session")

	outcome, err := s.sanitizeRequestJSON(r.Context(), body, clientID, sessionHeader)
	if err != nil {
		if be, ok := err.(*blockError); ok {
			// Private Mode: instead of blocking crown-jewel prompts, answer them
			// entirely on the local Lemonade model, nothing leaves the machine.
			if s.privateModeAvailable() {
				cats := outcome.Categories
				if cats == nil {
					cats = map[string]int{}
				}
				for _, f := range be.dec.Blocked {
					cats[string(f.Category)]++
				}
				s.serveLocalChat(w, r, body, outcome.SessionID, clientID, cats, outcome.DetectionMS, outcome.Original)
				return
			}
			metrics.BlockedTotal.Inc()
			s.audit.Record(audit.Event{
				Route:       "/v1/chat/completions",
				SessionID:   outcome.SessionID,
				Client:      clientID,
				Model:       modelFromBody(body),
				DetectionMS: outcome.DetectionMS,
				Blocked:     true,
				Categories:  outcome.Categories,
				OriginalPreview: outcome.Original,
			})
			writeOpenAIError(w, http.StatusBadRequest, "content_policy_violation", be.dec.Message)
			return
		}
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	metrics.DetectionSeconds.Observe(float64(outcome.DetectionMS) / 1000.0)
	for cat, n := range outcome.Categories {
		metrics.RedactionsTotal.WithLabelValues(cat).Add(float64(n))
	}

	ev := s.audit.Record(audit.Event{
		Route:            "/v1/chat/completions",
		SessionID:        outcome.SessionID,
		Client:           clientID,
		Model:            modelFromBody(body),
		DetectionMS:      outcome.DetectionMS,
		Categories:       outcome.Categories,
		Redactions:       briefRedactions(outcome.Applied),
		OriginalPreview:  outcome.Original,
		SanitizedPreview: outcome.Sanitized,
	})
	_ = ev

	upstreamURL := strings.TrimRight(s.cfg.Upstream.BaseURL, "/") + "/chat/completions"
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, upstreamURL, bytes.NewReader(outcome.Body))
	if err != nil {
		http.Error(w, "upstream request", http.StatusBadGateway)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if key := s.cfg.UpstreamAPIKey(); key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	// forward select headers
	if v := r.Header.Get("OpenAI-Organization"); v != "" {
		req.Header.Set("OpenAI-Organization", v)
	}

	stream := isStreaming(outcome.Body)
	start := time.Now()
	resp, err := s.httpClient.Do(req)
	if err != nil {
		metrics.RequestsTotal.WithLabelValues("chat.completions", "error").Inc()
		http.Error(w, "upstream: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	replacer := entmap.NewReplacer(func(pseudo string) (string, bool) {
		return s.entities.Resolve(outcome.SessionID, pseudo)
	})

	copyHeader(w.Header(), resp.Header)
	w.Header().Set("X-Cloak-Detection-Ms", itoa(outcome.DetectionMS))
	w.Header().Set("X-Cloak-Session", outcome.SessionID)
	w.WriteHeader(resp.StatusCode)

	if stream && strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		rh := entmap.NewStreamRehydrator(replacer)
		_ = transformOpenAISSE(w, resp.Body, rh)
		// emit leftover hold-back as a final tiny delta if needed
		if tail := rh.Flush(); tail != "" {
			final := map[string]any{
				"id": "cloak-flush", "object": "chat.completion.chunk",
				"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": tail}, "finish_reason": nil}},
			}
			b, _ := json.Marshal(final)
			_, _ = io.WriteString(w, "data: "+string(b)+"\n\n")
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
	} else {
		raw, _ := io.ReadAll(resp.Body)
		out := rehydrateJSONBody(raw, replacer, "openai")
		_, _ = w.Write(out)
	}

	status := "ok"
	if resp.StatusCode >= 400 {
		status = "upstream_error"
	}
	metrics.RequestsTotal.WithLabelValues("chat.completions", status).Inc()
	_ = start
}

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	upstreamURL := strings.TrimRight(s.cfg.Upstream.BaseURL, "/") + "/models"
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, upstreamURL, nil)
	if err != nil {
		http.Error(w, "upstream request", http.StatusBadGateway)
		return
	}
	if key := s.cfg.UpstreamAPIKey(); key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := s.httpClient.Do(req)
	if err != nil {
		http.Error(w, "upstream: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	copyHeader(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func writeOpenAIError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{
			"message": message,
			"type":    code,
			"code":    code,
			"param":   nil,
		},
	})
}

func copyHeader(dst, src http.Header) {
	for k, vs := range src {
		// hop-by-hop + length (body may change after rehydration)
		switch strings.ToLower(k) {
		case "connection", "keep-alive", "transfer-encoding", "te", "trailer", "upgrade",
			"content-length", "content-encoding":
			continue
		}
		for _, v := range vs {
			dst.Add(k, v)
		}
	}
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var neg bool
	if n < 0 {
		neg = true
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
