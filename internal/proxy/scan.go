package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/PrateekKumar1709/cloak/internal/audit"
	"github.com/PrateekKumar1709/cloak/internal/detect"
	"github.com/PrateekKumar1709/cloak/internal/entmap"
	"github.com/PrateekKumar1709/cloak/internal/policy"
)

// ScanOutcome is the result of scanning a request body.
type ScanOutcome struct {
	Body        []byte
	SessionID   string
	Decision    policy.Decision
	Applied     []entmap.Applied
	DetectionMS int64
	Categories  map[string]int
	Original    string
	Sanitized   string
}

var skipScanKeys = map[string]bool{
	"model": true, "stream": true, "temperature": true, "max_tokens": true,
	"max_completion_tokens": true, "top_p": true, "n": true, "stop": true,
	"presence_penalty": true, "frequency_penalty": true, "user": true,
	"response_format": true, "stream_options": true, "logprobs": true,
	"top_logprobs": true, "seed": true, "service_tier": true,
}

// walkAndScan walks a JSON value, scanning every string leaf (except protocol keys).
func (s *Server) walkAndScan(ctx context.Context, v any, sessionID string) (any, []entmap.Applied, int64, map[string]int, string, string, error) {
	var allApplied []entmap.Applied
	var totalMS int64
	cats := map[string]int{}
	var originals, sanitized []string

	var walk func(any, string) (any, error)
	walk = func(node any, parentKey string) (any, error) {
		switch n := node.(type) {
		case map[string]any:
			out := make(map[string]any, len(n))
			for k, val := range n {
				if skipScanKeys[k] {
					out[k] = val
					continue
				}
				nv, err := walk(val, k)
				if err != nil {
					return nil, err
				}
				out[k] = nv
			}
			return out, nil
		case []any:
			out := make([]any, len(n))
			for i, val := range n {
				nv, err := walk(val, parentKey)
				if err != nil {
					return nil, err
				}
				out[i] = nv
			}
			return out, nil
		case string:
			if n == "" || len(n) < 3 {
				return n, nil
			}
			res := s.pipeline.Scan(ctx, n)
			totalMS += res.Latency
			if len(res.Findings) == 0 {
				return n, nil
			}
			dec := s.policy.Evaluate(res.Findings)
			if !dec.Allowed {
				return nil, &blockError{dec: dec}
			}
			san, applied := entmap.ApplyFindings(n, dec.Redact, sessionID, s.entities)
			allApplied = append(allApplied, applied...)
			for _, a := range applied {
				cats[a.Category]++
			}
			originals = append(originals, truncate(n, 500))
			sanitized = append(sanitized, truncate(san, 500))
			return san, nil
		default:
			return n, nil
		}
	}

	out, err := walk(v, "")
	return out, allApplied, totalMS, cats, strings.Join(originals, "\n---\n"), strings.Join(sanitized, "\n---\n"), err
}

type blockError struct{ dec policy.Decision }

func (e *blockError) Error() string { return e.dec.Message }

// sanitizeRequestJSON scans an OpenAI/Anthropic JSON body.
func (s *Server) sanitizeRequestJSON(ctx context.Context, body []byte, clientID, sessionHeader string) (*ScanOutcome, error) {
	var root any
	if err := json.Unmarshal(body, &root); err != nil {
		return nil, fmt.Errorf("invalid json: %w", err)
	}

	firstUser := firstUserMessage(root)
	sessionID := entmap.SessionID(clientID, firstUser, sessionHeader)
	s.entities.GetOrCreate(sessionID)

	out, applied, ms, cats, orig, san, err := s.walkAndScan(ctx, root, sessionID)
	if err != nil {
		if be, ok := err.(*blockError); ok {
			return &ScanOutcome{
				SessionID:   sessionID,
				Decision:    be.dec,
				DetectionMS: ms,
				Categories:  cats,
				Original:    orig,
			}, err
		}
		return nil, err
	}

	newBody, err := json.Marshal(out)
	if err != nil {
		return nil, err
	}
	return &ScanOutcome{
		Body:        newBody,
		SessionID:   sessionID,
		Decision:    policy.Decision{Allowed: true, Redact: findingsFromApplied(applied)},
		Applied:     applied,
		DetectionMS: ms,
		Categories:  cats,
		Original:    orig,
		Sanitized:   san,
	}, nil
}

func findingsFromApplied(applied []entmap.Applied) []detect.Finding {
	out := make([]detect.Finding, len(applied))
	for i, a := range applied {
		out[i] = detect.Finding{Text: a.Real, Category: detect.Category(a.Category)}
	}
	return out
}

func firstUserMessage(root any) string {
	m, ok := root.(map[string]any)
	if !ok {
		return ""
	}
	if msgs, ok := m["messages"].([]any); ok {
		for _, msg := range msgs {
			mm, ok := msg.(map[string]any)
			if !ok {
				continue
			}
			if mm["role"] == "user" {
				return extractText(mm["content"])
			}
		}
	}
	return extractText(m["prompt"])
}

func extractText(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case []any:
		var b strings.Builder
		for _, part := range t {
			if pm, ok := part.(map[string]any); ok {
				if pm["type"] == "text" {
					if s, ok := pm["text"].(string); ok {
						b.WriteString(s)
					}
				}
			}
		}
		return b.String()
	default:
		return ""
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func briefRedactions(applied []entmap.Applied) []audit.RedactionBrief {
	out := make([]audit.RedactionBrief, len(applied))
	for i, a := range applied {
		out[i] = audit.RedactionBrief{
			Category:    a.Category,
			Placeholder: a.Pseudo,
			RealMasked:  audit.Mask(a.Real),
			Real:        a.Real,
		}
	}
	return out
}

func modelFromBody(body []byte) string {
	var m struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(body, &m)
	return m.Model
}

func isStreaming(body []byte) bool {
	var m struct {
		Stream bool `json:"stream"`
	}
	_ = json.Unmarshal(body, &m)
	return m.Stream
}
