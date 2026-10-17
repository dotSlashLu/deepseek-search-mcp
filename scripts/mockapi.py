#!/usr/bin/env python3
"""Minimal stand-in for the DeepSeek Search and Fetch APIs, used by smoke.sh.

Serves the two documented endpoints on 127.0.0.1 and requires the same bearer
token header the real API expects. It answers with canned payloads so the
server can be exercised end to end without spending API credits.
"""

from __future__ import annotations

import argparse
import json
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

CANNED_RESULTS = [
    {
        "rerank_score": 2.4,
        "title": "DeepSeek V4 model released",
        "url": "https://example.com/deepseek-v4",
        "snippet": "DeepSeek V4 is now available, with tool calling and long-context support.",
        "summary": "DeepSeek has released the V4 model with improved tool calling and long-context support.",
        "content": "DeepSeek V4 is now generally available.",
        "site_name": "Example News",
        "site_icon": "https://example.com/favicon.ico",
        "date": "2026-06-01T00:00:00+00:00",
        "authority": 3,
    },
    {
        "rerank_score": 1.1,
        "title": "Unrelated page",
        "url": "https://example.org/other",
        "snippet": "Another page.",
        "summary": "Another page that matched the query.",
        "content": "",
        "site_name": None,
        "site_icon": None,
        "date": None,
        "authority": -1,
    },
]


class Handler(BaseHTTPRequestHandler):
    server_version = "mock-deepseek/1.0"
    api_key = ""

    def log_message(self, fmt: str, *args: object) -> None:  # keep the smoke output quiet
        pass

    def _send(self, status: int, payload: dict) -> None:
        body = json.dumps(payload).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def _authorized(self) -> bool:
        header = self.headers.get("Authorization", "")
        return header == f"Bearer {self.api_key}"

    def do_POST(self) -> None:  # noqa: N802 - required by BaseHTTPRequestHandler
        length = int(self.headers.get("Content-Length") or 0)
        raw = self.rfile.read(length) if length else b"{}"
        try:
            request = json.loads(raw or b"{}")
        except json.JSONDecodeError:
            self._send(400, {"error": {"message": "malformed JSON", "code": "invalid_request_error"}})
            return

        if not self._authorized():
            self._send(401, {"error": {"message": "Authentication Fails", "type": "authentication_error"}})
            return

        if self.path == "/v1/tools/search":
            if not str(request.get("query", "")).strip():
                self._send(400, {"error": {"message": "'query' is required", "code": "invalid_request_error"}})
                return
            count = request.get("count") or 10
            results = []
            for item in CANNED_RESULTS[: max(0, min(count, len(CANNED_RESULTS)))]:
                entry = dict(item)
                if not request.get("need_content"):
                    entry["content"] = ""
                results.append(entry)
            self._send(200, {"results": results})
            return

        if self.path == "/v1/tools/fetch":
            url = str(request.get("url", ""))
            if not url.startswith("http"):
                self._send(400, {"error": {"message": "'url' must be an absolute URL", "code": "invalid_request_error"}})
                return
            if url.endswith("/missing"):
                self._send(404, {"error": {"message": "no usable content", "code": "not_found"}})
                return
            self._send(200, {"title": "Mock page", "content": f"# Mock page\n\nFetched from {url}."})
            return

        self._send(404, {"error": {"message": f"unknown path {self.path}", "code": "not_found"}})


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--port", type=int, required=True)
    parser.add_argument("--api-key", default="test-key")
    args = parser.parse_args()

    Handler.api_key = args.api_key
    with ThreadingHTTPServer(("127.0.0.1", args.port), Handler) as httpd:
        print(f"mock deepseek api listening on http://127.0.0.1:{args.port}", flush=True)
        httpd.serve_forever()


if __name__ == "__main__":
    main()
