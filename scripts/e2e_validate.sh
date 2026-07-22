#!/usr/bin/env bash
# End-to-end validation of Cloak against a mock upstream.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
BIN="$ROOT/bin/cloak"
CFG="$ROOT/.e2e-cloak.yaml"
UPSTREAM_LOG="$ROOT/.e2e-upstream.log"
CLOAK_LOG="$ROOT/.e2e-cloak.log"
PASS=0
FAIL=0

green() { printf '\033[32m✓ %s\033[0m\n' "$*" || true; }
red()   { printf '\033[31m✗ %s\033[0m\n' "$*" || true; }
info()  { printf '\033[36m· %s\033[0m\n' "$*" || true; }

assert() {
  local name="$1"
  shift
  set +e
  "$@"
  local rc=$?
  set -e
  if [[ $rc -eq 0 ]]; then
    green "$name"
    PASS=$((PASS + 1))
  else
    red "$name"
    FAIL=$((FAIL + 1))
  fi
}

cleanup() {
  info "cleaning up"
  if [[ -n "${CLOAK_PID:-}" ]] && kill -0 "$CLOAK_PID" 2>/dev/null; then
    kill "$CLOAK_PID" 2>/dev/null || true
    wait "$CLOAK_PID" 2>/dev/null || true
  fi
  if [[ -n "${UP_PID:-}" ]] && kill -0 "$UP_PID" 2>/dev/null; then
    kill "$UP_PID" 2>/dev/null || true
    wait "$UP_PID" 2>/dev/null || true
  fi
  "$BIN" stop 2>/dev/null || true
}
trap cleanup EXIT

cd "$ROOT"
info "building"
make build >/dev/null

info "starting mock upstream :9999"
: >"$UPSTREAM_LOG"
python3 "$ROOT/scripts/mock_upstream.py" >"$UPSTREAM_LOG" 2>&1 &
UP_PID=$!
sleep 0.5
assert "mock upstream listening" bash -c 'curl -sf http://127.0.0.1:9999/v1/models >/dev/null'

cat >"$CFG" <<'YAML'
listen: "127.0.0.1:7777"
upstream:
  base_url: "http://127.0.0.1:9999/v1"
  api_key: "mock-key"
  anthropic_base_url: "http://127.0.0.1:9999"
  anthropic_api_key: "mock-key"
lemonade:
  base_url: "http://localhost:13305/api/v1"
  model: "Qwen3-4B-Instruct-GGUF"
  timeout_ms: 800
  enabled: false
detection:
  tier2_fail_open_soft: true
  fail_closed_secrets: true
  cache_message_hashes: true
policy:
  EMAIL: redact
  PHONE: redact
  AWS_KEY: block
  GITHUB_TOKEN: block
  PERSON: redact
  CREDIT_CARD: block
watchlist:
  - "Project Nightingale"
allowlist:
  - "OpenAI"
persist_entities: false
data_dir: "/tmp/cloak-e2e"
log_level: info
audit: true
YAML

info "starting cloak"
: >"$CLOAK_LOG"
"$BIN" start --config "$CFG" --no-lemonade >"$CLOAK_LOG" 2>&1 &
CLOAK_PID=$!
for _ in $(seq 1 50); do
  if curl -sf "http://127.0.0.1:7777/healthz" >/dev/null 2>&1; then break; fi
  sleep 0.1
done

assert "cloak healthz" bash -c 'curl -sf http://127.0.0.1:7777/healthz | grep -q ok'
assert "dashboard HTML" bash -c 'curl -sf http://127.0.0.1:7777/ | grep -q Cloak'
assert "static CSS" bash -c 'curl -sf http://127.0.0.1:7777/static/app.css | grep -q -- "--accent"'
assert "static JS" bash -c 'curl -sf http://127.0.0.1:7777/static/app.js | grep -q EventSource'
assert "metrics endpoint" bash -c 'curl -sf http://127.0.0.1:7777/metrics | grep -q go_'
assert "models proxy" bash -c 'curl -sf http://127.0.0.1:7777/v1/models | grep -q mock-gpt'

