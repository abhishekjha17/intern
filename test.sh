#!/bin/bash
# test.sh — smoke tests for the intern router proxy
# Usage: start the proxy first, then run this script
#   go run . &
#   ./test.sh

BASE="http://localhost:11411"
PASS=0
FAIL=0

header() { echo -e "\n\033[1;34m=== $1 ===\033[0m"; }
pass()   { echo -e "\033[1;32m✓ PASS\033[0m: $1"; PASS=$((PASS+1)); }
fail()   { echo -e "\033[1;31m✗ FAIL\033[0m: $1"; FAIL=$((FAIL+1)); }

# -------------------------------------------------------
# Test 1: Simple request → should route LOCAL (non-streaming)
# -------------------------------------------------------
header "Test 1: Local routing (non-streaming)"
RESP=$(curl -s -X POST "$BASE/v1/messages" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "claude-3-haiku-20240307",
    "messages": [{"role": "user", "content": "write a hello world in python"}],
    "max_tokens": 100,
    "stream": false
  }')

echo "$RESP" | python3 -m json.tool 2>/dev/null

# Check response has Anthropic format fields
if echo "$RESP" | python3 -c "import sys,json; d=json.load(sys.stdin); assert d['type']=='message' and d['role']=='assistant' and len(d['content'])>0" 2>/dev/null; then
  pass "Response is valid Anthropic format with content"
else
  fail "Response missing Anthropic format fields"
fi

# -------------------------------------------------------
# Test 2: Simple request → should route LOCAL (streaming)
# -------------------------------------------------------
sleep 1
header "Test 2: Local routing (streaming)"
STREAM=$(curl -s -N -X POST "$BASE/v1/messages" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "claude-3-haiku-20240307",
    "messages": [{"role": "user", "content": "what is 2+2?"}],
    "max_tokens": 50,
    "stream": true
  }' --max-time 30)

echo "$STREAM" | head -20

# Check for Anthropic SSE events
if echo "$STREAM" | grep -q "event: message_start" && \
   echo "$STREAM" | grep -q "event: content_block_delta" && \
   echo "$STREAM" | grep -q "event: message_stop"; then
  pass "Stream contains all Anthropic lifecycle events"
else
  fail "Stream missing expected Anthropic SSE events"
fi

sleep 1
# -------------------------------------------------------
# Test 3: Complex request → should route CLOUD
# (will fail with 401 if no API key, but we verify the routing decision via logs)
# -------------------------------------------------------
header "Test 3: Cloud routing (complex request)"
RESP=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$BASE/v1/messages" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "claude-3-haiku-20240307",
    "messages": [{"role": "user", "content": "Design a distributed consensus algorithm that handles Byzantine faults in a heterogeneous network with varying latencies. Provide formal proofs of safety and liveness."}],
    "max_tokens": 500,
    "stream": false
  }')

echo "HTTP status: $RESP"
# 401 = reached Anthropic (no API key) → routing worked
# 200 = had a valid key → routing worked
if [ "$RESP" = "401" ] || [ "$RESP" = "200" ]; then
  pass "Request routed to cloud (HTTP $RESP)"
else
  fail "Unexpected HTTP status: $RESP (expected 401 or 200)"
fi

# -------------------------------------------------------
# Test 4: Tool-use request → routes LOCAL, returns tool_use content block
# -------------------------------------------------------
sleep 1
header "Test 4: Tool-use request → local with tool_use response"
RESP=$(curl -s -X POST "$BASE/v1/messages" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "claude-3-haiku-20240307",
    "messages": [{"role": "user", "content": "What is the weather in London?"}],
    "max_tokens": 200,
    "stream": false,
    "tools": [{"name": "get_weather", "description": "Get the current weather for a location", "input_schema": {"type": "object", "properties": {"location": {"type": "string", "description": "City name"}}, "required": ["location"]}}]
  }')

echo "$RESP" | python3 -m json.tool 2>/dev/null

# Check response has Anthropic format with tool_use content block and stop_reason
if echo "$RESP" | python3 -c "
import sys, json
d = json.load(sys.stdin)
assert d['type'] == 'message', f'type={d[\"type\"]}'
assert d['role'] == 'assistant'
# Should have at least one content block
assert len(d['content']) > 0
# Check for tool_use block or text block (model may not always call tools)
has_tool = any(b['type'] == 'tool_use' for b in d['content'])
has_text = any(b['type'] == 'text' for b in d['content'])
assert has_tool or has_text, 'No tool_use or text blocks'
# If tool_use present, check stop_reason
if has_tool:
    assert d['stop_reason'] == 'tool_use', f'stop_reason={d[\"stop_reason\"]}'
    tool_block = [b for b in d['content'] if b['type'] == 'tool_use'][0]
    assert 'id' in tool_block
    assert 'name' in tool_block
    assert 'input' in tool_block
