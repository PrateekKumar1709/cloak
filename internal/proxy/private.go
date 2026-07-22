package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/PrateekKumar1709/cloak/internal/audit"
	"github.com/PrateekKumar1709/cloak/internal/metrics"
)

// privateModeAvailable reports whether crown-jewel prompts can be answered locally.
func (s *Server) privateModeAvailable() bool {
	return s.cfg.Lemonade.Enabled && s.cfg.Lemonade.PrivateMode && s.lemonade != nil
}

// serveLocalChat answers a request entirely on the local Lemonade model.
// The ORIGINAL (un-redacted) body is used; it is not forwarded upstream, so
// no rehydration is needed. This is Cloak's "Private Mode": crown-jewel prompts
// get a real answer without a cloud round-trip.
func (s *Server) serveLocalChat(w http.ResponseWriter, r *http.Request, origBody []byte, sessionID, clientID string, cats map[string]int, detectionMS int64, origPreview string) {
	chatModel := s.cfg.Lemonade.PrivateChatModel()

	// Rewrite the model to the local chat model; keep everything else intact.
	var root map[string]any
	if err := json.Unmarshal(origBody, &root); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "invalid JSON body")
		return
	}
	root["model"] = chatModel
	stream, _ := root["stream"].(bool)
	localBody, _ := json.Marshal(root)

	ctx := r.Context()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.lemonade.ChatEndpoint(), bytes.NewReader(localBody))
	if err != nil {
		writeOpenAIError(w, http.StatusBadGateway, "api_error", "local model request failed")
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer lemonade")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		s.log.Warn("private mode: local model error", "err", err)
		writeOpenAIError(w, http.StatusBadGateway, "api_error", "Cloak Private Mode: local model unavailable; start Lemonade to answer sensitive prompts on-device")
		return
	}
	defer resp.Body.Close()

	s.audit.Record(audit.Event{
		Route:           "/v1/chat/completions",
		SessionID:       sessionID,
		Client:          clientID,
		Model:           chatModel + " (local)",
		DetectionMS:     detectionMS,
		Private:         true,
		Categories:      cats,
		OriginalPreview: origPreview,
		SanitizedPreview: "↩ answered locally by Lemonade (not forwarded to cloud)",
	})
	metrics.RequestsTotal.WithLabelValues("chat.completions", "local_private").Inc()

	copyHeader(w.Header(), resp.Header)
	w.Header().Set("X-Cloak-Route", "local-private")
	w.Header().Set("X-Cloak-Private", "lemonade")
	w.Header().Set("X-Cloak-Detection-Ms", itoa(detectionMS))
	w.Header().Set("X-Cloak-Session", sessionID)
	w.WriteHeader(resp.StatusCode)

	if stream && strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		// Local answer: no rehydration needed, stream straight through.
		fw, _ := w.(http.Flusher)
		buf := make([]byte, 4096)
		for {
			n, rerr := resp.Body.Read(buf)
			if n > 0 {
				_, _ = w.Write(buf[:n])
				if fw != nil {
					fw.Flush()
				}
			}
			if rerr != nil {
				break
			}
		}
		return
	}
	_, _ = io.Copy(w, resp.Body)
}

// warmPrivateModel best-effort loads the chat model so first Private Mode
// answer is not cold. Non-blocking; safe to ignore errors.
func (s *Server) warmPrivateModel() {
	if !s.privateModeAvailable() {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_, _ = s.lemonade.LoadedModel(ctx)
	}()
}
