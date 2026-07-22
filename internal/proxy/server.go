// Package proxy implements the Cloak OpenAI/Anthropic-compatible gateway.
package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/PrateekKumar1709/cloak/internal/audit"
	"github.com/PrateekKumar1709/cloak/internal/config"
	"github.com/PrateekKumar1709/cloak/internal/detect"
	"github.com/PrateekKumar1709/cloak/internal/entmap"
	"github.com/PrateekKumar1709/cloak/internal/lemonade"
	"github.com/PrateekKumar1709/cloak/internal/metrics"
	"github.com/PrateekKumar1709/cloak/internal/policy"
	"github.com/PrateekKumar1709/cloak/internal/web"
)

// Server is the Cloak gateway.
type Server struct {
	cfg        *config.Config
	pipeline   *detect.Pipeline
	entities   *entmap.Store
	policy     *policy.Engine
	audit      *audit.Store
	httpClient *http.Client
	lemonade   *lemonade.Client
	log        *slog.Logger
	httpServer *http.Server

	hwMu    sync.Mutex
	hwCache *hardwareInfo
	hwAt    time.Time
}

// New constructs a gateway server from config.
func New(cfg *config.Config, log *slog.Logger) (*Server, error) {
	if log == nil {
		log = slog.Default()
	}

	var tier2 *detect.Tier2Runner
	var lem *lemonade.Client
	if cfg.Lemonade.Enabled {
		timeout := time.Duration(cfg.Lemonade.TimeoutMS) * time.Millisecond
		if timeout <= 0 {
			timeout = 800 * time.Millisecond
		}
		lem = lemonade.New(cfg.Lemonade.BaseURL, cfg.Lemonade.Model, timeout+2*time.Second)
		tier2 = &detect.Tier2Runner{Client: lem, Timeout: timeout}
	}

	pipe := detect.NewPipeline(detect.PipelineConfig{
		Watchlist:          cfg.Watchlist,
		Allowlist:          cfg.Allowlist,
		Tier2FailOpenSoft:  cfg.Detection.Tier2FailOpenSoft,
		CacheMessageHashes: cfg.Detection.CacheMessageHashes,
	}, tier2)

	dataDir := ""
	if cfg.Audit || cfg.Persist {
		dataDir = cfg.DataDir
	}
	aud, err := audit.Open(dataDir)
	if err != nil {
		return nil, fmt.Errorf("audit store: %w", err)
	}

	s := &Server{
		cfg:      cfg,
		pipeline: pipe,
		entities: entmap.NewStore(),
		policy:   policy.New(cfg.Policy),
		audit:    aud,
		lemonade: lem,
		log:      log,
		httpClient: &http.Client{
			Timeout: 10 * time.Minute, // long-lived streams
		},
	}
	return s, nil
}

// Handler returns the root HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /{$}", s.handleDashboard)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("GET /metrics", metrics.Handler().ServeHTTP)

	// OpenAI-compatible
	mux.HandleFunc("POST /v1/chat/completions", s.handleChatCompletions)
	mux.HandleFunc("GET /v1/models", s.handleModels)
	mux.HandleFunc("POST /v1/responses", s.handleResponsesPassthrough)
	mux.HandleFunc("POST /v1/audio/transcriptions", s.handleAudioTranscriptions)
	mux.HandleFunc("POST /v1/embeddings", s.handleEmbeddingsPassthrough)

	// Anthropic-compatible (Claude Code)
	mux.HandleFunc("POST /anthropic/v1/messages", s.handleAnthropicMessages)
	mux.HandleFunc("POST /v1/messages", s.handleAnthropicMessages) // alias

	// Dashboard API
	mux.HandleFunc("GET /api/events", s.handleEventsSSE)
	mux.HandleFunc("GET /api/stats", s.handleStats)
	mux.HandleFunc("GET /api/entities", s.handleEntities)
	mux.HandleFunc("GET /api/policy", s.handleGetPolicy)
	mux.HandleFunc("PUT /api/policy", s.handlePutPolicy)
	mux.HandleFunc("GET /api/settings", s.handleSettings)
	mux.HandleFunc("GET /api/hardware", s.handleHardware)
	mux.HandleFunc("POST /api/test", s.handleTest)

	mux.Handle("GET /static/", http.FileServer(http.FS(web.Static)))

	return withLogging(s.log, mux)
}

// ListenAndServe starts the HTTP server.
func (s *Server) ListenAndServe() error {
	s.httpServer = &http.Server{
		Addr:              s.cfg.Listen,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	s.log.Info("cloak listening", "addr", s.cfg.Listen)
	s.warmPrivateModel()
	return s.httpServer.ListenAndServe()
}

// Shutdown gracefully stops the server.
func (s *Server) Shutdown(ctx context.Context) error {
	if s.httpServer == nil {
		return nil
	}
	err := s.httpServer.Shutdown(ctx)
	_ = s.audit.Close()
	return err
}

// Pipeline exposes the detector for CLI test.
func (s *Server) Pipeline() *detect.Pipeline { return s.pipeline }

// Entities exposes the entity store.
func (s *Server) Entities() *entmap.Store { return s.entities }

// PolicyEngine exposes the policy engine.
func (s *Server) PolicyEngine() *policy.Engine { return s.policy }

// AuditStore exposes audit.
func (s *Server) AuditStore() *audit.Store { return s.audit }

// LemonadeClient exposes the detector client.
func (s *Server) LemonadeClient() *lemonade.Client { return s.lemonade }

// Config returns config.
func (s *Server) Config() *config.Config { return s.cfg }

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	data, err := web.Static.ReadFile("static/index.html")
	if err != nil {
		http.Error(w, "dashboard missing", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(data)
}

func (s *Server) handleEventsSSE(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "sse unsupported", http.StatusInternalServerError)
		return
	}

	// send recent history first
	for _, ev := range s.audit.Recent(50) {
		b, _ := json.Marshal(ev)
		_, _ = fmt.Fprintf(w, "data: %s\n\n", b)
	}
	flusher.Flush()

	ch, cancel := s.audit.Subscribe()
	defer cancel()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			// strip real values for live feed
			for i := range ev.Redactions {
				ev.Redactions[i].Real = ""
			}
			b, _ := json.Marshal(ev)
			_, _ = fmt.Fprintf(w, "data: %s\n\n", b)
			flusher.Flush()
		}
	}
}

