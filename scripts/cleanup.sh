#!/usr/bin/env bash
set -euo pipefail

PGHOST="${PGHOST:-127.0.0.1}"
PGPORT="${PGPORT:-5432}"
PGUSER="${PGUSER:-postgres}"
PGPASSWORD="${PGPASSWORD:-postgres}"
PGDATABASE="${PGDATABASE:-ragent}"

export PGPASSWORD

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

CLEANUP_FILE="${CLEANUP_FILE:-$PROJECT_DIR/resources/database/cleanup_pg.sql}"

echo "==> 检查 PostgreSQL 连接..."
if ! psql -h "$PGHOST" -p "$PGPORT" -U "$PGUSER" -d "$PGDATABASE" -c "SELECT 1" > /dev/null 2>&1; then
    echo "错误：无法连接到 PostgreSQL ($PGHOST:$PGPORT/$PGDATABASE)"
    echo "请确认数据库已启动，或设置 PGHOST/PGPORT/PGUSER/PGPASSWORD 环境变量"
    exit 1
fi

echo "==> 执行清理脚本: $CLEANUP_FILE"
if [ -f "$CLEANUP_FILE" ]; then
    psql -h "$PGHOST" -p "$PGPORT" -U "$PGUSER" -d "$PGDATABASE" -f "$CLEANUP_FILE"
else
    echo "警告：清理脚本不存在: $CLEANUP_FILE"
    exit 1
fi

echo "==> 数据清理完成"
