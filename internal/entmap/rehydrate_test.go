package entmap

import (
	"math/rand"
	"strings"
	"testing"

	"github.com/PrateekKumar1709/cloak/internal/detect"
)

func TestSessionConsistentPlaceholders(t *testing.T) {
	s := NewStore()
	sid := "s1"
	s.GetOrCreate(sid)
	a := s.Assign(sid, "John Smith", detect.CatPerson)
	b := s.Assign(sid, "John Smith", detect.CatPerson)
	c := s.Assign(sid, "jane@acme.com", detect.CatEmail)
	if a != b {
		t.Fatalf("inconsistent: %s vs %s", a, b)
	}
	if a == c {
		t.Fatal("different values got same placeholder")
	}
	if !strings.HasPrefix(a, "PERSON_") || !strings.HasPrefix(c, "EMAIL_") {
		t.Fatalf("bad prefixes %s %s", a, c)
	}
}

func TestApplyAndRehydrate(t *testing.T) {
	store := NewStore()
	sid := "s2"
	store.GetOrCreate(sid)
	text := "Email jane@acme.com about John Smith"
	email, person := "jane@acme.com", "John Smith"
	findings := []detect.Finding{
		{Text: email, Category: detect.CatEmail, Start: strings.Index(text, email), End: strings.Index(text, email) + len(email)},
		{Text: person, Category: detect.CatPerson, Start: strings.Index(text, person), End: strings.Index(text, person) + len(person)},
	}
	san, applied := ApplyFindings(text, findings, sid, store)
	if len(applied) != 2 {
		t.Fatalf("applied=%d", len(applied))
	}
	if strings.Contains(san, "jane@acme.com") || strings.Contains(san, "John Smith") {
		t.Fatalf("not sanitized: %s", san)
	}
	r := NewReplacer(func(p string) (string, bool) { return store.Resolve(sid, p) })
	back := r.ReplaceAll(san)
	if back != text {
		t.Fatalf("rehydrate mismatch:\n got %q\nwant %q", back, text)
	}
}

func TestStreamRehydrateChunkSplits(t *testing.T) {
	m := map[string]string{"PERSON_1": "Ada Lovelace", "EMAIL_1": "ada@analeng.org"}
	r := NewReplacerFromMap(m)
	full := "Hello PERSON_1, write to EMAIL_1 please."
	want := r.ReplaceAll(full)

	rng := rand.New(rand.NewSource(42))
	for trial := 0; trial < 50; trial++ {
		rh := NewStreamRehydrator(r)
		var out strings.Builder
		i := 0
		for i < len(full) {
			n := 1 + rng.Intn(7)
			if i+n > len(full) {
				n = len(full) - i
			}
			out.WriteString(rh.Push(full[i : i+n]))
			i += n
		}
		out.WriteString(rh.Flush())
		if out.String() != want {
			t.Fatalf("trial %d:\n got %q\nwant %q", trial, out.String(), want)
		}
	}
}

func TestStreamPlaceholderAtBoundary(t *testing.T) {
	m := map[string]string{"PERSON_12": "Grace Hopper"}
	r := NewReplacerFromMap(m)
	rh := NewStreamRehydrator(r)
	parts := []string{"Hi ", "PERSON", "_1", "2", "!"}
	var out strings.Builder
	for _, p := range parts {
		out.WriteString(rh.Push(p))
	}
	out.WriteString(rh.Flush())
	if out.String() != "Hi Grace Hopper!" {
		t.Fatalf("got %q", out.String())
	}
}