curl -sf -X POST http://127.0.0.1:7777/api/test \
  -H 'Content-Type: application/json' \
  -d '{"text":"Contact ada@analeng.org about Project Nightingale"}' >/tmp/cloak-test.json
assert "api/test redacts email" bash -c 'grep -q EMAIL_ /tmp/cloak-test.json'
assert "api/test redacts watchlist" bash -c 'grep -q CODE_ /tmp/cloak-test.json'
assert "api/test returns sanitized" bash -c 'grep -q sanitized /tmp/cloak-test.json'

curl -sf http://127.0.0.1:7777/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -H 'X-Cloak-Session: e2e-chat' \
  -d '{"model":"mock-gpt","messages":[{"role":"user","content":"Please email ada@analeng.org and mention Project Nightingale"}]}' \
  >/tmp/cloak-chat.json
assert "chat completion valid JSON" bash -c 'python3 -c "import json; json.load(open(\"/tmp/cloak-chat.json\"))"'
assert "chat response rehydrated email" bash -c 'grep -q ada@analeng.org /tmp/cloak-chat.json'
assert "chat response rehydrated watchlist" bash -c 'grep -q "Project Nightingale" /tmp/cloak-chat.json'
assert "chat sets X-Cloak-Detection-Ms" bash -c 'curl -sD - -o /dev/null http://127.0.0.1:7777/v1/chat/completions -H "Content-Type: application/json" -H "X-Cloak-Session: e2e-hdr" -d "{\"model\":\"mock-gpt\",\"messages\":[{\"role\":\"user\",\"content\":\"hi ada@analeng.org\"}]}" | grep -qi X-Cloak-Detection-Ms'

# Upstream must see placeholders, never raw PII for a fresh unique email
curl -sf http://127.0.0.1:7777/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -H 'X-Cloak-Session: e2e-upcheck' \
  -d '{"model":"mock-gpt","messages":[{"role":"user","content":"mail secretuser@corp.example about the launch"}]}' \
  >/tmp/cloak-chat2.json
curl -sf http://127.0.0.1:9999/_last >/tmp/cloak-upstream-last.json
assert "upstream saw EMAIL_ placeholder" bash -c 'grep -q EMAIL_ /tmp/cloak-upstream-last.json'
assert "upstream did NOT see raw secret email" bash -c '! grep -q secretuser@corp.example /tmp/cloak-upstream-last.json'

curl -sfN http://127.0.0.1:7777/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -H 'X-Cloak-Session: e2e-stream' \
  -d '{"model":"mock-gpt","stream":true,"messages":[{"role":"user","content":"Write to ada@analeng.org please"}]}' \
  >/tmp/cloak-stream.txt
assert "stream contains SSE data lines" bash -c 'grep -q "^data:" /tmp/cloak-stream.txt'
assert "stream rehydrated email" bash -c 'grep -q ada@analeng.org /tmp/cloak-stream.txt'
assert "stream finished with DONE" bash -c 'grep -q "\[DONE\]" /tmp/cloak-stream.txt'

