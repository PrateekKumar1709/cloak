// Package policy evaluates per-category actions against findings.
package policy

import (
	"fmt"
	"strings"

	"github.com/PrateekKumar1709/cloak/internal/config"
	"github.com/PrateekKumar1709/cloak/internal/detect"
)

// Engine applies YAML policy rules.
type Engine struct {
	actions map[string]config.Action
}

// New creates a policy engine.
func New(actions map[string]config.Action) *Engine {
	m := make(map[string]config.Action, len(actions))
	for k, v := range actions {
		m[strings.ToUpper(k)] = v
	}
	return &Engine{actions: m}
}

// Decision is the outcome of evaluating findings.
type Decision struct {
	Allowed   bool
	Blocked   []detect.Finding
	Redact    []detect.Finding
	AuditOnly []detect.Finding
	Message   string
}

// Evaluate applies policy to findings.
func (e *Engine) Evaluate(findings []detect.Finding) Decision {
	var d Decision
	d.Allowed = true
	for _, f := range findings {
		action := e.actions[string(f.Category)]
		if action == "" {
			action = config.ActionRedact
		}
		switch action {
		case config.ActionAllow:
			// skip
		case config.ActionAudit:
			d.AuditOnly = append(d.AuditOnly, f)
		case config.ActionBlock:
			d.Allowed = false
			d.Blocked = append(d.Blocked, f)
		default: // redact
			d.Redact = append(d.Redact, f)
		}
	}
	if !d.Allowed {
		cats := make([]string, 0, len(d.Blocked))
		for _, f := range d.Blocked {
			cats = append(cats, string(f.Category))
		}
		d.Message = fmt.Sprintf("Cloak blocked request: policy forbids sending %s", strings.Join(unique(cats), ", "))
	}
	return d
}

// ActionFor returns the configured action for a category.
func (e *Engine) ActionFor(cat string) config.Action {
	if a, ok := e.actions[strings.ToUpper(cat)]; ok {
		return a
	}
	return config.ActionRedact
}

// Set updates a category action (dashboard edits).
func (e *Engine) Set(cat string, action config.Action) {
	e.actions[strings.ToUpper(cat)] = action
}

// All returns a copy of the policy map.
func (e *Engine) All() map[string]config.Action {
	out := make(map[string]config.Action, len(e.actions))
	for k, v := range e.actions {
		out[k] = v
	}
	return out
}

func unique(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}
