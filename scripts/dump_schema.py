#!/usr/bin/env python3
"""Print the tool definitions the server advertises (name, description, schema).

Used as a regression check on the model-facing surface: `make schema` after any
change to descriptions, schemas or defaults.

  python3 scripts/dump_schema.py [--bin ./bin/deepseek-search]
"""

from __future__ import annotations

import argparse
import json
import os
import subprocess
import sys


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--bin", default="./bin/deepseek-search")
    parser.add_argument("--json", action="store_true", help="emit raw JSON instead of a summary")
    args = parser.parse_args()

    env = {**os.environ, "DEEPSEEK_API_KEY": "schema-dump", "MCP_LOG_LEVEL": "error"}
    proc = subprocess.Popen(
        [args.bin],
        stdin=subprocess.PIPE,
        stdout=subprocess.PIPE,
        stderr=subprocess.DEVNULL,
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
                raise RuntimeError(f"server closed stdout before id={request_id}")
            message = json.loads(line)
            if message.get("id") == request_id:
                return message["result"]

    send(
        {
            "jsonrpc": "2.0",
            "id": 1,
            "method": "initialize",
            "params": {
                "protocolVersion": "2025-06-18",
                "capabilities": {},
                "clientInfo": {"name": "dump-schema", "version": "0"},
            },
        }
    )
    init = receive(1)
    send({"jsonrpc": "2.0", "method": "notifications/initialized"})
    send({"jsonrpc": "2.0", "id": 2, "method": "tools/list"})
    tools = receive(2)["tools"]
    proc.stdin.close()
    proc.wait(timeout=10)

    if args.json:
        json.dump({"instructions": init.get("instructions"), "tools": tools}, sys.stdout, ensure_ascii=False, indent=2)
        print()
        return 0

    print("## instructions\n")
    print((init.get("instructions") or "").strip(), "\n")
    for tool in tools:
        print(f"## {tool['name']} — {tool.get('annotations', {}).get('title', '')}\n")
        print(tool["description"], "\n")
        for kind in ("inputSchema", "outputSchema"):
            print(f"### {kind}")
            print(json.dumps(tool.get(kind), ensure_ascii=False, indent=2), "\n")
    return 0


if __name__ == "__main__":
    sys.exit(main())
