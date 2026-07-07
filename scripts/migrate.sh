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

SCHEMA_FILE="${SCHEMA_FILE:-$PROJECT_DIR/resources/database/schema_pg.sql}"
INIT_FILE="${INIT_FILE:-$PROJECT_DIR/resources/database/init_data_pg.sql}"

echo "==> 检查 PostgreSQL 连接..."
if ! psql -h "$PGHOST" -p "$PGPORT" -U "$PGUSER" -d "$PGDATABASE" -c "SELECT 1" > /dev/null 2>&1; then
    echo "错误：无法连接到 PostgreSQL ($PGHOST:$PGPORT/$PGDATABASE)"
    echo "请确认数据库已启动，或设置 PGHOST/PGPORT/PGUSER/PGPASSWORD 环境变量"
    exit 1
fi

echo "==> 执行 schema 脚本: $SCHEMA_FILE"
if [ -f "$SCHEMA_FILE" ]; then
    psql -h "$PGHOST" -p "$PGPORT" -U "$PGUSER" -d "$PGDATABASE" -f "$SCHEMA_FILE"
else
    echo "警告：schema 文件不存在: $SCHEMA_FILE"
    echo "请将 Java 仓库的 resources/database/schema_pg.sql 复制到 $PROJECT_DIR/resources/database/"
fi

echo "==> 执行初始化数据脚本: $INIT_FILE"
if [ -f "$INIT_FILE" ]; then
    psql -h "$PGHOST" -p "$PGPORT" -U "$PGUSER" -d "$PGDATABASE" -f "$INIT_FILE"
else
    echo "提示：初始化数据文件不存在: $INIT_FILE（首次部署可跳过）"
fi

echo "==> 数据库迁移完成"