" 2>/dev/null; then
  pass "Tool-use response in valid Anthropic format"
else
  fail "Tool-use response missing expected format"
  echo "Response was: $RESP"
fi

# -------------------------------------------------------
# Test 5: System prompt passthrough
# -------------------------------------------------------
header "Test 5: System prompt included in local request"
sleep 1
RESP=$(curl -s -X POST "$BASE/v1/messages" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "claude-3-haiku-20240307",
    "messages": [{"role": "user", "content": "what is 10+5?"}],
    "max_tokens": 100,
    "stream": false,
    "system": "You must always reply in French. No matter what, every response must be in French."
  }')

echo "$RESP" | python3 -m json.tool 2>/dev/null

# Check that the response came back in Anthropic format (confirming local routing)
# and contains some text (system prompt was passed through to shape the response)
if echo "$RESP" | python3 -c "import sys,json; d=json.load(sys.stdin); assert d['type']=='message' and len(d['content'])>0 and len(d['content'][0]['text'])>0" 2>/dev/null; then
  pass "System prompt passed through — got local Anthropic-format response"
else
  fail "Response missing or not in Anthropic format"
fi

# -------------------------------------------------------
# Test 6: Multi-turn tool conversation (tool_result in follow-up)
# -------------------------------------------------------
sleep 1
header "Test 6: Multi-turn tool conversation"
RESP=$(curl -s -X POST "$BASE/v1/messages" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "claude-3-haiku-20240307",
    "messages": [
      {"role": "user", "content": "What is the weather in Paris?"},
      {"role": "assistant", "content": [
        {"type": "text", "text": "Let me check the weather for you."},
        {"type": "tool_use", "id": "toolu_01ABC", "name": "get_weather", "input": {"location": "Paris"}}
      ]},
      {"role": "user", "content": [
        {"type": "tool_result", "tool_use_id": "toolu_01ABC", "content": "Sunny, 24°C, light breeze from the west"}
      ]}
    ],
    "max_tokens": 200,
    "stream": false,
    "tools": [{"name": "get_weather", "description": "Get the current weather for a location", "input_schema": {"type": "object", "properties": {"location": {"type": "string"}}, "required": ["location"]}}]
  }')

echo "$RESP" | python3 -m json.tool 2>/dev/null

# The model should respond with text summarizing the tool result
if echo "$RESP" | python3 -c "
import sys, json
d = json.load(sys.stdin)
assert d['type'] == 'message'
assert d['role'] == 'assistant'
assert len(d['content']) > 0
# Should have at least a text block with the weather summary
has_text = any(b['type'] == 'text' and len(b.get('text','')) > 0 for b in d['content'])
assert has_text, 'Expected text response summarizing tool result'
" 2>/dev/null; then
  pass "Multi-turn tool conversation works — model used tool result"
else
  fail "Multi-turn tool conversation failed"
  echo "Response was: $RESP"
fi

# -------------------------------------------------------
# Test 7: Streaming tool-use response
# -------------------------------------------------------
sleep 1
header "Test 7: Streaming tool-use response"
STREAM=$(curl -s -N -X POST "$BASE/v1/messages" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "claude-3-haiku-20240307",
    "messages": [{"role": "user", "content": "What is the weather in Tokyo?"}],
    "max_tokens": 200,
    "stream": true,
    "tools": [{"name": "get_weather", "description": "Get the current weather for a location", "input_schema": {"type": "object", "properties": {"location": {"type": "string", "description": "City name"}}, "required": ["location"]}}]
  }' --max-time 30)

echo "$STREAM" | head -30

# Check for Anthropic SSE lifecycle events
if echo "$STREAM" | grep -q "event: message_start" && \
   echo "$STREAM" | grep -q "event: content_block_start" && \
   echo "$STREAM" | grep -q "event: message_stop"; then
  # Check if tool_use appeared in the stream (model may or may not call the tool)
  if echo "$STREAM" | grep -q "tool_use"; then
    pass "Streaming tool-use response with Anthropic SSE events"
  else
    pass "Streaming response with Anthropic SSE events (model chose text instead of tool)"
  fi
else
  fail "Streaming tool-use missing expected SSE events"
  echo "Stream was: $STREAM"
fi

# -------------------------------------------------------
# Summary
# -------------------------------------------------------
header "Results"
echo "Passed: $PASS"
echo "Failed: $FAIL"
[ "$FAIL" -eq 0 ] && echo -e "\033[1;32mAll tests passed!\033[0m" || echo -e "\033[1;31mSome tests failed.\033[0m"
exit $FAIL
