#!/usr/bin/env python3
"""Drive the deepseek-search MCP server over stdio and print its responses.

Keeps stdin open while waiting for each response, which is what a real MCP
client does; a shell pipe that writes every request and closes stdin
immediately can make the server exit before the responses are flushed.

Responses are printed as newline-delimited JSON on stdout, the child's stderr
goes to --stderr-file (or the parent's stderr).
"""

from __future__ import annotations

import argparse
import json
import os
import subprocess
import sys


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--bin", required=True, help="path to the deepseek-search binary")
    parser.add_argument("--base-url", required=True, help="DeepSeek API base URL")
    parser.add_argument("--api-key", default="test-key")
    parser.add_argument("--stderr-file", default="")
    args = parser.parse_args()

    env = dict(os.environ, DEEPSEEK_API_KEY=args.api_key)
    stderr = open(args.stderr_file, "w") if args.stderr_file else sys.stderr
    proc = subprocess.Popen(
        [args.bin, "--base-url", args.base_url],
        stdin=subprocess.PIPE,
        stdout=subprocess.PIPE,
        stderr=stderr,
        text=True,
        bufsize=1,
        env=env,
    )
    assert proc.stdin and proc.stdout

    def send(message: dict) -> None:
        proc.stdin.write(json.dumps(message) + "\n")
        proc.stdin.flush()

    def receive(request_id: int) -> dict:
        while True:
            line = proc.stdout.readline()
            if not line:
                raise RuntimeError(f"server closed stdout before answering id={request_id}")
            line = line.strip()
            if not line:
                continue
            message = json.loads(line)
            if message.get("id") == request_id:
                if "error" in message:
                    raise RuntimeError(f"JSON-RPC error for id={request_id}: {message['error']}")
                print(line, flush=True)
                return message

    try:
        send(
            {
                "jsonrpc": "2.0",
                "id": 1,
                "method": "initialize",
                "params": {
                    "protocolVersion": "2025-06-18",
                    "capabilities": {},
                    "clientInfo": {"name": "smoke-stdio-client", "version": "0"},
                },
            }
        )
        receive(1)

        send({"jsonrpc": "2.0", "method": "notifications/initialized"})
        send({"jsonrpc": "2.0", "id": 2, "method": "tools/list"})
        receive(2)

        send(
            {
                "jsonrpc": "2.0",
                "id": 3,
                "method": "tools/call",
                "params": {"name": "deepseek_search", "arguments": {"query": "DeepSeek V4", "count": 2}},
            }
        )
        receive(3)

        send(
            {
                "jsonrpc": "2.0",
                "id": 4,
                "method": "tools/call",
                "params": {"name": "deepseek_fetch", "arguments": {"url": "https://example.com/article"}},
            }
        )
        receive(4)
    except Exception as exc:  # noqa: BLE001 - smoke test: report and fail
        print(f"stdio client failed: {exc}", file=sys.stderr)
        proc.kill()
        return 1
    finally:
        if stderr is not sys.stderr:
            stderr.close()

    proc.stdin.close()
    try:
        return proc.wait(timeout=10)
    except subprocess.TimeoutExpired:
        proc.kill()
        print("server did not exit after stdin closed", file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())
