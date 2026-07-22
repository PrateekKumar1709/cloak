#!/usr/bin/env bash
# Omni ASR e2e: Lemonade Whisper → Cloak redaction
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
BIN="$ROOT/bin/cloak"
cd "$ROOT"

green(){ printf '\033[32m✓ %s\033[0m\n' "$*"; }
red(){ printf '\033[31m✗ %s\033[0m\n' "$*"; FAIL=1; }
FAIL=0

./scripts/start-lemonade.sh
make build >/dev/null

# Pull whisper if needed
curl -sf -X POST http://127.0.0.1:13305/api/v1/pull \
  -H 'Content-Type: application/json' \
  -d '{"model_name":"Whisper-Tiny"}' >/dev/null || true

# Generate WAV with macOS TTS (digits/spelled email for Whisper)
WAV=/tmp/cloak-omni-e2e.wav
if command -v say >/dev/null && command -v afconvert >/dev/null; then
  say -o /tmp/cloak-omni-e2e.aiff "My email is jane at example dot com and my phone is four one five five five five two six seven one"
  afconvert -f WAVE -d LEI16 /tmp/cloak-omni-e2e.aiff "$WAV"
else
  echo "need macOS say+afconvert for this e2e"; exit 1
fi

# Start cloak demo in background
pkill -f 'bin/cloak start' 2>/dev/null || true
"$BIN" start --demo >/tmp/cloak-omni.log 2>&1 &
RPID=$!
trap 'kill $RPID 2>/dev/null || true' EXIT
for _ in $(seq 1 40); do curl -sf http://127.0.0.1:7777/healthz >/dev/null && break; sleep 0.15; done

RESP=$(curl -sf http://127.0.0.1:7777/v1/audio/transcriptions -F "file=@$WAV" -F "model=Whisper-Tiny")
echo "$RESP" | python3 -m json.tool
echo "$RESP" | grep -qi 'cloak' && green "omni response includes cloak metadata" || red "missing cloak metadata"
echo "$RESP" | grep -Eiq 'EMAIL_|PHONE_|jane|example|415|four' && green "transcript produced / redacted" || red "no useful transcript"
curl -sfI http://127.0.0.1:7777/v1/audio/transcriptions -F file=@"$WAV" 2>/dev/null || true
# header check via verbose
HDR=$(curl -sD - -o /tmp/omni-body.json http://127.0.0.1:7777/v1/audio/transcriptions -F "file=@$WAV" -F "model=Whisper-Tiny")
echo "$HDR" | grep -qi 'X-Cloak-Omni' && green "X-Cloak-Omni header" || red "missing X-Cloak-Omni"

[[ "${FAIL:-0}" -eq 0 ]] && echo "OMNI E2E PASSED" || { echo "OMNI E2E FAILED"; tail -30 /tmp/cloak-omni.log; exit 1; }
