#!/usr/bin/env bash
set -Eeuo pipefail
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"
require_root; validate_inputs; ensure_account
emit_progress 10 prepare_directories "正在创建 PHP-FPM 配置目录"
install -d -m 0755 -- "${install_dir}/etc/php-fpm.d" "${install_dir}/etc/php.d"
emit_progress 30 write_php_config "正在写入 PHP 运行配置"
cat >>"${install_dir}/lib/php.ini" <<EOF

; Oneinstack Panel managed settings
expose_php = Off
memory_limit = ${memory_limit}
date.timezone = Asia/Shanghai
cgi.fix_pathinfo = 0
session.cookie_httponly = 1
opcache.enable = 1
opcache.enable_cli = 0
opcache.memory_consumption = 128
EOF
emit_progress 48 write_fpm_config "正在写入 PHP-FPM 池配置"
cat >"${install_dir}/etc/php-fpm.conf" <<EOF
[global]
pid = /run/php-fpm.pid
error_log = /var/log/php-fpm.log
include=${install_dir}/etc/php-fpm.d/*.conf
EOF
cat >"${install_dir}/etc/php-fpm.d/www.conf" <<EOF
[www]
user = ${run_user}
group = ${run_group}
listen = /dev/shm/php-cgi.sock
listen.owner = ${run_user}
listen.group = ${run_group}
listen.mode = 0660
pm = dynamic
pm.max_children = 32
pm.start_servers = 4
pm.min_spare_servers = 2
pm.max_spare_servers = 8
pm.max_requests = 500
catch_workers_output = yes
clear_env = yes
security.limit_extensions = .php
EOF
migrate_external_php_config
normalize_runtime_permissions
emit_progress 68 write_service "正在写入 PHP-FPM systemd 服务"
cat >"${unit_file}" <<EOF
[Unit]
Description=The PHP FastCGI Process Manager
After=network.target
[Service]
Type=simple
PIDFile=/run/php-fpm.pid
ExecStart=${install_dir}/sbin/php-fpm --nodaemonize --fpm-config ${install_dir}/etc/php-fpm.conf
ExecReload=/bin/kill -USR2 \$MAINPID
PrivateTmp=true
ProtectSystem=full
Restart=on-failure
[Install]
WantedBy=multi-user.target
EOF
emit_progress 82 validate_config "正在校验 PHP-FPM 配置"
"${install_dir}/sbin/php-fpm" --test --fpm-config "${install_dir}/etc/php-fpm.conf"
emit_progress 92 service_start "正在启动 PHP-FPM 服务"
systemctl daemon-reload; systemctl enable --now php-fpm
emit_progress 100 configure_completed "PHP-FPM 配置和服务部署完成"
echo "PHP-FPM configuration and systemd service installed."
