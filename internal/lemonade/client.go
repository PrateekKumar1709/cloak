// Package lemonade talks to the local Lemonade OpenAI-compatible server.
package lemonade

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"path/filepath"
	"strings"
	"time"
)

// Client calls Lemonade for Tier-2 NER.
type Client struct {
	BaseURL    string
	Model      string
	HTTPClient *http.Client
}

// New creates a Lemonade client.
func New(baseURL, model string, timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		Model:   model,
		HTTPClient: &http.Client{
			// Allow long first-token / model-load; per-call context still bounds NER.
			Timeout: timeout,
		},
	}
}

// Entity is a span extracted by the local model.
type Entity struct {
	Text       string  `json:"text"`
	Category   string  `json:"category"`
	Confidence float64 `json:"confidence"`
}

// ModelInfo is a Lemonade /models entry.
type ModelInfo struct {
	ID         string `json:"id"`
	Downloaded bool   `json:"downloaded"`
	Recipe     string `json:"recipe"`
	Size       float64 `json:"size"`
}

const systemPrompt = `You extract sensitive named entities for a privacy firewall.
Reply with ONLY a JSON array (no markdown, no commentary, no chain-of-thought) of objects:
[{"text":"<exact substring>","category":"PERSON|ORG|ADDRESS|PROJECT_CODENAME|INTERNAL_HOSTNAME|MEDICAL|FINANCIAL|OTHER_ID","confidence":0.0-1.0}]
Rules:
- text MUST be copied verbatim from the input (exact contiguous substring).
- Do not invent entities. If none, return [].
- Skip emails, phone numbers, SSNs, credit cards, API keys, JWTs; handled elsewhere.
- PERSON = human names; ORG = companies/orgs; INTERNAL_HOSTNAME = internal hostnames;
  PROJECT_CODENAME = internal project/codenames; ADDRESS = street addresses;
  MEDICAL/FINANCIAL = clinical/financial identifiers; OTHER_ID = other personal IDs.
- Do not explain your reasoning. Output the JSON array immediately.`

const fewShotUser = `Text: Call Alice Johnson at Orion Labs regarding Project Falcon on host api-internal-7.
JSON:`

const fewShotAssistant = `[{"text":"Alice Johnson","category":"PERSON","confidence":0.95},{"text":"Orion Labs","category":"ORG","confidence":0.9},{"text":"Project Falcon","category":"PROJECT_CODENAME","confidence":0.92},{"text":"api-internal-7","category":"INTERNAL_HOSTNAME","confidence":0.9}]`

// ExtractEntities runs Tier-2 NER on content.
func (c *Client) ExtractEntities(ctx context.Context, content string) ([]Entity, error) {
	if content == "" {
		return nil, nil
	}
	// Prefer few-shot JSON without response_format; more reliable on small GGUF models.
	entities, err := c.chat(ctx, content, false)
	if err == nil {
		return entities, nil
	}
	// Retry once with json_object hint if the first parse failed.
	entities, err2 := c.chat(ctx, content, true)
	if err2 != nil {
		return nil, fmt.Errorf("lemonade ner: %v (retry: %w)", err, err2)
	}
	return entities, nil
}

