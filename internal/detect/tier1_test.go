package detect

import (
	"strings"
	"testing"
)

func TestTier1EmailPhoneKeys(t *testing.T) {
	text := `Contact Jane at jane.doe@acme.com or +1 (415) 555-2671.
AWS key AKIAIOSFODNN7EXAMPLE and github ghp_abcdefghijklmnopqrstuvwxyz0123456789
OpenAI sk-abcdefghijklmnopqrstuvwxyz012345
Card 4111-1111-1111-1111 SSN 219-09-9999`
	findings := Tier1(text)
	cats := map[Category]bool{}
	for _, f := range findings {
		cats[f.Category] = true
		if !strings.Contains(text, f.Text) {
			t.Fatalf("finding text not in source: %q", f.Text)
		}
	}
	for _, want := range []Category{CatEmail, CatPhone, CatAWSKey, CatGitHubToken, CatOpenAIKey, CatCreditCard, CatSSN} {
		if !cats[want] {
			t.Errorf("missing category %s; got %#v", want, cats)
		}
	}
}

func TestLuhnReject(t *testing.T) {
	findings := Tier1("card 1234-5678-9012-3456")
	for _, f := range findings {
		if f.Category == CatCreditCard {
			t.Fatalf("expected luhn reject, got %v", f)
		}
	}
}

func TestWatchlist(t *testing.T) {
	text := "Deploy Project Nightingale to staging"
	f := WatchlistFindings(text, []string{"Project Nightingale"})
	if len(f) != 1 || f[0].Text != "Project Nightingale" {
		t.Fatalf("watchlist: %#v", f)
	}
}
