package proxy

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/PrateekKumar1709/cloak/internal/entmap"
)

// transformOpenAISSE rehydrates text deltas in an OpenAI-compatible SSE stream.
func transformOpenAISSE(dst http.ResponseWriter, src io.Reader, rh *entmap.StreamRehydrator) error {
	flusher, _ := dst.(http.Flusher)
	scanner := bufio.NewScanner(src)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var dataBuf bytes.Buffer
	flushEvent := func() error {
		if dataBuf.Len() == 0 {
			return nil
		}
		data := dataBuf.String()
		dataBuf.Reset()
		if data == "[DONE]" {
			_, err := io.WriteString(dst, "data: [DONE]\n\n")
			if flusher != nil {
				flusher.Flush()
			}
			return err
		}
		transformed := transformOpenAIChunk(data, rh)
		_, err := io.WriteString(dst, "data: "+transformed+"\n\n")
		if flusher != nil {
			flusher.Flush()
		}
		return err
	}

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if err := flushEvent(); err != nil {
				return err
			}
			continue
		}
		if strings.HasPrefix(line, "data:") {
			payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if dataBuf.Len() > 0 {
				dataBuf.WriteByte('\n')
			}
			dataBuf.WriteString(payload)
			continue
		}
		// pass through comments / event: / id: untouched
		_, _ = io.WriteString(dst, line+"\n")
	}
	if err := flushEvent(); err != nil {
		return err
	}
	return scanner.Err()
}

func transformOpenAIChunk(data string, rh *entmap.StreamRehydrator) string {
	var obj map[string]any
	if err := json.Unmarshal([]byte(data), &obj); err != nil {
		return data
	}
	choices, _ := obj["choices"].([]any)
	for _, c := range choices {
		cm, ok := c.(map[string]any)
		if !ok {
			continue
		}
		if delta, ok := cm["delta"].(map[string]any); ok {
			if content, ok := delta["content"].(string); ok && content != "" {
				emit := rh.Push(content)
				delta["content"] = emit
			}
			// tool call arguments
			if toolCalls, ok := delta["tool_calls"].([]any); ok {
				for _, tc := range toolCalls {
					tcm, ok := tc.(map[string]any)
					if !ok {
						continue
					}
					if fn, ok := tcm["function"].(map[string]any); ok {
						if args, ok := fn["arguments"].(string); ok && args != "" {
							fn["arguments"] = rh.Push(args)
						}
					}
				}
			}
		}
		if msg, ok := cm["message"].(map[string]any); ok {
			if content, ok := msg["content"].(string); ok && content != "" {
				msg["content"] = rh.ReplaceAll(content)
			}
		}
	}
	out, err := json.Marshal(obj)
	if err != nil {
		return data
	}
	return string(out)
}

// transformAnthropicSSE rehydrates Anthropic content_block_delta text.
func transformAnthropicSSE(dst http.ResponseWriter, src io.Reader, rh *entmap.StreamRehydrator) error {
	flusher, _ := dst.(http.Flusher)
	scanner := bufio.NewScanner(src)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var eventType string
	var dataBuf bytes.Buffer

	flushEvent := func() error {
		if dataBuf.Len() == 0 && eventType == "" {
			return nil
		}
		data := dataBuf.String()
		dataBuf.Reset()
		et := eventType
		eventType = ""

		if et != "" {
			_, _ = io.WriteString(dst, "event: "+et+"\n")
		}
		if data != "" {
			if et == "content_block_delta" || strings.Contains(data, "content_block_delta") {
				data = transformAnthropicChunk(data, rh)
			}
			_, _ = io.WriteString(dst, "data: "+data+"\n")
		}
		_, err := io.WriteString(dst, "\n")
		if flusher != nil {
			flusher.Flush()
		}
		return err
	}

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if err := flushEvent(); err != nil {
				return err
			}
			continue
		}
		if strings.HasPrefix(line, "event:") {
			eventType = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			continue
		}
		if strings.HasPrefix(line, "data:") {
			payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if dataBuf.Len() > 0 {
				dataBuf.WriteByte('\n')
			}
			dataBuf.WriteString(payload)
			continue
		}
		_, _ = io.WriteString(dst, line+"\n")
	}
	_ = flushEvent()
	return scanner.Err()
}

func transformAnthropicChunk(data string, rh *entmap.StreamRehydrator) string {
	var obj map[string]any
	if err := json.Unmarshal([]byte(data), &obj); err != nil {
		return data
	}
	if delta, ok := obj["delta"].(map[string]any); ok {
		if text, ok := delta["text"].(string); ok && text != "" {
			delta["text"] = rh.Push(text)
		}
		if partial, ok := delta["partial_json"].(string); ok && partial != "" {
			delta["partial_json"] = rh.Push(partial)
		}
	}
	out, err := json.Marshal(obj)
	if err != nil {
		return data
	}
	return string(out)
}

// rehydrateJSONBody restores placeholders in a non-streaming response body.
func rehydrateJSONBody(body []byte, r *entmap.Replacer, style string) []byte {
	var obj map[string]any
	if err := json.Unmarshal(body, &obj); err != nil {
		return body
	}
	switch style {
	case "anthropic":
		if content, ok := obj["content"].([]any); ok {
			for _, block := range content {
				bm, ok := block.(map[string]any)
				if !ok {
					continue
				}
				if t, ok := bm["text"].(string); ok {
					bm["text"] = r.ReplaceAll(t)
				}
			}
		}
	default: // openai
		if choices, ok := obj["choices"].([]any); ok {
			for _, c := range choices {
				cm, ok := c.(map[string]any)
				if !ok {
					continue
				}
				if msg, ok := cm["message"].(map[string]any); ok {
					if content, ok := msg["content"].(string); ok {
						msg["content"] = r.ReplaceAll(content)
					}
				}
				if text, ok := cm["text"].(string); ok {
					cm["text"] = r.ReplaceAll(text)
				}
			}
		}
	}
	out, err := json.Marshal(obj)
	if err != nil {
		return body
	}
	return out
}