func (c *Client) chat(ctx context.Context, content string, useJSONFormat bool) ([]Entity, error) {
	body := map[string]any{
		"model": c.Model,
		"messages": []map[string]string{
			{"role": "system", "content": systemPrompt},
			{"role": "user", "content": fewShotUser},
			{"role": "assistant", "content": fewShotAssistant},
			{"role": "user", "content": "Text: " + content + "\nJSON:"},
		},
		"temperature": 0,
		"max_tokens":  512,
	}
	if useJSONFormat {
		body["response_format"] = map[string]string{"type": "json_object"}
	}

	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/chat/completions", bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer lemonade")

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, truncate(string(data), 200))
	}

	var parsed struct {
		Choices []struct {
			Message struct {
				Content          string `json:"content"`
				ReasoningContent string `json:"reasoning_content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, err
	}
	if len(parsed.Choices) == 0 {
		return nil, fmt.Errorf("empty choices")
	}
	msg := parsed.Choices[0].Message
	// Prefer content; some "thinking" models (e.g. Qwen3.5-35B-A3B) put
	// useful text only in reasoning_content and leave content empty.
	text := strings.TrimSpace(msg.Content)
	if text == "" {
		text = strings.TrimSpace(msg.ReasoningContent)
	}
	if text == "" {
		return nil, fmt.Errorf("empty model content")
	}
	return parseEntities(text)
}

func parseEntities(content string) ([]Entity, error) {
	content = strings.TrimSpace(content)
	// strip thinking / preamble common in some models
	if i := strings.Index(content, "["); i >= 0 {
		if j := strings.LastIndex(content, "]"); j > i {
			// keep full content for wrap parse first; salvage below uses slice
		}
	}
	if strings.HasPrefix(content, "```") {
		content = strings.TrimPrefix(content, "```json")
		content = strings.TrimPrefix(content, "```")
		content = strings.TrimSuffix(strings.TrimSpace(content), "```")
		content = strings.TrimSpace(content)
	}
	var arr []Entity
	if err := json.Unmarshal([]byte(content), &arr); err == nil {
		return arr, nil
	}
	var wrap struct {
		Entities []Entity `json:"entities"`
	}
	if err := json.Unmarshal([]byte(content), &wrap); err == nil && wrap.Entities != nil {
		return wrap.Entities, nil
	}
	start := strings.Index(content, "[")
	end := strings.LastIndex(content, "]")
	if start >= 0 && end > start {
		if err := json.Unmarshal([]byte(content[start:end+1]), &arr); err == nil {
			return arr, nil
		}
	}
	return nil, fmt.Errorf("parse entities: %s", truncate(content, 160))
}

// Healthy checks whether the Lemonade server responds.
func (c *Client) Healthy(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/health", nil)
	if err != nil {
		return err
	}
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		// fallback to /models for older builds
		req2, err2 := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/models", nil)
		if err2 != nil {
			return err
		}
		resp2, err2 := c.HTTPClient.Do(req2)
		if err2 != nil {
			return err
		}
		defer resp2.Body.Close()
		if resp2.StatusCode >= 300 {
			return fmt.Errorf("models status %d", resp2.StatusCode)
		}
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("health status %d", resp.StatusCode)
	}
	return nil
}

// ListModels returns registered models from Lemonade.
func (c *Client) ListModels(ctx context.Context, downloadedOnly bool) ([]ModelInfo, error) {
	url := c.BaseURL + "/models"
	if !downloadedOnly {
		url += "?show_all=true"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("models %d: %s", resp.StatusCode, truncate(string(data), 120))
	}
	var parsed struct {
		Data []ModelInfo `json:"data"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, err
	}
	return parsed.Data, nil
}

// Pull downloads a model by registered name.
func (c *Client) Pull(ctx context.Context, model string) error {
	body, _ := json.Marshal(map[string]string{"model_name": model})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/pull", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("pull %d: %s", resp.StatusCode, truncate(string(data), 200))
	}
	return nil
}

// Load loads a model into memory.
func (c *Client) Load(ctx context.Context, model string) error {
	body, _ := json.Marshal(map[string]string{"model_name": model})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/load", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("load %d: %s", resp.StatusCode, truncate(string(data), 200))
	}
	return nil
}

// Transcription is an OpenAI-compatible ASR result.
type Transcription struct {
	Text string `json:"text"`
}

// Transcribe runs Lemonade Whisper (omni ASR) on a WAV file body.
// model should be e.g. Whisper-Tiny / Whisper-Base (Lemonade registry name).
func (c *Client) Transcribe(ctx context.Context, model, filename string, wav []byte, language string) (*Transcription, error) {
	if model == "" {
		model = "Whisper-Tiny"
	}
	if filename == "" {
		filename = "audio.wav"
	}
	if !strings.HasSuffix(strings.ToLower(filename), ".wav") {
		filename = strings.TrimSuffix(filename, filepath.Ext(filename)) + ".wav"
	}

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)

	h := make(textproto.MIMEHeader)
	h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename="%s"`, filename))
	h.Set("Content-Type", "audio/wav")
	part, err := w.CreatePart(h)
	if err != nil {
		return nil, err
	}
	if _, err := part.Write(wav); err != nil {
		return nil, err
	}
	if err := w.WriteField("model", model); err != nil {
		return nil, err
	}
	if language != "" {
		_ = w.WriteField("language", language)
	}
	_ = w.WriteField("response_format", "json")
	if err := w.Close(); err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/audio/transcriptions", &buf)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.Header.Set("Authorization", "Bearer lemonade")

	// ASR can exceed the short NER client timeout.
	httpClient := &http.Client{Timeout: 5 * time.Minute}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("transcribe %d: %s", resp.StatusCode, truncate(string(data), 240))
	}
	var out Transcription
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// LoadedModel returns the currently loaded LLM name, if any.
func (c *Client) LoadedModel(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/health", nil)
	if err != nil {
		return "", err
	}
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	var h struct {
		ModelLoaded string `json:"model_loaded"`
	}
	if err := json.Unmarshal(data, &h); err != nil {
		return "", err
	}
	return h.ModelLoaded, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
