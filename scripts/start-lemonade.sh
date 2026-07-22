#!/usr/bin/env bash
# Start a local Lemonade (lemond) server.
# Place an embeddable lemond binary at tools/lemonade/lemond (not committed to git).
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
LEM="$ROOT/tools/lemonade"
LOG="$ROOT/.lemonade-server.log"
PIDFILE="$ROOT/.lemonade-server.pid"
HOST="${LEMONADE_HOST:-127.0.0.1}"
PORT="${LEMONADE_PORT:-13305}"
# Override e.g. CLOAK_LEMONADE_MODEL=Qwen3-30B-A3B-Instruct-2507-GGUF
MODEL="${CLOAK_LEMONADE_MODEL:-Qwen3-4B-Instruct-2507-GGUF}"

if [[ ! -x "$LEM/lemond" ]]; then
  echo "Missing $LEM/lemond"
  echo "Download embeddable build:"
  echo "  mkdir -p tools && cd /tmp && curl -fsSL -o lem.tgz \\"
  echo "    https://github.com/lemonade-sdk/lemonade/releases/download/v11.0.0/lemonade-embeddable-11.0.0-macos-arm64.tar.gz"
  echo "  tar -xzf lem.tgz -C $ROOT/tools && mv $ROOT/tools/lemonade-embeddable-* $ROOT/tools/lemonade"
  exit 1
fi

if curl -sf "http://$HOST:$PORT/api/v1/health" >/dev/null 2>&1; then
  echo "Lemonade already running on $HOST:$PORT"
else
  echo "Starting lemond on $HOST:$PORT…"
  nohup "$LEM/lemond" --host "$HOST" --port "$PORT" >>"$LOG" 2>&1 &
  echo $! >"$PIDFILE"
  for _ in $(seq 1 50); do
    if curl -sf "http://$HOST:$PORT/api/v1/health" >/dev/null 2>&1; then break; fi
    sleep 0.1
  done
fi

curl -sf "http://$HOST:$PORT/api/v1/health" | python3 -c 'import sys,json; h=json.load(sys.stdin); print("status:", h.get("status"), "loaded:", h.get("model_loaded"))'

# Ensure detector model is pulled + loaded
if ! curl -sf "http://$HOST:$PORT/api/v1/models?show_all=false" | grep -q "$MODEL"; then
  echo "Pulling $MODEL…"
  curl -sf -X POST "http://$HOST:$PORT/api/v1/pull" \
    -H 'Content-Type: application/json' \
    -d "{\"model_name\":\"$MODEL\"}" >/dev/null
fi

LOADED=$(curl -sf "http://$HOST:$PORT/api/v1/health" | python3 -c 'import sys,json; print(json.load(sys.stdin).get("model_loaded") or "")')
if [[ "$LOADED" != "$MODEL" ]]; then
  echo "Loading $MODEL…"
  curl -sf -X POST "http://$HOST:$PORT/api/v1/load" \
    -H 'Content-Type: application/json' \
    -d "{\"model_name\":\"$MODEL\"}" >/dev/null
fi

WHISPER="${CLOAK_WHISPER_MODEL:-Whisper-Tiny}"
if ! curl -sf "http://$HOST:$PORT/api/v1/models?show_all=false" | grep -q "$WHISPER"; then
  echo "Pulling Omni ASR model $WHISPER…"
  curl -sf -X POST "http://$HOST:$PORT/api/v1/pull" \
    -H 'Content-Type: application/json' \
    -d "{\"model_name\":\"$WHISPER\"}" >/dev/null || true
fi

curl -sf "http://$HOST:$PORT/api/v1/health" | python3 -c 'import sys,json; h=json.load(sys.stdin); print("ready; model_loaded:", h.get("model_loaded"))'
echo "API: http://$HOST:$PORT/api/v1"
echo "Omni Whisper: $WHISPER (used via Cloak POST /v1/audio/transcriptions)"
