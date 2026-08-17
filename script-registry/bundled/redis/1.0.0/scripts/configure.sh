#!/usr/bin/env bash
set -Eeuo pipefail
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"
require_root; validate_inputs; ensure_account
emit_progress 10 prepare_directories "正在创建 Redis 数据和配置目录"
install -d -o redis -g redis -m 0750 -- "${data_dir}"
migrate_external_redis_data
install -d -o root -g redis -m 0750 -- "${install_dir}/etc"
emit_progress 30 write_config "正在写入 Redis 配置"
cat >"${install_dir}/etc/redis.conf" <<EOF
bind ${redis_bind}
protected-mode yes
port ${redis_port}
tcp-backlog 511
timeout 0
tcp-keepalive 300
daemonize no
supervised no
loglevel notice
logfile ""
databases 16
dir ${data_dir}
dbfilename dump.rdb
appendonly yes
appendfilename "appendonly.aof"
appenddirname "appendonlydir"
appendfsync everysec
save 3600 1
save 300 100
save 60 10000
EOF
if [[ -n "${redis_password}" ]]; then printf 'requirepass %s\n' "${redis_password}" >>"${install_dir}/etc/redis.conf"; fi
chown root:redis "${install_dir}/etc/redis.conf"; chmod 0640 "${install_dir}/etc/redis.conf"
normalize_runtime_permissions
emit_progress 65 write_service "正在写入 Redis systemd 服务"
cat >"${unit_file}" <<EOF
[Unit]
Description=Redis persistent key-value database
After=network-online.target
Wants=network-online.target
[Service]
Type=simple
User=redis
Group=redis
ExecStart=${install_dir}/bin/redis-server ${install_dir}/etc/redis.conf
ExecStop=/bin/kill -s TERM \$MAINPID
Restart=on-failure
RestartSec=3
LimitNOFILE=65535
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=full
ReadWritePaths=${data_dir}
[Install]
WantedBy=multi-user.target
EOF
emit_progress 90 service_start "正在启动 Redis 服务"
systemctl daemon-reload
systemctl enable redis
systemctl restart redis
export REDISCLI_AUTH="${redis_password}"
redis_ready=false
for _ in {1..20}; do
  if systemctl is-active --quiet redis && [[ "$("${install_dir}/bin/redis-cli" -h 127.0.0.1 -p "${redis_port}" ping 2>/dev/null || true)" == "PONG" ]]; then
    redis_ready=true
    break
  fi
  sleep 0.5
done
if [[ "${redis_ready}" != "true" ]]; then
  systemctl status redis.service --no-pager --lines=20 >&2 || true
  journalctl -u redis.service --no-pager --lines=30 >&2 || true
  die "Redis did not become ready."
fi
emit_progress 100 configure_completed "Redis 配置和服务部署完成"
echo "Redis configuration and systemd service installed."
