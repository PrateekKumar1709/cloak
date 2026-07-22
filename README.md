# Cloak

**The privacy layer for AI.** Point any app’s OpenAI or Anthropic base URL at Cloak. A local [Lemonade](https://github.com/lemonade-sdk/lemonade) model detects and pseudonymizes PII, secrets, and confidential identifiers *before* requests leave your machine, and can answer your most sensitive prompts entirely on-device.

<p align="center">
  <strong>Use cloud AI with a local trust layer.</strong><br/>
  Redact and rehydrate by default. Route crown-jewel prompts to Lemonade with Private Mode.
</p>

```bash
curl -fsSL https://raw.githubusercontent.com/PrateekKumar1709/cloak/main/scripts/install.sh | sh
cloak demo            # Lemonade + mock cloud + dashboard (no API key needed to try)
# open http://127.0.0.1:7777
```

```bash
export OPENAI_BASE_URL=http://127.0.0.1:7777/v1
# Cursor, Claude Code, the OpenAI SDK: unchanged. One env var.
```

Originally built for the **AMD Lemonade Developer Challenge**. MIT licensed. v1.0.

---

## Screenshots

![Cloak dashboard: hero, live protection counters, and the Lemonade hardware panel](docs/screenshots/dashboard.png)

<p align="center"><em>Live protection counters and the “Lemonade · LOCAL” panel: device, backend, and real-time tokens/sec on your own hardware.</em></p>

![Cloak live stream: PII redacted to placeholders and crown-jewel prompts answered on-device](docs/screenshots/live-feed.png)

<p align="center"><em>The cloud sees placeholders such as <code>EMAIL_1</code> / <code>PERSON_1</code> / <code>HOST_1</code>. Prompts that hit a <code>block</code> policy (for example an AWS key) are answered on-device by Lemonade and are not forwarded to the cloud.</em></p>

---

## What makes Cloak different

Most local-AI projects say local AI **replaces** cloud AI. Cloak says local AI is the **trust layer that makes cloud AI safer**, and when a prompt is too sensitive, the thing that answers so the cloud never sees it.

| | Without Cloak | With Cloak |
|---|---|---|
| Normal prompt | Raw PII → cloud | PII pseudonymized locally → cloud → **answer rehydrated** for you |
| Crown-jewel prompt (`block` policy: AWS key, private key, …) | Raw secret → cloud | **Private Mode** (OpenAI-compatible path): answered on-device by Lemonade; request is not sent upstream |
| Voice note | Cloud ASR sees everything | **Omni**: local Whisper transcribes, Cloak redacts |

### Three things it does

1. **Redact & rehydrate.** PII/secrets become session-consistent placeholders (`EMAIL_1`, `PERSON_1`) before the cloud sees them; the cloud’s reply is rehydrated back to real values for you. Streaming-safe.
2. **Private Mode (flagship).** When a prompt trips a `block` policy on the OpenAI-compatible `/v1/chat/completions` path, Cloak answers on the local Lemonade model instead of forwarding (`X-Cloak-Route: local-private`). The Anthropic path currently hard-blocks those prompts rather than answering locally.
3. **Runs on your hardware.** The dashboard shows the Lemonade device/backend Lemonade reports and a live **tokens/sec** sample. On Ryzen AI PCs, Lemonade can place compatible models on the **NPU/GPU**; Cloak surfaces whatever Lemonade selected.

---

## Architecture

![Cloak architecture: a local gateway on your machine; Lemonade does NER, Whisper, and Private Mode answers; only sanitized placeholders ever cross to the cloud](docs/screenshots/architecture.png)

### Detection pipeline

1. **Tier 1 (deterministic, <1 ms):** emails, phones, SSN, Luhn cards, JWTs, private keys, AWS/GCP/GitHub/Slack/OpenAI/Stripe credentials, IPs, URL credentials, high-entropy tokens, watchlists.
2. **Tier 2 (Lemonade instruct model):** PERSON / ORG / INTERNAL_HOSTNAME / PROJECT_CODENAME / … with **span validation** (hallucinated spans are dropped).
3. **Private Mode router:** `block`-policy hits on `/v1/chat/completions` are answered by the local Lemonade chat model instead of being rejected or forwarded.
4. **Omni ASR:** `POST /v1/audio/transcriptions` → Lemonade Whisper → same redaction pipeline.

Session-consistent placeholders keep multi-turn cloud reasoning coherent. Streaming responses use a hold-back buffer so placeholders split across SSE chunks never leak half-replaced.

---

## Install

```bash
# one-line (builds from source; needs Go 1.25+)
curl -fsSL https://raw.githubusercontent.com/PrateekKumar1709/cloak/main/scripts/install.sh | sh

# or from a checkout
git clone https://github.com/PrateekKumar1709/cloak.git
cd cloak
make build && ./bin/cloak version
```

A Homebrew formula stub lives in [`Formula/cloak.rb`](Formula/cloak.rb) for a future tagged release; it is not published as a tap yet.

Prerequisites: **Go 1.25+**, and [Lemonade](https://lemonade-server.ai/install_options.html) running locally (see Quickstart). Cloud keys (`OPENAI_API_KEY` / `ANTHROPIC_API_KEY`) are optional; you only need them to proxy to a real cloud model.

### Menubar companion (optional)

```bash
make menubar && ./bin/cloak-menubar   # live "protected / on-device" count (systray; best supported on macOS)
```

### Quickstart

```bash
# Install Lemonade (embeddable binary under tools/lemonade), then:
./scripts/start-lemonade.sh
make build
./bin/cloak demo
```

If `tools/lemonade/lemond` is missing, `start-lemonade.sh` prints download steps (macos-arm64 embeddable build today; for other platforms use the [Lemonade install docs](https://lemonade-server.ai/install_options.html)).

Dashboard: **http://127.0.0.1:7777**. Then:

```bash
# 1. Redaction: the mock cloud only sees EMAIL_1 / PHONE_1
curl -s http://127.0.0.1:7777/v1/chat/completions -H 'Content-Type: application/json' \
  -d '{"model":"demo","messages":[{"role":"user","content":"email jane@acme.com and call 415-555-0199"}]}'

# 2. Private Mode: a block-policy secret is answered on-device (not sent upstream)
curl -s -D - http://127.0.0.1:7777/v1/chat/completions -H 'Content-Type: application/json' \
  -d '{"model":"demo","messages":[{"role":"user","content":"Is this AWS key valid: AKIAIOSFODNN7EXAMPLE"}]}'
# -> response header:  X-Cloak-Route: local-private
```

### Real cloud upstream

```bash
cp cloak.example.yaml ~/.config/cloak/cloak.yaml
export OPENAI_API_KEY=sk-...
cloak start
export OPENAI_BASE_URL=http://127.0.0.1:7777/v1
export ANTHROPIC_BASE_URL=http://127.0.0.1:7777/anthropic
```

### Detector / chat model picks (64 GB unified memory)

| Model | Role |
|---|---|
| `Qwen3-4B-Instruct-2507-GGUF` | Default (~1.3 s NER), doubles as the Private Mode answerer |
| `Qwen3-8B-GGUF` | Stronger NER |
| `Qwen3-30B-A3B-Instruct-2507-GGUF` | Best big MoE option (~1.5 s) |
| `Whisper-Tiny` / `Whisper-Base` | Omni ASR |

```bash
CLOAK_LEMONADE_MODEL=Qwen3-30B-A3B-Instruct-2507-GGUF ./scripts/start-lemonade.sh
```

On Ryzen AI PCs, Lemonade can place compatible models on the **NPU** automatically. Cloak shows the backend/device Lemonade reports plus a tokens/sec sample on the dashboard.

---

## Measured eval (developer-leak fixtures)

```bash
go run ./eval -fixtures eval/fixtures/dev_leaks.jsonl           # Tier-1
go run ./eval -fixtures eval/fixtures/dev_leaks.jsonl -tier2    # + Lemonade NER
```

Measured on Apple M2 Max (64 GB), `Qwen3-4B-Instruct-2507-GGUF` on Metal via Lemonade (anecdotal; re-run locally for your hardware):

| category | P | R | F1 |
|---|---:|---:|---:|
| PERSON | 0.80 | 1.00 | 0.89 |
| ORG | 1.00 | 1.00 | 1.00 |
| INTERNAL_HOSTNAME | 1.00 | 1.00 | 1.00 |
| EMAIL / AWS_KEY / SSN / CARD / JWT (Tier-1) | 1.00 | 1.00 | 1.00 |
| **Detection latency** | | **p50 ~360 ms** | **p95 ~540 ms** |
| **Local generation (Private Mode)** | | **~78 tok/s** | (Metal) |

Streaming rehydration <0.1 ms/chunk. Risk reduction, not perfection; see [Threat model](#threat-model).

---

## Omni: local Whisper redaction

```bash
say -o /tmp/cloak.aiff "Email jane at acme dot com about the outage"
afconvert -f WAVE -d LEI16 /tmp/cloak.aiff /tmp/cloak.wav
curl -s http://127.0.0.1:7777/v1/audio/transcriptions -F file=@/tmp/cloak.wav -F model=Whisper-Tiny | python3 -m json.tool
```

Response includes the redacted `text` plus timings. Engine header: `X-Cloak-Omni: lemonade-whisper`.

---

## CLI

| Command | Purpose |
|---|---|
| `cloak demo` | Lemonade + mock cloud + gateway |
| `cloak start [--demo] [--no-lemonade]` | Start the gateway |
| `cloak doctor` | Lemonade / model / keys check |
| `cloak test "…"` | Show what would be redacted |
| `cloak pull-model [name]` | Pull + load a model via Lemonade |
| `cloak tail` | Live audit SSE |

Prometheus metrics: `http://127.0.0.1:7777/metrics`. Hardware panel API: `GET /api/hardware`.

---

## Policy (`cloak.yaml`)

```yaml
lemonade:
  base_url: "http://127.0.0.1:13305/api/v1"
  model: "Qwen3-4B-Instruct-2507-GGUF"
  whisper_model: "Whisper-Tiny"
  omni_asr: true
  private_mode: true          # answer block-policy prompts locally (OpenAI path)
policy:
  EMAIL: redact
  AWS_KEY: block              # -> answered on-device when private_mode is on (OpenAI path)
  MEDICAL: redact             # redacted to cloud by default; set block for Private Mode
  PERSON: redact
watchlist: ["Project Nightingale"]
```

Actions: `redact` · `block` · `allow` · `audit`.

---

## Threat model

- **False negatives happen.** Tier-1 catches high-severity secrets; Tier-2 improves contextual recall. Use `block` + `watchlist` for crown jewels; with Private Mode on the OpenAI path they are answered locally instead of forwarded.
- Cloak reduces accidental leaks from IDEs/agents; it is not a compliance certification.
- Embeddings are passed through with `X-Cloak-Warning` (not scanned in v1).
- Anthropic `/anthropic` hard-blocks `block`-policy prompts today; it does not yet answer them via Private Mode.
- Audit DB stays local under `data_dir`; protect that directory.

---

## Development

```bash
make test
make build
bash scripts/e2e_validate.sh    # mock upstream, Tier-1
bash scripts/e2e_lemonade.sh    # live Lemonade Tier-2
bash scripts/e2e_omni.sh        # Omni Whisper redaction
```

Packages: `proxy` · `detect` · `entmap` · `policy` · `lemonade` · `audit` · `web`. Menubar companion lives in `menubar/` (separate module, keeps the core dependency-free).

---

## License

MIT; see [LICENSE](LICENSE).

**Local AI isn’t just a replacement for the cloud; it’s the trust layer for it.**
