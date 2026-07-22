package proxy

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/PrateekKumar1709/cloak/internal/audit"
	"github.com/PrateekKumar1709/cloak/internal/detect"
	"github.com/PrateekKumar1709/cloak/internal/entmap"
	"github.com/PrateekKumar1709/cloak/internal/metrics"
)

// handleAudioTranscriptions is the Omni ASR route:
// client → Cloak → local Lemonade Whisper → redact transcript → return.
// Sensitive speech does not need a cloud ASR provider.
func (s *Server) handleAudioTranscriptions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.lemonade == nil || !s.cfg.Lemonade.OmniASR {
		writeOpenAIError(w, http.StatusServiceUnavailable, "omni_disabled",
			"Cloak Omni ASR requires lemonade.omni_asr=true and a running Lemonade server with Whisper")
		return
	}

	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "expected multipart form with file")
		return
	}
	file, hdr, err := r.FormFile("file")
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "missing file field")
		return
	}
	defer file.Close()
	wav, err := io.ReadAll(io.LimitReader(file, 32<<20))
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "read file failed")
		return
	}

	model := r.FormValue("model")
	if model == "" || strings.HasPrefix(model, "whisper-") || model == "whisper-1" {
		// Map OpenAI default names → Lemonade registry
		model = s.cfg.Lemonade.WhisperModel
		if model == "" {
			model = "Whisper-Tiny"
		}
	}
	language := r.FormValue("language")

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()

	start := time.Now()
	tr, err := s.lemonade.Transcribe(ctx, model, hdr.Filename, wav, language)
	asrMS := time.Since(start).Milliseconds()
	if err != nil {
		s.log.Error("lemonade whisper failed", "err", err)
		writeOpenAIError(w, http.StatusBadGateway, "asr_error", "Lemonade Whisper failed: "+err.Error())
		return
	}

	text := strings.TrimSpace(tr.Text)
	sessionHeader := r.Header.Get("X-Cloak-Session")
	clientID := r.Header.Get("X-Cloak-Client")
	if clientID == "" {
		clientID = r.UserAgent()
	}
	sessionID := entmap.SessionID(clientID, text, sessionHeader)
	s.entities.GetOrCreate(sessionID)

	detectStart := time.Now()
	res := s.pipeline.Scan(ctx, text)
	detectMS := time.Since(detectStart).Milliseconds()
	dec := s.policy.Evaluate(res.Findings)
	if !dec.Allowed {
		metrics.BlockedTotal.Inc()
		s.audit.Record(audit.Event{
			Route:           "/v1/audio/transcriptions",
			SessionID:       sessionID,
			Client:          clientID,
			Model:           model,
			DetectionMS:     detectMS + asrMS,
			Blocked:         true,
			Categories:      countFindings(dec.Blocked),
			OriginalPreview: truncate(text, 500),
		})
		writeOpenAIError(w, http.StatusBadRequest, "content_policy_violation", dec.Message)
		return
	}

	san, applied := entmap.ApplyFindings(text, dec.Redact, sessionID, s.entities)
	cats := map[string]int{}
	for _, a := range applied {
		cats[a.Category]++
		metrics.RedactionsTotal.WithLabelValues(a.Category).Inc()
	}
	metrics.DetectionSeconds.Observe(float64(detectMS) / 1000.0)
	metrics.RequestsTotal.WithLabelValues("audio.transcriptions", "ok").Inc()

	s.audit.Record(audit.Event{
		Route:            "/v1/audio/transcriptions",
		SessionID:        sessionID,
		Client:           clientID,
		Model:            model,
		DetectionMS:      detectMS + asrMS,
		Categories:       cats,
		Redactions:       briefRedactions(applied),
		OriginalPreview:  truncate(text, 500),
		SanitizedPreview: truncate(san, 500),
	})

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Cloak-Detection-Ms", itoa(detectMS))
	w.Header().Set("X-Cloak-ASR-Ms", itoa(asrMS))
	w.Header().Set("X-Cloak-Session", sessionID)
	w.Header().Set("X-Cloak-Omni", "lemonade-whisper")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"text": san,
		"cloak": map[string]any{
			"original_text": text,
			"redacted":      len(applied) > 0,
			"asr_ms":        asrMS,
			"detection_ms":  detectMS,
			"engine":        "lemonade-whisper",
			"model":         model,
		},
	})
}

func countFindings(findings []detect.Finding) map[string]int {
	out := map[string]int{}
	for _, f := range findings {
		out[string(f.Category)]++
	}
	return out
}
