package entmap

import (
	"regexp"
	"sort"
	"strings"

	"github.com/PrateekKumar1709/cloak/internal/detect"
)

// PlaceholderRE matches session-consistent placeholders.
var PlaceholderRE = regexp.MustCompile(`\b(?:PERSON|EMAIL|PHONE|KEY|HOST|ORG|ADDR|ID|SSN|CARD|JWT|PKEY|IP|MAC|CODE|MED|FIN|URLCRED)_\d+\b`)

// maxPlaceholderLen is used for the streaming hold-back window.
const maxPlaceholderLen = 32

// Replacer restores placeholders to real values for a session.
type Replacer struct {
	lookup func(pseudo string) (string, bool)
}

// NewReplacer builds a replacer from a live lookup.
func NewReplacer(lookup func(pseudo string) (string, bool)) *Replacer {
	return &Replacer{lookup: lookup}
}

// NewReplacerFromMap builds a replacer from a static map.
func NewReplacerFromMap(m map[string]string) *Replacer {
	return &Replacer{lookup: func(p string) (string, bool) {
		v, ok := m[p]
		return v, ok
	}}
}

// ReplaceAll replaces all complete placeholders in text.
func (r *Replacer) ReplaceAll(text string) string {
	return PlaceholderRE.ReplaceAllStringFunc(text, func(m string) string {
		if v, ok := r.lookup(m); ok {
			return v
		}
		return m
	})
}

// StreamRehydrator implements split-safe streaming rehydration with a hold-back buffer.
type StreamRehydrator struct {
	replacer *Replacer
	buf      strings.Builder
}

// NewStreamRehydrator creates a streaming rehydrator.
func NewStreamRehydrator(r *Replacer) *StreamRehydrator {
	return &StreamRehydrator{replacer: r}
}

// Push appends a chunk and returns emit-safe text (holding back a tail window).
func (s *StreamRehydrator) Push(chunk string) string {
	s.buf.WriteString(chunk)
	full := s.buf.String()
	replaced := s.replacer.ReplaceAll(full)

	hold := maxPlaceholderLen - 1
	if hold > len(replaced) {
		s.buf.Reset()
		s.buf.WriteString(replaced)
		return ""
	}
	emit := replaced[:len(replaced)-hold]
	tail := replaced[len(replaced)-hold:]
	s.buf.Reset()
	s.buf.WriteString(tail)
	return emit
}

// Flush emits the remaining buffer with final replacements.
func (s *StreamRehydrator) Flush() string {
	out := s.replacer.ReplaceAll(s.buf.String())
	s.buf.Reset()
	return out
}

// ReplaceAll runs a full replacement (non-streaming).
func (s *StreamRehydrator) ReplaceAll(text string) string {
	return s.replacer.ReplaceAll(text)
}

// Applied is one replacement performed.
type Applied struct {
	Real     string `json:"real"`
	Pseudo   string `json:"pseudo"`
	Category string `json:"category"`
}

// ApplyFindings replaces findings in text longest-first, assigning placeholders via the store.
func ApplyFindings(text string, findings []detect.Finding, sessionID string, store *Store) (string, []Applied) {
	if len(findings) == 0 {
		return text, nil
	}

	type span struct {
		start, end int
		text       string
		cat        detect.Category
	}
	spans := make([]span, 0, len(findings))
	for _, f := range findings {
		spans = append(spans, span{f.Start, f.End, f.Text, f.Category})
	}
	sort.Slice(spans, func(i, j int) bool {
		if spans[i].start == spans[j].start {
			return (spans[i].end - spans[i].start) > (spans[j].end - spans[j].start)
		}
		return spans[i].start > spans[j].start
	})

	var kept []span
	occupied := make([]bool, len(text))
	for _, sp := range spans {
		if sp.start < 0 || sp.end > len(text) || sp.start >= sp.end {
			continue
		}
		// verify substring match (anti-hallucination for Tier 2)
		if text[sp.start:sp.end] != sp.text {
			// try to locate text
			idx := strings.Index(text, sp.text)
			if idx < 0 {
				continue
			}
			sp.start = idx
			sp.end = idx + len(sp.text)
		}
		overlap := false
		for i := sp.start; i < sp.end; i++ {
			if occupied[i] {
				overlap = true
				break
			}
		}
		if overlap {
			continue
		}
		for i := sp.start; i < sp.end; i++ {
			occupied[i] = true
		}
		kept = append(kept, sp)
	}

	var applied []Applied
	b := []byte(text)
	for _, sp := range kept {
		pseudo := store.Assign(sessionID, sp.text, sp.cat)
		applied = append(applied, Applied{Real: sp.text, Pseudo: pseudo, Category: string(sp.cat)})
		replacement := []byte(pseudo)
		out := make([]byte, 0, len(b)- (sp.end-sp.start) + len(replacement))
		out = append(out, b[:sp.start]...)
		out = append(out, replacement...)
		out = append(out, b[sp.end:]...)
		b = out
	}
	return string(b), applied
}
