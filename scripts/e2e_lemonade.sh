#!/usr/bin/env bash
# Validate Lemonade Tier-2 NER through Cloak (mock cloud upstream).
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
BIN="$ROOT/bin/cloak"
CFG="$ROOT/.e2e-lemonade.yaml"
PASS=0; FAIL=0
green(){ printf '\033[32m✓ %s\033[0m\n' "$*" || true; }
red(){ printf '\033[31m✗ %s\033[0m\n' "$*" || true; }
info(){ printf '\033[36m· %s\033[0m\n' "$*" || true; }
assert(){ local n="$1"; shift; set +e; "$@"; local rc=$?; set -e; if [[ $rc -eq 0 ]]; then green "$n"; PASS=$((PASS+1)); else red "$n"; FAIL=$((FAIL+1)); fi; }

cleanup(){
  [[ -n "${CLOAK_PID:-}" ]] && kill "$CLOAK_PID" 2>/dev/null || true
  [[ -n "${UP_PID:-}" ]] && kill "$UP_PID" 2>/dev/null || true
  "$BIN" stop 2>/dev/null || true
}
trap cleanup EXIT

cd "$ROOT"
chmod +x scripts/start-lemonade.sh
info "ensuring lemonade + detector model"
./scripts/start-lemonade.sh

assert "lemonade health" bash -c 'curl -sf http://127.0.0.1:13305/api/v1/health | grep -q Qwen3-4B-Instruct-2507-GGUF'

info "building cloak"
make build >/dev/null

info "mock upstream"
python3 "$ROOT/scripts/mock_upstream.py" >/tmp/cloak-up.log 2>&1 &
UP_PID=$!
sleep 0.4

cat >"$CFG" <<'YAML'
listen: "127.0.0.1:7777"
upstream:
  base_url: "http://127.0.0.1:9999/v1"
  api_key: "mock"
  anthropic_base_url: "http://127.0.0.1:9999"
  anthropic_api_key: "mock"
lemonade:
  base_url: "http://127.0.0.1:13305/api/v1"
  model: "Qwen3-4B-Instruct-2507-GGUF"
  timeout_ms: 4000
  enabled: true
detection:
  tier2_fail_open_soft: true
  cache_message_hashes: true
policy:
  EMAIL: redact
  PERSON: redact
  ORG: redact
  PROJECT_CODENAME: redact
  INTERNAL_HOSTNAME: redact
  AWS_KEY: block
watchlist: []
allowlist: []
persist_entities: false
data_dir: "/tmp/cloak-e2e-lem"
log_level: info
audit: true
YAML

info "starting cloak with Tier-2 enabled"
"$BIN" start --config "$CFG" >/tmp/cloak-lem.log 2>&1 &
CLOAK_PID=$!
for _ in $(seq 1 50); do curl -sf http://127.0.0.1:7777/healthz >/dev/null && break; sleep 0.1; done
assert "cloak up" bash -c 'curl -sf http://127.0.0.1:7777/healthz | grep -q ok'
assert "doctor lemonade ok" bash -c 'CLOAK_CONFIG='"$CFG"' '"$BIN"' doctor | grep -q "detector model loaded"'

# Tier-2 entity extraction via /api/test
curl -sf -X POST http://127.0.0.1:7777/api/test \
  -H 'Content-Type: application/json' \
  -d '{"text":"Please ask Sarah Chen on host db-prod-03 about Project Nightingale at Acme Corp."}' \
  >/tmp/cloak-t2.json
assert "tier2 finds PERSON" bash -c 'grep -q PERSON /tmp/cloak-t2.json && grep -q "Sarah Chen" /tmp/cloak-t2.json'
assert "tier2 finds HOST" bash -c 'grep -q INTERNAL_HOSTNAME /tmp/cloak-t2.json'
assert "tier2 finds PROJECT/CODE or watch" bash -c 'grep -Eq "PROJECT_CODENAME|Nightingale" /tmp/cloak-t2.json'
assert "tier2 finds ORG" bash -c 'grep -q ORG /tmp/cloak-t2.json'
assert "sanitized uses placeholders" bash -c 'grep -Eq "PERSON_|HOST_|ORG_|CODE_" /tmp/cloak-t2.json'
python3 - <<'PY'
import json
d=json.load(open("/tmp/cloak-t2.json"))
print("latency_ms", d.get("latency_ms"))
print("sanitized", d.get("sanitized"))
print("findings", [(f["category"], f["text"], f.get("tier")) for f in d.get("findings",[])])
assert d.get("latency_ms", 0) < 15000
PY
assert "tier2 latency under 15s" true

# Full proxy: upstream must not see real names
curl -sf http://127.0.0.1:7777/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -H 'X-Cloak-Session: lem-e2e' \
  -d '{"model":"mock-gpt","messages":[{"role":"user","content":"Ping Sarah Chen on db-prod-03 about Acme Corp"}]}' \
  >/tmp/cloak-t2-chat.json
curl -sf http://127.0.0.1:9999/_last >/tmp/cloak-t2-up.json
assert "client rehydrated PERSON" bash -c 'grep -q "Sarah Chen" /tmp/cloak-t2-chat.json'
assert "upstream got placeholder not Sarah" bash -c '! grep -q "Sarah Chen" /tmp/cloak-t2-up.json && grep -q PERSON_ /tmp/cloak-t2-up.json'

echo
info "results: $PASS passed, $FAIL failed"
[[ "$FAIL" -eq 0 ]] || { echo '---- logs ----'; tail -40 /tmp/cloak-lem.log; cat /tmp/cloak-t2.json; exit 1; }
echo "LEMONADE E2E PASSED"
