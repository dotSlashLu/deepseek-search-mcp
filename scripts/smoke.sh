#!/usr/bin/env bash
#
# End-to-end smoke test for the deepseek-search MCP server.
#
# It starts a canned stand-in for the DeepSeek API (scripts/mockapi.py), then
# drives the server over stdio and over Streamable HTTP, asserting the protocol
# responses and the HTTP auth behaviour. No DeepSeek API key is required.
#
# Usage: BIN=./bin/deepseek-search ./scripts/smoke.sh

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BIN="${BIN:-$ROOT/bin/deepseek-search}"
API_KEY="test-key"
AUTH_TOKEN="s3cret"

MOCK_PORT="${MOCK_PORT:-$((20000 + RANDOM % 10000))}"
HTTP_PORT="${HTTP_PORT:-$((30000 + RANDOM % 10000))}"

WORKDIR="$(mktemp -d)"
MOCK_PID=""
SERVER_PID=""
FAILURES=0

cleanup() {
  for pid in "$SERVER_PID" "$MOCK_PID"; do
    if [ -n "$pid" ]; then
      kill "$pid" 2>/dev/null || true
      wait "$pid" 2>/dev/null || true
    fi
  done
  rm -rf "$WORKDIR"
}
trap cleanup EXIT

pass() { printf '  \033[32mok\033[0m   %s\n' "$1"; }
fail() { printf '  \033[31mFAIL\033[0m %s\n' "$1"; FAILURES=$((FAILURES + 1)); }

check_contains() { # <description> <needle> <haystack-file>
  if grep -q -- "$2" "$3"; then pass "$1"; else fail "$1 (missing: $2)"; fi
}

check_status() { # <description> <expected> <actual>
  if [ "$2" = "$3" ]; then pass "$1"; else fail "$1 (want $2, got $3)"; fi
}

require_tool() {
  command -v "$1" >/dev/null 2>&1 || { echo "missing required tool: $1" >&2; exit 2; }
}

require_tool go
require_tool python3
require_tool curl

if [ ! -x "$BIN" ]; then
  echo "building $BIN"
  (cd "$ROOT" && go build -o "$BIN" .)
fi

echo "== starting mock DeepSeek API on 127.0.0.1:$MOCK_PORT"
python3 "$ROOT/scripts/mockapi.py" --port "$MOCK_PORT" --api-key "$API_KEY" >"$WORKDIR/mock.log" 2>&1 &
MOCK_PID=$!
for _ in $(seq 1 50); do
  code="$(curl -s -o /dev/null -w '%{http_code}' -X POST "http://127.0.0.1:$MOCK_PORT/v1/tools/search" -d '{}' || true)"
  [ "$code" != "000" ] && break
  sleep 0.1
done

echo "== version"
"$BIN" --version >"$WORKDIR/version.txt" 2>&1
check_contains "prints a version" "deepseek-search" "$WORKDIR/version.txt"

echo "== stdio transport"
if python3 "$ROOT/scripts/stdio_client.py" \
  --bin "$BIN" \
  --base-url "http://127.0.0.1:$MOCK_PORT" \
  --api-key "$API_KEY" \
  --stderr-file "$WORKDIR/stdio.err" >"$WORKDIR/stdio.out"; then
  pass "initialize, tools/list and both tool calls succeed over stdio"
else
  fail "stdio session failed (see stderr below)"
fi

check_contains "reports the server name" '"deepseek-search"' "$WORKDIR/stdio.out"
check_contains "lists deepseek_search" '"deepseek_search"' "$WORKDIR/stdio.out"
check_contains "lists deepseek_fetch" '"deepseek_fetch"' "$WORKDIR/stdio.out"
check_contains "search returns mock content" 'DeepSeek V4 model released' "$WORKDIR/stdio.out"
check_contains "fetch returns mock content" 'Mock page' "$WORKDIR/stdio.out"
check_contains "logs go to stderr" 'serving MCP over stdio' "$WORKDIR/stdio.err"

if [ "$(wc -l <"$WORKDIR/stdio.out" | tr -d ' ')" = "4" ]; then
  pass "stdout carries exactly the four protocol responses"
else
  fail "stdout line count is $(wc -l <"$WORKDIR/stdio.out" | tr -d ' ') (want 4): logs must not pollute stdout"
fi

if python3 - "$WORKDIR/stdio.out" <<'PY'
import json, sys
for line in open(sys.argv[1]):
    line = line.strip()
    if line:
        json.loads(line)
PY
then
  pass "every stdout line is valid JSON-RPC"
else
  fail "stdout is not newline-delimited JSON"
fi

echo "== http transport (stateless, bearer auth)"
DEEPSEEK_API_KEY="$API_KEY" "$BIN" \
  --transport http --addr "127.0.0.1:$HTTP_PORT" --http-auth-token "$AUTH_TOKEN" \
  --stateless --base-url "http://127.0.0.1:$MOCK_PORT" \
  >"$WORKDIR/http.out" 2>"$WORKDIR/http.err" &
SERVER_PID=$!
for _ in $(seq 1 50); do
  code="$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:$HTTP_PORT/healthz" || true)"
  [ "$code" = "200" ] && break
  sleep 0.1
done

check_status "GET /healthz" "200" "$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:$HTTP_PORT/healthz")"
check_status "POST /mcp without a token is rejected" "401" \
  "$(curl -s -o /dev/null -w '%{http_code}' -X POST "http://127.0.0.1:$HTTP_PORT/mcp" -H 'Content-Type: application/json' -d '{}')"
check_status "POST /mcp with a wrong token is rejected" "401" \
  "$(curl -s -o /dev/null -w '%{http_code}' -X POST "http://127.0.0.1:$HTTP_PORT/mcp" -H 'Authorization: Bearer nope' -H 'Content-Type: application/json' -d '{}')"

mcp_post() { # <json body>
  curl -s -X POST "http://127.0.0.1:$HTTP_PORT/mcp" \
    -H "Authorization: Bearer $AUTH_TOKEN" \
    -H 'Content-Type: application/json' \
    -H 'Accept: application/json, text/event-stream' \
    -d "$1"
}

mcp_post '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"smoke","version":"0"}}}' >"$WORKDIR/http_init.txt"
check_contains "initialize over HTTP" '"deepseek-search"' "$WORKDIR/http_init.txt"

mcp_post '{"jsonrpc":"2.0","id":2,"method":"tools/list"}' >"$WORKDIR/http_tools.txt"
check_contains "tools/list over HTTP" '"deepseek_search"' "$WORKDIR/http_tools.txt"

mcp_post '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"deepseek_fetch","arguments":{"url":"https://example.com/article"}}}' >"$WORKDIR/http_call.txt"
check_contains "tools/call over HTTP" 'Mock page' "$WORKDIR/http_call.txt"

mcp_post '{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"deepseek_fetch","arguments":{"url":"https://example.com/missing"}}}' >"$WORKDIR/http_err.txt"
check_contains "API failures surface as tool errors" 'no usable page content' "$WORKDIR/http_err.txt"

kill "$SERVER_PID" 2>/dev/null || true
wait "$SERVER_PID" 2>/dev/null || true
SERVER_PID=""

echo
if [ "$FAILURES" -eq 0 ]; then
  echo "smoke test passed"
else
  echo "smoke test failed: $FAILURES check(s)"
  echo "--- stdio stdout ---"; cat "$WORKDIR/stdio.out"
  echo "--- stdio stderr ---"; cat "$WORKDIR/stdio.err"
  echo "--- http stderr ---"; cat "$WORKDIR/http.err"
  exit 1
fi
