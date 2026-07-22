package detect

import (
	"context"
	"strings"
	"time"

	"github.com/PrateekKumar1709/cloak/internal/lemonade"
)

// Tier2Runner calls Lemonade for contextual NER with span validation.
type Tier2Runner struct {
	Client  *lemonade.Client
	Timeout time.Duration
}

// Run extracts entities and validates each text is a literal substring.
func (t *Tier2Runner) Run(ctx context.Context, content string) ([]Finding, error) {
	if t == nil || t.Client == nil || content == "" {
		return nil, nil
	}
	timeout := t.Timeout
	if timeout <= 0 {
		timeout = 800 * time.Millisecond
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	entities, err := t.Client.ExtractEntities(ctx, content)
	if err != nil {
		return nil, err
	}
	var out []Finding
	for _, e := range entities {
		text := strings.TrimSpace(e.Text)
		if text == "" {
			continue
		}
		idx := strings.Index(content, text)
		if idx < 0 {
			// anti-hallucination: drop non-substring matches
			continue
		}
		cat := mapCategory(e.Category)
		conf := e.Confidence
		if conf <= 0 {
			conf = 0.7
		}
		out = append(out, Finding{
			Text: text, Category: cat, Start: idx, End: idx + len(text),
			Confidence: conf, Tier: 2,
		})
	}
	return out, nil
}

func mapCategory(s string) Category {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "PERSON":
		return CatPerson
	case "ORG", "ORGANIZATION":
		return CatOrg
	case "ADDRESS":
		return CatAddress
	case "PROJECT_CODENAME", "CODENAME":
		return CatProjectCodename
	case "INTERNAL_HOSTNAME", "HOSTNAME", "HOST":
		return CatInternalHostname
	case "MEDICAL":
		return CatMedical
	case "FINANCIAL":
		return CatFinancial
	default:
		return CatOtherID
	}
}
