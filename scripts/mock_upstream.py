#!/usr/bin/env python3
"""Minimal OpenAI/Anthropic-compatible upstream for Cloak E2E tests."""
from __future__ import annotations

import json
import re
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

PLACEHOLDER_RE = re.compile(
    r"\b(?:PERSON|EMAIL|PHONE|KEY|HOST|ORG|ADDR|ID|SSN|CARD|JWT|PKEY|IP|MAC|CODE|MED|FIN|URLCRED)_\d+\b"
)


class Handler(BaseHTTPRequestHandler):
    last_request = ""
    last_text = ""
    history: list[str] = []

    def log_message(self, fmt, *args):  # quieter
        print(f"[upstream] {self.command} {self.path} -> {fmt % args}", flush=True)

    def _read_json(self):
        n = int(self.headers.get("Content-Length", "0"))
        raw = self.rfile.read(n) if n else b"{}"
        return json.loads(raw.decode() or "{}"), raw

    def _collect_text(self, body: dict) -> str:
        parts = []
        for msg in body.get("messages") or []:
            c = msg.get("content")
            if isinstance(c, str):
                parts.append(c)
            elif isinstance(c, list):
                for p in c:
                    if isinstance(p, dict) and p.get("type") == "text":
                        parts.append(p.get("text") or "")
        return "\n".join(parts)

    def _reply_text(self, scanned: str) -> str:
        ph = PLACEHOLDER_RE.findall(scanned)
        if ph:
            return f"Acknowledged placeholders: {', '.join(dict.fromkeys(ph))}."
        return "No sensitive placeholders seen."

    def do_GET(self):
        if self.path in ("/_last", "/v1/_last"):
            self._json(
                200,
                {
                    "raw": Handler.last_request,
                    "text": Handler.last_text,
                    "history": Handler.history[-20:],
                },
            )
            return
        if self.path.endswith("/models") or self.path == "/v1/models":
            payload = {
                "object": "list",
                "data": [{"id": "mock-gpt", "object": "model", "owned_by": "mock"}],
            }
            self._json(200, payload)
            return
        self._json(404, {"error": "not found"})

    def do_POST(self):
        body, raw = self._read_json()
        Handler.last_request = raw.decode()
        scanned = self._collect_text(body)
        Handler.last_text = scanned
        Handler.history.append(scanned)
        print(f"[upstream-body] {scanned}", flush=True)
        reply = self._reply_text(scanned)
        stream = bool(body.get("stream"))

        if "/messages" in self.path:
            if stream:
                self._anthropic_stream(reply)
            else:
                self._json(
                    200,
                    {
                        "id": "msg_mock",
                        "type": "message",
                        "role": "assistant",
                        "content": [{"type": "text", "text": reply}],
                        "model": body.get("model", "mock-claude"),
                        "stop_reason": "end_turn",
                    },
                )
            return

        if stream:
            self._openai_stream(reply)
            return

        self._json(
            200,
            {
                "id": "chatcmpl-mock",
                "object": "chat.completion",
                "model": body.get("model", "mock-gpt"),
                "choices": [
                    {
                        "index": 0,
                        "message": {"role": "assistant", "content": reply},
                        "finish_reason": "stop",
                    }
                ],
            },
        )

    def _openai_stream(self, text: str):
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Cache-Control", "no-cache")
        self.end_headers()
        # Intentionally split mid-placeholder-friendly chunks
        for i, ch in enumerate(text):
            chunk = {
                "id": "chatcmpl-mock",
                "object": "chat.completion.chunk",
                "choices": [{"index": 0, "delta": {"content": ch}, "finish_reason": None}],
            }
            self.wfile.write(f"data: {json.dumps(chunk)}\n\n".encode())
            self.wfile.flush()
        done = {
            "id": "chatcmpl-mock",
            "object": "chat.completion.chunk",
            "choices": [{"index": 0, "delta": {}, "finish_reason": "stop"}],
        }
        self.wfile.write(f"data: {json.dumps(done)}\n\ndata: [DONE]\n\n".encode())
        self.wfile.flush()

    def _anthropic_stream(self, text: str):
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.end_headers()
        start = {"type": "message_start", "message": {"id": "msg_mock", "role": "assistant"}}
        self.wfile.write(f"event: message_start\ndata: {json.dumps(start)}\n\n".encode())
        block = {"type": "content_block_start", "index": 0, "content_block": {"type": "text", "text": ""}}
        self.wfile.write(f"event: content_block_start\ndata: {json.dumps(block)}\n\n".encode())
        for ch in text:
            delta = {"type": "content_block_delta", "index": 0, "delta": {"type": "text_delta", "text": ch}}
            self.wfile.write(f"event: content_block_delta\ndata: {json.dumps(delta)}\n\n".encode())
            self.wfile.flush()
        stop = {"type": "message_stop"}
        self.wfile.write(f"event: message_stop\ndata: {json.dumps(stop)}\n\n".encode())
        self.wfile.flush()

    def _json(self, code: int, obj):
        data = json.dumps(obj).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)


def main():
    host, port = "127.0.0.1", 9999
    httpd = ThreadingHTTPServer((host, port), Handler)
    print(f"mock upstream on http://{host}:{port}/v1", flush=True)
    httpd.serve_forever()


if __name__ == "__main__":
    main()
