#!/usr/bin/env bash
set -euo pipefail

BASE_URL="${BASE_URL:-http://127.0.0.1:9091}"
PGHOST="${PGHOST:-127.0.0.1}"
PGPORT="${PGPORT:-5432}"
PGUSER="${PGUSER:-postgres}"
PGPASSWORD="${PGPASSWORD:-postgres}"
PGDATABASE="${PGDATABASE:-ragent}"
REDIS_HOST="${REDIS_HOST:-127.0.0.1}"
REDIS_PORT="${REDIS_PORT:-6379}"
REDIS_PASSWORD="${REDIS_PASSWORD:-}"
ADMIN_USERNAME="${ADMIN_USERNAME:-admin}"
ADMIN_PASSWORD="${ADMIN_PASSWORD:-admin}"
SKIP_DB_CHECK="${SKIP_DB_CHECK:-false}"
SKIP_REDIS_CHECK="${SKIP_REDIS_CHECK:-false}"

export PGPASSWORD

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
DEFAULT_CONFIG="${CONFIG_FILE:-$PROJECT_DIR/configs/config.yaml}"

echo "==> 预检开始"
echo "服务地址: $BASE_URL"
echo "配置文件: $DEFAULT_CONFIG"

if [ ! -f "$DEFAULT_CONFIG" ]; then
    echo "警告：配置文件不存在: $DEFAULT_CONFIG"
fi

echo "==> 检查 PostgreSQL 连接..."
if [ "$SKIP_DB_CHECK" = "true" ]; then
    echo "跳过 PostgreSQL 检查"
else
    psql -h "$PGHOST" -p "$PGPORT" -U "$PGUSER" -d "$PGDATABASE" -c "SELECT 1" >/dev/null
fi

echo "==> 检查 Redis 连接..."
if [ "$SKIP_REDIS_CHECK" = "true" ]; then
    echo "跳过 Redis 检查"
else
    python3 - "$REDIS_HOST" "$REDIS_PORT" "$REDIS_PASSWORD" <<'PY'
import socket
import sys

host, port, password = sys.argv[1], int(sys.argv[2]), sys.argv[3]

def command(*parts):
    payload = [f"*{len(parts)}\r\n".encode()]
    for part in parts:
        data = str(part).encode()
        payload.append(f"${len(data)}\r\n".encode())
        payload.append(data + b"\r\n")
    return b"".join(payload)

with socket.create_connection((host, port), timeout=3) as conn:
    conn.settimeout(3)
    if password:
        conn.sendall(command("AUTH", password))
        if not conn.recv(1024).startswith(b"+OK"):
            raise SystemExit("Redis AUTH failed")
    conn.sendall(command("PING"))
    if not conn.recv(1024).startswith(b"+PONG"):
        raise SystemExit("Redis PING failed")
PY
fi

echo "==> 检查服务健康接口..."
curl -sf "$BASE_URL/health" >/dev/null
curl -sf "$BASE_URL/readyz" >/dev/null
curl -sf "$BASE_URL/api/ragent/health" >/dev/null
curl -sf "$BASE_URL/rag/settings" >/dev/null

echo "==> 登录管理员并校验当前用户..."
login_resp="$(curl -sf -X POST "$BASE_URL/api/ragent/auth/login" \
    -H 'Content-Type: application/json' \
    -d "{\"username\":\"$ADMIN_USERNAME\",\"password\":\"$ADMIN_PASSWORD\"}")"
token="$(python3 -c 'import json,sys;print(json.load(sys.stdin)["data"]["token"])' <<<"$login_resp")"
role="$(python3 -c 'import json,sys;print(json.load(sys.stdin)["data"]["role"])' <<<"$login_resp")"
if [ "$role" != "admin" ]; then
    echo "错误：登录账号不是 admin，role=$role"
    exit 1
fi
curl -sf "$BASE_URL/api/ragent/auth/current-user" -H "Authorization: Bearer $token" >/dev/null

echo "==> 预检完成"
