package lemonade

import "testing"

func TestParseEntitiesArray(t *testing.T) {
	in := `[{"text":"Sarah Chen","category":"PERSON","confidence":0.95}]`
	ents, err := parseEntities(in)
	if err != nil || len(ents) != 1 || ents[0].Text != "Sarah Chen" {
		t.Fatalf("got %#v err=%v", ents, err)
	}
}

func TestParseEntitiesMarkdown(t *testing.T) {
	in := "```json\n[{\"text\":\"Acme Corp\",\"category\":\"ORG\",\"confidence\":0.9}]\n```"
	ents, err := parseEntities(in)
	if err != nil || len(ents) != 1 || ents[0].Category != "ORG" {
		t.Fatalf("got %#v err=%v", ents, err)
	}
}

func TestParseEntitiesPreamble(t *testing.T) {
	in := "Here you go:\n[{\"text\":\"db-prod-03\",\"category\":\"INTERNAL_HOSTNAME\",\"confidence\":0.9}]"
	ents, err := parseEntities(in)
	if err != nil || len(ents) != 1 {
		t.Fatalf("got %#v err=%v", ents, err)
	}
}
