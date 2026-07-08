#!/bin/bash
# 测试 /v2/web_create_order 接口 - 回归环境
set -euo pipefail

HOST="${1:-36.99.172.42}"
PORT="${2:-443}"
PROTO="https"
BASE="${PROTO}://${HOST}:${PORT}"

echo "========================================"
echo "测试环境: ${BASE}"
echo "========================================"

# 构建请求 body
REQUEST_BODY=$(cat <<'JSON'
{
    "sku_id": 101193,
    "rule_num": 10101193,
    "payment_method": "WEIXIN_DAIKOU_XCX",
    "pay_type": 1,
    "fee_type": "CNY",
    "version": 2,
    "pay_from": "MSPAY_XCX",
    "from": "",
    "appkey": "5a05c7dc0f49e9bb82f6e7bd8f1744e4",
    "ext": "{\"sku_key\":\"8VnnqDwoP/N+TzsWVq+mkg==\",\"os_type\":\"applet\",\"ad_channel_id\":\"980\",\"click_id\":\"\",\"req_id\":\"\",\"platform\":\"ios\",\"account_id\":\"\",\"open_id\":\"oOwxw3Y2xn4QU3A8HDwNoFalA_e8\",\"bd_vid\":\"\",\"qhclickid\":\"\",\"qz_gdt\":\"\",\"gdt_vid\":\"\",\"logid_url\":\"\",\"bd_account_id\":\"\"}",
    "cookie": false,
    "show_price": "6.9",
    "client_type": "7",
    "active_type": 0,
    "openid": "oOwxw3Y2xn4QU3A8HDwNoFalA_e8",
    "uuid": "V1220955832",
    "card_uniq_id": "",
    "qid": "",
    "Q": "",
    "T": "",
    "common": {
        "mid": "d41d8cd98f00b204e9800998ecf8427e",
        "qdas_mid": "d41d8cd98f00b204e9800998ecf8427e",
        "oaid": "",
        "uuid": "V1220955832",
        "channel": "",
        "is_new_install": "0",
        "source": "",
        "abv": "",
        "user_tag": ""
    },
    "is_new_install": "0",
    "xcx_type": "wx"
}
JSON
)

echo ""
echo ">>> 请求 body:"
echo "${REQUEST_BODY}" | python3 -m json.tool 2>/dev/null || echo "${REQUEST_BODY}"
echo ""

# 发送请求 (跳过 SSL 验证因为是回归环境)
echo ">>> 发送请求到 ${BASE}/v2/web_create_order ..."
RESPONSE=$(curl -s -w "\n%{http_code}" \
    -X POST \
    -H "Content-Type: application/json" \
    -H "Host: pay.regression.example.com" \
    -d "${REQUEST_BODY}" \
    "${BASE}/v2/web_create_order" 2>&1) || true

HTTP_CODE=$(echo "${RESPONSE}" | tail -1)
BODY=$(echo "${RESPONSE}" | sed '$d')

echo ""
echo "========================================"
echo "HTTP 状态码: ${HTTP_CODE}"
echo "响应 body:"
echo "${BODY}" | python3 -m json.tool 2>/dev/null || echo "${BODY}"
echo "========================================"

# 检查是否返回错误码 2428
if echo "${BODY}" | grep -q "2428"; then
    echo ""
    echo ">>> 确认: 返回了错误码 2428（当前价格不存在）"
fi
