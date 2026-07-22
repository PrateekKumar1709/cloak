# Cloak evaluation harness

Reproduce precision/recall and latency numbers for the README “Measured eval” section.

## Fixtures

- `fixtures/dev_leaks.jsonl`: hand-built developer-leak prompts (keys, hosts, names, codenames).

Optional: add a slice from [ai4privacy/pii-masking-200k](https://huggingface.co/datasets/ai4privacy/pii-masking-200k) in the same JSONL shape:
`{"id":"…","text":"…","expect":["EMAIL","PERSON"]}`.

## Run

```bash
# Tier-1 only
go run ./eval -fixtures eval/fixtures/dev_leaks.jsonl

# + Lemonade Tier-2 NER (server must be up)
./scripts/start-lemonade.sh
go run ./eval -fixtures eval/fixtures/dev_leaks.jsonl -tier2 -json eval/results-tier2.json
```

Use the printed table to refresh the README metrics for your hardware.
