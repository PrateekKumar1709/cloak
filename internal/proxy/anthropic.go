package proxy

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/PrateekKumar1709/cloak/internal/audit"
	"github.com/PrateekKumar1709/cloak/internal/entmap"
	"github.com/PrateekKumar1709/cloak/internal/metrics"
)

func (s *Server) handleAnthropicMessages(w http.ResponseWriter, r *http.Request) {
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
			metrics.BlockedTotal.Inc()
			s.audit.Record(audit.Event{
				Route:       "/anthropic/v1/messages",
				SessionID:   outcome.SessionID,
				Client:      clientID,
				Model:       modelFromBody(body),
				DetectionMS: outcome.DetectionMS,
				Blocked:     true,
				Categories:  outcome.Categories,
				OriginalPreview: outcome.Original,
			})
			writeAnthropicError(w, http.StatusBadRequest, be.dec.Message)
			return
		}
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	metrics.DetectionSeconds.Observe(float64(outcome.DetectionMS) / 1000.0)
	for cat, n := range outcome.Categories {
		metrics.RedactionsTotal.WithLabelValues(cat).Add(float64(n))
	}
	s.audit.Record(audit.Event{
		Route:            "/anthropic/v1/messages",
		SessionID:        outcome.SessionID,
		Client:           clientID,
		Model:            modelFromBody(body),
		DetectionMS:      outcome.DetectionMS,
		Categories:       outcome.Categories,
		Redactions:       briefRedactions(outcome.Applied),
		OriginalPreview:  outcome.Original,
		SanitizedPreview: outcome.Sanitized,
	})

	base := s.cfg.Upstream.AnthropicBaseURL
	if base == "" {
		base = "https://api.anthropic.com"
	}
	upstreamURL := strings.TrimRight(base, "/") + "/v1/messages"
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, upstreamURL, bytes.NewReader(outcome.Body))
	if err != nil {
		http.Error(w, "upstream request", http.StatusBadGateway)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", r.Header.Get("anthropic-version"))
	if req.Header.Get("anthropic-version") == "" {
		req.Header.Set("anthropic-version", "2023-06-01")
	}
	if v := r.Header.Get("anthropic-beta"); v != "" {
		req.Header.Set("anthropic-beta", v)
	}
	key := s.cfg.AnthropicKey()
	if key == "" {
		// fall back to x-api-key from client
		key = r.Header.Get("x-api-key")
	}
	if key != "" {
		req.Header.Set("x-api-key", key)
	}

	resp, err := s.httpClient.Do(req)
	if err != nil {
		metrics.RequestsTotal.WithLabelValues("anthropic.messages", "error").Inc()
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

	stream := isStreaming(outcome.Body)
	if stream {
		rh := entmap.NewStreamRehydrator(replacer)
		_ = transformAnthropicSSE(w, resp.Body, rh)
		if tail := rh.Flush(); tail != "" {
			final := map[string]any{
				"type":  "content_block_delta",
				"index": 0,
				"delta": map[string]any{"type": "text_delta", "text": tail},
			}
			b, _ := json.Marshal(final)
			_, _ = io.WriteString(w, "event: content_block_delta\ndata: "+string(b)+"\n\n")
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
	} else {
		raw, _ := io.ReadAll(resp.Body)
		out := rehydrateJSONBody(raw, replacer, "anthropic")
		_, _ = w.Write(out)
	}

	status := "ok"
	if resp.StatusCode >= 400 {
		status = "upstream_error"
	}
	metrics.RequestsTotal.WithLabelValues("anthropic.messages", status).Inc()
}

func writeAnthropicError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"type": "error",
		"error": map[string]any{
			"type":    "invalid_request_error",
			"message": message,
		},
	})
}
