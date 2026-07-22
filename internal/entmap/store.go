// Package entmap maintains session-consistent bidirectional pseudonym maps.
package entmap

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sync"

	"github.com/PrateekKumar1709/cloak/internal/detect"
)

// Store holds per-session entity maps.
type Store struct {
	mu       sync.RWMutex
	sessions map[string]*Session
	counters map[string]map[string]int // session -> prefix -> count
}

// Session is a bidirectional real↔placeholder map.
type Session struct {
	ID           string
	RealToPseudo map[string]string
	PseudoToReal map[string]string
	Categories   map[string]detect.Category // placeholder -> category
}

// NewStore creates an empty entity map store.
func NewStore() *Store {
	return &Store{
		sessions: make(map[string]*Session),
		counters: make(map[string]map[string]int),
	}
}

// SessionID derives a stable session key from fingerprint parts.
func SessionID(clientID, firstUserMsg string, header string) string {
	if header != "" {
		return "hdr:" + header
	}
	h := sha256.Sum256([]byte(clientID + "\x00" + firstUserMsg))
	return hex.EncodeToString(h[:16])
}

// GetOrCreate returns the session map, creating it if needed.
func (s *Store) GetOrCreate(id string) *Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sess, ok := s.sessions[id]; ok {
		return sess
	}
	sess := &Session{
		ID:           id,
		RealToPseudo: make(map[string]string),
		PseudoToReal: make(map[string]string),
		Categories:   make(map[string]detect.Category),
	}
	s.sessions[id] = sess
	s.counters[id] = make(map[string]int)
	return sess
}

// Assign returns an existing placeholder or creates a new one for value.
func (s *Store) Assign(sessionID, value string, cat detect.Category) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess := s.sessions[sessionID]
	if sess == nil {
		sess = &Session{
			ID:           sessionID,
			RealToPseudo: make(map[string]string),
			PseudoToReal: make(map[string]string),
			Categories:   make(map[string]detect.Category),
		}
		s.sessions[sessionID] = sess
		s.counters[sessionID] = make(map[string]int)
	}
	if p, ok := sess.RealToPseudo[value]; ok {
		return p
	}
	prefix := detect.PrefixFor(cat)
	s.counters[sessionID][prefix]++
	n := s.counters[sessionID][prefix]
	pseudo := fmt.Sprintf("%s_%d", prefix, n)
	sess.RealToPseudo[value] = pseudo
	sess.PseudoToReal[pseudo] = value
	sess.Categories[pseudo] = cat
	return pseudo
}

// Resolve returns the real value for a placeholder, if known.
func (s *Store) Resolve(sessionID, pseudo string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sess := s.sessions[sessionID]
	if sess == nil {
		return "", false
	}
	v, ok := sess.PseudoToReal[pseudo]
	return v, ok
}

// Snapshot returns a copy of the session's mappings (for dashboard).
func (s *Store) Snapshot(sessionID string) map[string]string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sess := s.sessions[sessionID]
	if sess == nil {
		return nil
	}
	out := make(map[string]string, len(sess.PseudoToReal))
	for k, v := range sess.PseudoToReal {
		out[k] = v
	}
	return out
}

// AllSessions returns session IDs and entity counts.
func (s *Store) AllSessions() []SessionInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]SessionInfo, 0, len(s.sessions))
	for id, sess := range s.sessions {
		out = append(out, SessionInfo{ID: id, Entities: len(sess.PseudoToReal)})
	}
	return out
}

// SessionInfo is a summary for the dashboard.
type SessionInfo struct {
	ID       string `json:"id"`
	Entities int    `json:"entities"`
}

// ListEntities returns all entities across sessions (redacted by default in UI).
func (s *Store) ListEntities() []EntityRow {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []EntityRow
	for sid, sess := range s.sessions {
		for pseudo, real := range sess.PseudoToReal {
			out = append(out, EntityRow{
				SessionID:   sid,
				Placeholder: pseudo,
				Real:        real,
				Category:    string(sess.Categories[pseudo]),
			})
		}
	}
	return out
}

// EntityRow is a dashboard row.
type EntityRow struct {
	SessionID   string `json:"session_id"`
	Placeholder string `json:"placeholder"`
	Real        string `json:"real"`
	Category    string `json:"category"`
}
