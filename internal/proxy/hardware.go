package proxy

import (
	"context"
	"net/http"
	"time"
)

// hardwareInfo is the payload for the dashboard "Powered by Lemonade" panel.
type hardwareInfo struct {
	Enabled      bool    `json:"enabled"`
	Device       string  `json:"device"`
	Backend      string  `json:"backend"`
	OS           string  `json:"os"`
	Arch         string  `json:"arch"`
	DetectorModel string `json:"detector_model"`
	ChatModel    string  `json:"chat_model"`
	LoadedModel  string  `json:"loaded_model"`
	PrivateMode  bool    `json:"private_mode"`
	OmniASR      bool    `json:"omni_asr"`
	WhisperModel string  `json:"whisper_model"`
	TokensPerSec float64 `json:"tokens_per_sec"`
	BenchMS      int64   `json:"bench_ms"`
	Healthy      bool    `json:"healthy"`
}

// handleHardware reports the local Lemonade device/backend + a throughput
// benchmark, cached briefly so the panel is cheap to poll.
func (s *Server) handleHardware(w http.ResponseWriter, r *http.Request) {
	s.hwMu.Lock()
	if s.hwCache != nil && time.Since(s.hwAt) < 20*time.Second {
		cached := *s.hwCache
		s.hwMu.Unlock()
		writeJSON(w, cached)
		return
	}
	s.hwMu.Unlock()

	info := &hardwareInfo{
		Enabled:       s.cfg.Lemonade.Enabled,
		DetectorModel: s.cfg.Lemonade.Model,
		ChatModel:     s.cfg.Lemonade.PrivateChatModel(),
		PrivateMode:   s.cfg.Lemonade.PrivateMode,
		OmniASR:       s.cfg.Lemonade.OmniASR,
		WhisperModel:  s.cfg.Lemonade.WhisperModel,
	}

	if s.lemonade != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		sys := s.lemonade.SystemInfo(ctx) // degrades to host guess if server is quiet
		info.Device = sys.Device
		info.Backend = sys.Backend
		info.OS = sys.OS
		info.Arch = sys.Arch
		if err := s.lemonade.Healthy(ctx); err == nil {
			info.Healthy = true
			info.LoadedModel, _ = s.lemonade.LoadedModel(ctx)
			bench := s.lemonade.Benchmark(ctx, benchModel(info))
			info.TokensPerSec = bench.TokensPerSec
			info.BenchMS = bench.TotalMS
		}
	}

	s.hwMu.Lock()
	s.hwCache = info
	s.hwAt = time.Now()
	s.hwMu.Unlock()
	writeJSON(w, info)
}

func benchModel(info *hardwareInfo) string {
	if info.LoadedModel != "" {
		return info.LoadedModel
	}
	return info.DetectorModel
}