func (s *Server) handleStats(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, s.audit.Stats())
}

func (s *Server) handleEntities(w http.ResponseWriter, r *http.Request) {
	reveal := r.URL.Query().Get("reveal") == "1"
	rows := s.entities.ListEntities()
	type row struct {
		SessionID   string `json:"session_id"`
		Placeholder string `json:"placeholder"`
		RealMasked  string `json:"real_masked"`
		Real        string `json:"real,omitempty"`
		Category    string `json:"category"`
	}
	out := make([]row, len(rows))
	for i, e := range rows {
		out[i] = row{
			SessionID:   e.SessionID,
			Placeholder: e.Placeholder,
			RealMasked:  audit.Mask(e.Real),
			Category:    e.Category,
		}
		if reveal {
			out[i].Real = e.Real
		}
	}
	writeJSON(w, out)
}

func (s *Server) handleGetPolicy(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, s.policy.All())
}

func (s *Server) handlePutPolicy(w http.ResponseWriter, r *http.Request) {
	var body map[string]config.Action
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	for k, v := range body {
		s.policy.Set(k, v)
	}
	writeJSON(w, s.policy.All())
}

func (s *Server) handleSettings(w http.ResponseWriter, _ *http.Request) {
	loaded := ""
	if s.lemonade != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		loaded, _ = s.lemonade.LoadedModel(ctx)
		cancel()
	}
	writeJSON(w, map[string]any{
		"listen":            s.cfg.Listen,
		"upstream":          s.cfg.Upstream.BaseURL,
		"lemonade":          s.cfg.Lemonade.BaseURL,
		"lemonade_model":    s.cfg.Lemonade.Model,
		"lemonade_loaded":   loaded,
		"lemonade_enabled":  s.cfg.Lemonade.Enabled,
		"whisper_model":     s.cfg.Lemonade.WhisperModel,
		"omni_asr":          s.cfg.Lemonade.OmniASR,
		"private_mode":      s.cfg.Lemonade.PrivateMode,
		"chat_model":        s.cfg.Lemonade.PrivateChatModel(),
		"has_openai_key":    s.cfg.UpstreamAPIKey() != "",
		"has_anthropic_key": s.cfg.AnthropicKey() != "",
		"engine":            "lemonade",
	})
}

func (s *Server) handleEmbeddingsPassthrough(w http.ResponseWriter, r *http.Request) {
	// v1: pass through untouched with a warning header (no PII transform on vectors).
	body, err := io.ReadAll(io.LimitReader(r.Body, 16<<20))
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}
	upstreamURL := strings.TrimRight(s.cfg.Upstream.BaseURL, "/") + "/embeddings"
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, upstreamURL, bytes.NewReader(body))
	if err != nil {
		http.Error(w, "upstream", http.StatusBadGateway)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if key := s.cfg.UpstreamAPIKey(); key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := s.httpClient.Do(req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	copyHeader(w.Header(), resp.Header)
	w.Header().Set("X-Cloak-Warning", "embeddings are not scanned for PII in v1")
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func (s *Server) handleTest(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	res := s.pipeline.Scan(r.Context(), body.Text)
	dec := s.policy.Evaluate(res.Findings)
	sid := "test"
	s.entities.GetOrCreate(sid)
	toReplace := append(append([]detect.Finding{}, dec.Redact...), dec.Blocked...)
	san, applied := entmap.ApplyFindings(body.Text, toReplace, sid, s.entities)
	writeJSON(w, map[string]any{
		"findings":     res.Findings,
		"latency_ms":   res.Latency,
		"blocked":      !dec.Allowed,
		"block_message": dec.Message,
		"sanitized":    san,
		"applied":      applied,
	})
}

func (s *Server) handleResponsesPassthrough(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 32<<20))
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}
	clientID := r.UserAgent()
	outcome, err := s.sanitizeRequestJSON(r.Context(), body, clientID, r.Header.Get("X-Cloak-Session"))
	if err != nil {
		if be, ok := err.(*blockError); ok {
			writeOpenAIError(w, http.StatusBadRequest, "content_policy_violation", be.dec.Message)
			return
		}
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	upstreamURL := strings.TrimRight(s.cfg.Upstream.BaseURL, "/") + "/responses"
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, upstreamURL, bytes.NewReader(outcome.Body))
	if err != nil {
		http.Error(w, "upstream", http.StatusBadGateway)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if key := s.cfg.UpstreamAPIKey(); key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := s.httpClient.Do(req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	replacer := entmap.NewReplacer(func(pseudo string) (string, bool) {
		return s.entities.Resolve(outcome.SessionID, pseudo)
	})
	copyHeader(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	raw, _ := io.ReadAll(resp.Body)
	_, _ = w.Write(rehydrateJSONBody(raw, replacer, "openai"))
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func withLogging(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		if r.URL.Path != "/api/events" && r.URL.Path != "/metrics" {
			log.Debug("request", "method", r.Method, "path", r.URL.Path, "dur", time.Since(start))
		}
	})
}
