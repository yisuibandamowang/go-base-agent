#!/usr/bin/env bash
# SSE Chat 测试脚本 — 直接查看原始 SSE 事件流
# 用法: bash scripts/test_sse_chat.sh [问题] [conversationId]

set -euo pipefail

QUESTION="${1:-你好，请用一句话介绍你自己}"
CONVERSATION_ID="${2:-test-$(date +%s)}"
BASE_URL="${BASE_URL:-http://localhost:9090}"

echo "=== SSE Chat 测试 ==="
echo "后端: $BASE_URL"
echo "问题: $QUESTION"
echo "会话: $CONVERSATION_ID"
echo "---"

# 先登录获取 token
TOKEN=$(curl -sf -X POST "$BASE_URL/api/ragent/auth/login" \
  -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"admin"}' | python3 -c "import sys,json;print(json.load(sys.stdin)['data']['token'])" 2>/dev/null || echo "")

if [ -n "$TOKEN" ]; then
  echo "Token: ${TOKEN:0:20}..."
  AUTH_HEADER="-H 'Authorization: Bearer $TOKEN'"
else
  echo "警告: 登录失败，token为空"
  AUTH_HEADER=""
fi

echo "--- SSE 原始事件流 ---"

# SSE 请求
curl -sf -N -G "$BASE_URL/api/ragent/rag/v3/chat" \
  -H "Authorization: Bearer $TOKEN" \
  --data-urlencode "question=$QUESTION" \
  --data-urlencode "conversationId=$CONVERSATION_ID" \
  --max-time 30 2>&1 || echo "[SSE连接结束或超时]"

echo ""
echo "=== SSE 测试完成 ==="