BLOCK_CODE=$(curl -s -o /tmp/cloak-block.json -w "%{http_code}" http://127.0.0.1:7777/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{"model":"mock-gpt","messages":[{"role":"user","content":"rotate AKIAIOSFODNN7EXAMPLE now"}]}')
assert "AWS key request blocked (non-200)" bash -c "[[ \"$BLOCK_CODE\" != \"200\" ]]"
assert "block body explains policy" bash -c 'grep -Eiq "block|AWS_KEY|content_policy" /tmp/cloak-block.json'

curl -sf http://127.0.0.1:7777/anthropic/v1/messages \
  -H 'Content-Type: application/json' \
  -H 'x-api-key: mock' \
  -H 'anthropic-version: 2023-06-01' \
  -H 'X-Cloak-Session: e2e-anth' \
  -d '{"model":"mock-claude","max_tokens":64,"messages":[{"role":"user","content":"ping jane.doe@acme.com"}]}' \
  >/tmp/cloak-anth.json
assert "anthropic messages rehydrates" bash -c 'grep -q jane.doe@acme.com /tmp/cloak-anth.json'

curl -sf http://127.0.0.1:7777/api/stats >/tmp/cloak-stats.json
assert "stats show requests > 0" bash -c 'python3 -c "import json; s=json.load(open(\"/tmp/cloak-stats.json\")); assert s[\"requests\"]>0"'
curl -sf 'http://127.0.0.1:7777/api/entities?reveal=1' >/tmp/cloak-ents.json
assert "entities map populated" bash -c 'python3 -c "import json; assert len(json.load(open(\"/tmp/cloak-ents.json\")))>0"'
assert "policy API returns EMAIL" bash -c 'curl -sf http://127.0.0.1:7777/api/policy | grep -q EMAIL'
assert "settings show lemonade disabled" bash -c 'curl -sf http://127.0.0.1:7777/api/settings | grep -q "\"lemonade_enabled\":false"'

assert "cloak status up" "$BIN" status
export CLOAK_CONFIG="$CFG"
DOC=$("$BIN" doctor 2>&1 || true)
assert "cloak doctor sees gateway" bash -c "printf '%s' \"$DOC\" | grep -q 'cloak gateway up'"

# Session consistency: same email in one session => same EMAIL_n twice in upstream history
curl -sf http://127.0.0.1:9999/_last >/dev/null  # warm
# reset history by restarting is heavy; instead inspect last two history entries after two calls
python3 - <<'PY' >/tmp/cloak-hist-before.txt
import json,urllib.request
print(len(json.load(urllib.request.urlopen("http://127.0.0.1:9999/_last")).get("history") or []))
PY
curl -sf http://127.0.0.1:7777/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -H 'X-Cloak-Session: e2e-consistent' \
  -d '{"model":"mock-gpt","messages":[{"role":"user","content":"first uniquetag99@analeng.org"}]}' >/dev/null
curl -sf http://127.0.0.1:7777/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -H 'X-Cloak-Session: e2e-consistent' \
  -d '{"model":"mock-gpt","messages":[{"role":"user","content":"again uniquetag99@analeng.org"}]}' >/dev/null
assert "session-consistent placeholder reused" python3 - <<'PY'
import json, urllib.request, re
from collections import Counter
data = json.load(urllib.request.urlopen("http://127.0.0.1:9999/_last"))
hist = data.get("history") or []
# last two user texts for this session probe
tail = [h for h in hist if "uniquetag99" in h or "EMAIL_" in h][-2:]
assert len(tail) >= 2, hist[-5:]
ids = []
for h in tail:
    ids.extend(re.findall(r"EMAIL_\d+", h))
    assert "uniquetag99@analeng.org" not in h, h
assert len(ids) >= 2 and len(set(ids)) == 1, (ids, tail)
PY

# Unit tests as part of e2e gate
info "running go test ./..."
assert "go test ./..." bash -c 'cd "'"$ROOT"'" && go test ./... >/tmp/cloak-gotest.txt 2>&1'

echo
info "results: $PASS passed, $FAIL failed"
if [[ "$FAIL" -gt 0 ]]; then
  echo "---- cloak log (tail) ----"
  tail -n 50 "$CLOAK_LOG" || true
  echo "---- upstream log (tail) ----"
  tail -n 50 "$UPSTREAM_LOG" || true
  echo "---- sample artifacts ----"
  tail -n 5 /tmp/cloak-chat.json 2>/dev/null || true
  exit 1
fi
echo "ALL E2E CHECKS PASSED"
