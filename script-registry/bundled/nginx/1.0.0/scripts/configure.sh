#!/usr/bin/env bash
set -Eeuo pipefail
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"
require_root; validate_inputs; ensure_account
emit_progress 10 prepare_directories "正在创建 Nginx 配置目录"
install -d -m 0755 -- "${install_dir}/conf/conf.d"
normalize_runtime_permissions
emit_progress 30 write_config "正在写入 Nginx 主配置"
cat >"${install_dir}/conf/nginx.conf" <<EOF
user ${run_user} ${run_group};
worker_processes auto;
pid logs/nginx.pid;
error_log ${log_dir}/nginx-error.log warn;
events { worker_connections 4096; use epoll; multi_accept on; }
http {
    include mime.types;
    default_type application/octet-stream;
    log_format main '\$remote_addr - \$remote_user [\$time_local] "\$request" \$status \$body_bytes_sent "\$http_referer" "\$http_user_agent"';
    access_log ${log_dir}/nginx-access.log main;
    sendfile on;
    tcp_nopush on;
    keepalive_timeout 65;
    server_tokens off;
    gzip on;
    include conf.d/*.conf;
}
EOF
emit_progress 50 write_site_config "正在写入默认站点配置"
cat >"${install_dir}/conf/conf.d/default.conf" <<EOF
server {
    listen 80 default_server;
    server_name _;
    root ${web_root}/default;
    index index.html index.htm index.php;
    location / { try_files \$uri \$uri/ =404; }
    location ~ \\.php\$ {
        include fastcgi_params;
        fastcgi_param SCRIPT_FILENAME \$document_root\$fastcgi_script_name;
        fastcgi_pass unix:/dev/shm/php-cgi.sock;
    }
}
EOF
migrate_external_nginx_config
latest_removed=""
for candidate in "${state_dir}"/removed/*; do
  [[ -d "${candidate}/install/conf/conf.d" ]] || continue
  latest_removed="${candidate}"
done
if [[ -n "${latest_removed}" ]]; then
  emit_progress 58 restore_site_config "正在恢复卸载前的网站配置"
  shopt -s nullglob
  for preserved_config in "${latest_removed}/install/conf/conf.d/"*.conf; do
    [[ -f "${preserved_config}" && ! -L "${preserved_config}" ]] || continue
    install -m 0640 -- "${preserved_config}" "${install_dir}/conf/conf.d/$(basename -- "${preserved_config}")"
  done
  shopt -u nullglob
fi
if [[ ! -f "${web_root}/default/index.html" ]]; then
  printf '%s\n' '<!doctype html><meta charset="utf-8"><title>Oneinstack Panel</title><h1>It works.</h1>' >"${web_root}/default/index.html"
  chown "${run_user}:${run_group}" "${web_root}/default/index.html"
fi
emit_progress 65 write_service "正在写入 Nginx systemd 服务"
cat >"${unit_file}" <<EOF
[Unit]
Description=The NGINX HTTP and reverse proxy server
After=network-online.target
Wants=network-online.target
[Service]
Type=forking
PIDFile=${install_dir}/logs/nginx.pid
ExecStartPre=${install_dir}/sbin/nginx -t -q
ExecStart=${install_dir}/sbin/nginx
ExecReload=${install_dir}/sbin/nginx -s reload
ExecStop=-${install_dir}/sbin/nginx -s quit
PrivateTmp=true
LimitNOFILE=65535
Restart=on-failure
[Install]
WantedBy=multi-user.target
EOF
emit_progress 80 validate_config "正在校验 Nginx 配置"
"${install_dir}/sbin/nginx" -t
emit_progress 90 service_start "正在启动 Nginx 服务"
if systemd_available; then
  systemctl daemon-reload
  systemctl enable --now nginx
else
  echo "systemd is unavailable; starting Nginx directly for this container/runtime."
  start_nginx
fi
emit_progress 100 configure_completed "Nginx 配置和服务部署完成"
echo "Nginx configuration and service installed."
