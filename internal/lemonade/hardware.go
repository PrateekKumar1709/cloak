package lemonade

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"strings"
	"time"
)

// ChatEndpoint is the OpenAI-compatible chat completions URL on Lemonade.
func (c *Client) ChatEndpoint() string { return c.BaseURL + "/chat/completions" }

// SystemInfo describes the hardware Lemonade is running on.
type SystemInfo struct {
	Device  string `json:"device"`  // e.g. "Ryzen AI NPU", "Radeon GPU", "Apple Metal", "CPU"
	Backend string `json:"backend"` // e.g. "llamacpp", "oga", "hf"
	OS      string `json:"os"`
	Arch    string `json:"arch"`
	Raw     any    `json:"raw,omitempty"`
}

// SystemInfo probes Lemonade for the active hardware/backend, degrading
// gracefully to host info when the server does not expose it.
func (c *Client) SystemInfo(ctx context.Context) SystemInfo {
	info := SystemInfo{
		OS:      runtime.GOOS,
		Arch:    runtime.GOARCH,
		Device:  hostDeviceGuess(),
		Backend: "llamacpp",
	}
	// Lemonade exposes machine details at /system-info on some builds.
	for _, path := range []string{"/system-info", "/system_info"} {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+path, nil)
		if err != nil {
			continue
		}
		resp, err := c.HTTPClient.Do(req)
		if err != nil {
			continue
		}
		data, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode >= 300 || len(data) == 0 {
			continue
		}
		var raw map[string]any
		if json.Unmarshal(data, &raw) != nil {
			continue
		}
		info.Raw = raw
		if dev := firstStringField(raw, "device", "Device", "device_name", "gpu", "processor", "cpu"); dev != "" {
			info.Device = dev
		}
		if be := firstStringField(raw, "backend", "Backend", "recipe", "engine"); be != "" {
			info.Backend = be
		}
		break
	}
	return info
}

func hostDeviceGuess() string {
	switch runtime.GOOS {
	case "darwin":
		if runtime.GOARCH == "arm64" {
			return "Apple Silicon (Metal via Lemonade)"
		}
		return "CPU (Metal unavailable)"
	case "windows", "linux":
		// On Ryzen AI PCs Lemonade can target NPU/GPU; surface the class.
		return "AMD Ryzen AI / Radeon (Lemonade-selected)"
	default:
		return "local device"
	}
}

func firstStringField(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			if s, ok := v.(string); ok && s != "" {
				return s
			}
		}
	}
	return ""
}

// BenchResult is a quick local-throughput measurement.
type BenchResult struct {
	Model       string  `json:"model"`
	TokensPerSec float64 `json:"tokens_per_sec"`
	CompletionTokens int `json:"completion_tokens"`
	TotalMS     int64   `json:"total_ms"`
	OK          bool    `json:"ok"`
	Error       string  `json:"error,omitempty"`
}

// Benchmark runs a tiny fixed generation to measure local tokens/sec.
func (c *Client) Benchmark(ctx context.Context, model string) BenchResult {
	if model == "" {
		model = c.Model
	}
	res := BenchResult{Model: model}
	body, _ := json.Marshal(map[string]any{
		"model": model,
		"messages": []map[string]string{
			{"role": "user", "content": "In one sentence, describe why running AI locally protects privacy."},
		},
		"temperature": 0,
		"max_tokens":  64,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.ChatEndpoint(), bytes.NewReader(body))
	if err != nil {
		res.Error = err.Error()
		return res
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer lemonade")
	start := time.Now()
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	res.TotalMS = time.Since(start).Milliseconds()
	if resp.StatusCode >= 300 {
		res.Error = fmt.Sprintf("status %d", resp.StatusCode)
		return res
	}
	var parsed struct {
		Choices []struct {
			Message struct {
				Content          string `json:"content"`
				ReasoningContent string `json:"reasoning_content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if json.Unmarshal(data, &parsed) != nil {
		res.Error = "parse"
		return res
	}
	tokens := parsed.Usage.CompletionTokens
	if tokens == 0 && len(parsed.Choices) > 0 {
		txt := parsed.Choices[0].Message.Content
		if txt == "" {
			txt = parsed.Choices[0].Message.ReasoningContent
		}
		tokens = len(strings.Fields(txt)) // rough fallback
	}
	res.CompletionTokens = tokens
	if res.TotalMS > 0 && tokens > 0 {
		res.TokensPerSec = float64(tokens) / (float64(res.TotalMS) / 1000.0)
	}
	res.OK = res.Error == ""
	return res
}
