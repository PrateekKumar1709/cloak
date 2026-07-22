// Package audit persists request audit events locally (SQLite or memory).
package audit

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// Event is one audited request.
type Event struct {
	ID           int64             `json:"id"`
	TS           time.Time         `json:"ts"`
	Route        string            `json:"route"`
	SessionID    string            `json:"session_id"`
	Client       string            `json:"client"`
	Model        string            `json:"model"`
	DetectionMS  int64             `json:"detection_ms"`
	Blocked      bool              `json:"blocked"`
	Private      bool              `json:"private"` // answered locally by Lemonade (never left the machine)
	Categories   map[string]int    `json:"categories"`
	Redactions    []RedactionBrief  `json:"redactions"`
	OriginalPreview string         `json:"original_preview,omitempty"`
	SanitizedPreview string        `json:"sanitized_preview,omitempty"`
}

// RedactionBrief is a UI-safe redaction summary (real value hidden by default).
type RedactionBrief struct {
	Category    string `json:"category"`
	Placeholder string `json:"placeholder"`
	RealMasked  string `json:"real_masked"`
	Real        string `json:"real,omitempty"` // only on reveal
}

// Store persists and streams audit events.
type Store struct {
	mu     sync.RWMutex
	db     *sql.DB
	memory []Event
	nextID int64
	subs   map[chan Event]struct{}
}

// Open opens (or creates) an audit store. If dataDir is empty, memory-only.
func Open(dataDir string) (*Store, error) {
	s := &Store{
		subs:   make(map[chan Event]struct{}),
		nextID: 1,
	}
	if dataDir == "" {
		return s, nil
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, err
	}
	dbPath := filepath.Join(dataDir, "audit.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS events (
			id INTEGER PRIMARY KEY,
			ts TEXT NOT NULL,
			payload TEXT NOT NULL
		);
	`); err != nil {
		_ = db.Close()
		return nil, err
	}
	s.db = db
	return s, nil
}

// Close closes the DB.
func (s *Store) Close() error {
	if s.db != nil {
		return s.db.Close()
	}
	return nil
}

// Record appends an event and notifies subscribers.
func (s *Store) Record(ev Event) Event {
	s.mu.Lock()
	ev.ID = s.nextID
	s.nextID++
	if ev.TS.IsZero() {
		ev.TS = time.Now()
	}
	s.memory = append(s.memory, ev)
	if len(s.memory) > 1000 {
		s.memory = s.memory[len(s.memory)-1000:]
	}
	if s.db != nil {
		payload, _ := json.Marshal(ev)
		_, _ = s.db.Exec(`INSERT INTO events (id, ts, payload) VALUES (?, ?, ?)`,
			ev.ID, ev.TS.Format(time.RFC3339Nano), string(payload))
	}
	subs := make([]chan Event, 0, len(s.subs))
	for ch := range s.subs {
		subs = append(subs, ch)
	}
	s.mu.Unlock()

	for _, ch := range subs {
		select {
		case ch <- ev:
		default:
		}
	}
	return ev
}

// Recent returns the last n events (real values stripped).
func (s *Store) Recent(n int) []Event {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if n <= 0 || n > len(s.memory) {
		n = len(s.memory)
	}
	src := s.memory[len(s.memory)-n:]
	out := make([]Event, len(src))
	for i, e := range src {
		out[i] = redactEvent(e)
	}
	return out
}

// Subscribe returns a channel of live events. Call cancel to unsubscribe.
func (s *Store) Subscribe() (<-chan Event, func()) {
	ch := make(chan Event, 16)
	s.mu.Lock()
	s.subs[ch] = struct{}{}
	s.mu.Unlock()
	cancel := func() {
		s.mu.Lock()
		delete(s.subs, ch)
		s.mu.Unlock()
		close(ch)
	}
	return ch, cancel
}

// Stats aggregates category counts and latency.
func (s *Store) Stats() Stats {
	s.mu.RLock()
	defer s.mu.RUnlock()
	st := Stats{Categories: map[string]int{}, Requests: len(s.memory)}
	var sum int64
	var latencies []int64
	for _, e := range s.memory {
		sum += e.DetectionMS
		latencies = append(latencies, e.DetectionMS)
		for k, v := range e.Categories {
			st.Categories[k] += v
		}
		if e.Blocked {
			st.Blocked++
		}
		if e.Private {
			st.LocalAnswered++
		}
	}
	if st.Requests > 0 {
		st.AvgDetectionMS = float64(sum) / float64(st.Requests)
	}
	st.P50DetectionMS = percentile(latencies, 0.50)
	st.P95DetectionMS = percentile(latencies, 0.95)
	return st
}

// Stats is aggregate dashboard data.
type Stats struct {
	Requests        int            `json:"requests"`
	Blocked         int            `json:"blocked"`
	LocalAnswered   int            `json:"local_answered"`
	Categories      map[string]int `json:"categories"`
	AvgDetectionMS  float64        `json:"avg_detection_ms"`
	P50DetectionMS  float64        `json:"p50_detection_ms"`
	P95DetectionMS  float64        `json:"p95_detection_ms"`
}

func redactEvent(e Event) Event {
	for i := range e.Redactions {
		e.Redactions[i].Real = ""
	}
	return e
}

// Mask hides most of a secret for UI previews.
func Mask(s string) string {
	if len(s) <= 4 {
		return "••••"
	}
	if len(s) <= 8 {
		return s[:1] + "••••" + s[len(s)-1:]
	}
	return s[:2] + "••••••••" + s[len(s)-2:]
}

func percentile(vals []int64, p float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	// simple copy-sort
	cp := append([]int64(nil), vals...)
	for i := 0; i < len(cp); i++ {
		for j := i + 1; j < len(cp); j++ {
			if cp[j] < cp[i] {
				cp[i], cp[j] = cp[j], cp[i]
			}
		}
	}
	idx := int(float64(len(cp)-1) * p)
	return float64(cp[idx])
}
