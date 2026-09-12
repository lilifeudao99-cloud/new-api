#!/usr/bin/env bash
set -Eeuo pipefail

# 第八步：生产升级前备份与只读基线采集
# 默认只执行备份，不修改生产服务、不拉取镜像、不重启容器。

REMOTE_HOST="${REMOTE_HOST:-ubuntu@152.32.135.43}"
SSH_KEY="${SSH_KEY:-/Users/weizhang/.ssh/ailili_ucloud_2026}"
REMOTE_DIR="${REMOTE_DIR:-/opt/new-api-relay}"
PG_SERVICE="${PG_SERVICE:-postgres}"
NEW_API_SERVICE="${NEW_API_SERVICE:-new-api}"
STAMP="$(date +%Y%m%d-%H%M%S)"
BACKUP_DIR="${REMOTE_DIR}/backups/${STAMP}"

if [[ "${1:-}" == "--help" || "${1:-}" == "-h" ]]; then
  sed -n '1,12p' "$0"
  echo "Usage: $0 [--help]"
  exit 0
fi

[[ -r "$SSH_KEY" ]] || { echo "SSH key not readable: $SSH_KEY" >&2; exit 1; }

ssh_cmd=(ssh -i "$SSH_KEY" -o BatchMode=yes -o StrictHostKeyChecking=yes "$REMOTE_HOST")

echo "Creating backup directory: $BACKUP_DIR"
"${ssh_cmd[@]}" "mkdir -p '$BACKUP_DIR' && chmod 700 '$BACKUP_DIR'"

echo "Backing up deployment files (including .env without printing contents)"
"${ssh_cmd[@]}" "set -e; cd '$REMOTE_DIR'; for f in Caddyfile compose.yaml compose.override.yaml .env; do if [ -f \"\$f\" ]; then cp -p \"\$f\" '$BACKUP_DIR/'; fi; done; chmod 600 '$BACKUP_DIR/.env' 2>/dev/null || true"

echo "Dumping PostgreSQL database"
"${ssh_cmd[@]}" "set -e; cd '$REMOTE_DIR'; docker compose ps; docker compose exec -T '$PG_SERVICE' pg_dumpall -U \"\${POSTGRES_USER:-postgres}\" > '$BACKUP_DIR/postgres-all.sql'; chmod 600 '$BACKUP_DIR/postgres-all.sql'; test -s '$BACKUP_DIR/postgres-all.sql'"

echo "Capturing image, container, and read-only application baselines"
"${ssh_cmd[@]}" "set -e; cd '$REMOTE_DIR'; container_id=\$(docker compose ps -q '$NEW_API_SERVICE'); test -n \"\$container_id\"; docker inspect \"\$container_id\" > '$BACKUP_DIR/new-api-container-inspect.json'; docker compose ps > '$BACKUP_DIR/compose-ps.txt'; docker images --digests > '$BACKUP_DIR/images-digests.txt'; docker compose config > '$BACKUP_DIR/compose-config.rendered.yaml'; docker compose exec -T '$NEW_API_SERVICE' sh -c 'new-api --version 2>/dev/null || true' > '$BACKUP_DIR/new-api-version.txt' || true"

echo "Writing checksums and backup manifest"
"${ssh_cmd[@]}" "set -e; cd '$BACKUP_DIR'; find . -maxdepth 1 -type f ! -name SHA256SUMS -print0 | xargs -0 shasum -a 256 > SHA256SUMS; printf '%s\\n' \"backup_timestamp=$STAMP\" \"remote=$REMOTE_HOST\" \"deployment_dir=$REMOTE_DIR\" \"postgres_service=$PG_SERVICE\" \"new_api_service=$NEW_API_SERVICE\" > MANIFEST.txt; chmod 600 MANIFEST.txt SHA256SUMS; ls -l"

echo "Backup completed: $REMOTE_HOST:$BACKUP_DIR"
