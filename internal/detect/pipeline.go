package detect

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"sync"
	"time"
)

// PipelineConfig controls detection behavior.
type PipelineConfig struct {
	Watchlist          []string
	Allowlist          []string
	Tier2FailOpenSoft  bool
	CacheMessageHashes bool
}

// Pipeline runs Tier-1 + Tier-2 detection with caching.
type Pipeline struct {
	cfg   PipelineConfig
	tier2 *Tier2Runner
	mu    sync.RWMutex
	cache map[string][]Finding
}

// NewPipeline creates a detection pipeline.
func NewPipeline(cfg PipelineConfig, tier2 *Tier2Runner) *Pipeline {
	return &Pipeline{
		cfg:   cfg,
		tier2: tier2,
		cache: make(map[string][]Finding),
	}
}

// Scan runs full detection on text.
func (p *Pipeline) Scan(ctx context.Context, text string) Result {
	start := time.Now()
	if text == "" {
		return Result{}
	}

	hash := hashText(text)
	if p.cfg.CacheMessageHashes {
		p.mu.RLock()
		if cached, ok := p.cache[hash]; ok {
			p.mu.RUnlock()
			return Result{Findings: cached, Latency: time.Since(start).Milliseconds()}
		}
		p.mu.RUnlock()
	}

	findings := Tier1(text)
	findings = append(findings, WatchlistFindings(text, p.cfg.Watchlist)...)

	if p.tier2 != nil {
		t2, err := p.tier2.Run(ctx, text)
		if err != nil {
			if !p.cfg.Tier2FailOpenSoft {
				// soft fail: continue with Tier-1 only (documented tradeoff)
			}
		} else {
			findings = append(findings, t2...)
		}
	}

	findings = filterAllowlist(findings, p.cfg.Allowlist)
	findings = dedupeFindings(findings)

	if p.cfg.CacheMessageHashes {
		p.mu.Lock()
		p.cache[hash] = findings
		p.mu.Unlock()
	}

	return Result{Findings: findings, Latency: time.Since(start).Milliseconds()}
}

// ScanCachedOnly returns cached findings if present without scanning.
func (p *Pipeline) ScanCachedOnly(text string) ([]Finding, bool) {
	if !p.cfg.CacheMessageHashes {
		return nil, false
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	f, ok := p.cache[hashText(text)]
	return f, ok
}

func hashText(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func filterAllowlist(findings []Finding, allow []string) []Finding {
	if len(allow) == 0 {
		return findings
	}
	allowSet := map[string]bool{}
	for _, a := range allow {
		allowSet[strings.ToLower(a)] = true
	}
	var out []Finding
	for _, f := range findings {
		if allowSet[strings.ToLower(f.Text)] {
			continue
		}
		out = append(out, f)
	}
	return out
}

func dedupeFindings(in []Finding) []Finding {
	seen := map[string]bool{}
	var out []Finding
	for _, f := range in {
		key := string(f.Category) + "|" + f.Text + "|" + strconv.Itoa(f.Start)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, f)
	}
	return out
}
