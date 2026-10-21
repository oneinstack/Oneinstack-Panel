#!/usr/bin/env bash
# shellcheck source=common.sh
set -Eeuo pipefail
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"
require_root; validate_inputs; ensure_install_port_available; ensure_account
emit_progress 10 prepare_directories "正在创建 OpenResty 配置目录"
install -d -m 0755 -- "${install_dir}/nginx/conf/conf.d" "${web_vhost_root}"
normalize_runtime_permissions
emit_progress 30 write_config "正在写入 OpenResty 主配置"
if [[ -f "${rollback_dir}/install/nginx/conf/nginx.conf" ]]; then
  install -m 0640 -- "${rollback_dir}/install/nginx/conf/nginx.conf" "${install_dir}/nginx/conf/nginx.conf"
else
cat >"${install_dir}/nginx/conf/nginx.conf" <<EOF
user ${run_user} ${run_group};
worker_processes auto;
pid logs/nginx.pid;
error_log ${log_dir}/openresty-error.log warn;
events { worker_connections 4096; use epoll; multi_accept on; }
http {
    include mime.types;
    default_type application/octet-stream;
    log_format main '\$remote_addr - \$remote_user [\$time_local] "\$request" \$status \$body_bytes_sent "\$http_referer" "\$http_user_agent"';
    access_log ${log_dir}/openresty-access.log main;
    sendfile on;
    tcp_nopush on;
    keepalive_timeout 65;
    server_tokens off;
    gzip on;
    include conf.d/*.conf;
	    include ${web_vhost_root}/*.conf;
}
EOF
fi
ensure_openresty_managed_includes
if [[ -d "${rollback_dir}/install/nginx/conf/conf.d" ]]; then
  emit_progress 45 restore_previous_config "正在恢复升级前的 OpenResty 网站配置"
  shopt -s nullglob
  for previous_config in "${rollback_dir}/install/nginx/conf/conf.d/"*.conf; do
    [[ -f "${previous_config}" && ! -L "${previous_config}" ]] || continue
    install -m 0640 -- "${previous_config}" "${install_dir}/nginx/conf/conf.d/$(basename -- "${previous_config}")"
  done
  shopt -u nullglob
fi
emit_progress 50 write_site_config "正在写入默认站点配置"
default_site_config="${install_dir}/nginx/conf/conf.d/default.conf"
if [[ ! -f "${default_site_config}" ]]; then
cat >"${default_site_config}" <<EOF
server {
    listen ${openresty_port} default_server;
    server_name _;
    root ${web_root}/default;
    index index.html index.htm index.php;
    location / { try_files \$uri \$uri/ =404; }
    location ~ \\.php\$ {
        include fastcgi_params;
        fastcgi_param SCRIPT_FILENAME \$document_root\$fastcgi_script_name;
        fastcgi_pass unix:${php_fpm_socket};
    }
}
EOF
fi
migrate_external_openresty_config
latest_removed=""
for candidate in "${state_dir}"/removed/*; do
  [[ -d "${candidate}/install/nginx/conf/conf.d" ]] || continue
  latest_removed="${candidate}"
done
if [[ -n "${latest_removed}" ]]; then
  emit_progress 58 restore_site_config "正在恢复卸载前的网站配置"
  shopt -s nullglob
  for preserved_config in "${latest_removed}/install/nginx/conf/conf.d/"*.conf; do
    [[ -f "${preserved_config}" && ! -L "${preserved_config}" ]] || continue
    install -m 0640 -- "${preserved_config}" "${install_dir}/nginx/conf/conf.d/$(basename -- "${preserved_config}")"
  done
  shopt -u nullglob
fi
if [[ -f "${default_site_config}" ]]; then
  socket_candidate="$(mktemp "${default_site_config}.socket.XXXXXX")"
  sed -E "s|^[[:space:]]*fastcgi_pass[[:space:]]+unix:[^;]+;|        fastcgi_pass unix:${php_fpm_socket};|" \
    "${default_site_config}" >"${socket_candidate}"
  chmod --reference="${default_site_config}" "${socket_candidate}"
  chown --reference="${default_site_config}" "${socket_candidate}"
  mv -f -- "${socket_candidate}" "${default_site_config}"
fi
existing_web_root="$(sed -nE 's|^[[:space:]]*root[[:space:]]+([^;]+);|\1|p' "${default_site_config}" | head -n1)"
if [[ "${existing_web_root}" =~ ^/data/wwwroot(/default)+$ ]]; then
  emit_progress 60 repair_site_root "正在修复重复的默认站点根目录"
  repaired_site_config="$(mktemp "$(dirname "${default_site_config}")/.oneinstack-openresty-site.XXXXXX")"
  cp -p -- "${default_site_config}" "${repaired_site_config}"
  sed -Ei 's|^[[:space:]]*root[[:space:]]+[^;]+;|    root /data/wwwroot/default;|' "${repaired_site_config}"
  mv -f -- "${repaired_site_config}" "${default_site_config}"
fi
reconcile_default_site_port
if [[ ! -f "${web_root}/default/index.html" ]]; then
  printf '%s\n' '<!doctype html><meta charset="utf-8"><title>Oneinstack Panel</title><h1>It works.</h1>' >"${web_root}/default/index.html"
  chown "${run_user}:${run_group}" "${web_root}/default/index.html"
fi
emit_progress 65 write_service "正在写入 OpenResty systemd 服务"
cat >"${unit_file}" <<EOF
[Unit]
Description=OpenResty HTTP and reverse proxy server
After=network-online.target
Wants=network-online.target
[Service]
Type=forking
PIDFile=${install_dir}/nginx/logs/nginx.pid
ExecStartPre=${install_dir}/nginx/sbin/nginx -t -q -p ${install_dir}/nginx/ -c ${install_dir}/nginx/conf/nginx.conf
ExecStart=${install_dir}/nginx/sbin/nginx -p ${install_dir}/nginx/ -c ${install_dir}/nginx/conf/nginx.conf
ExecReload=${install_dir}/nginx/sbin/nginx -p ${install_dir}/nginx/ -c ${install_dir}/nginx/conf/nginx.conf -s reload
ExecStop=-${install_dir}/nginx/sbin/nginx -p ${install_dir}/nginx/ -c ${install_dir}/nginx/conf/nginx.conf -s quit
PrivateTmp=true
LimitNOFILE=65535
Restart=on-failure
[Install]
WantedBy=multi-user.target
EOF
emit_progress 80 validate_config "正在校验 OpenResty 配置"
"${install_dir}/nginx/sbin/nginx" -t
emit_progress 90 service_start "正在启动 OpenResty 服务"
if systemd_available; then
  systemctl daemon-reload
  systemctl stop "${service_name}.service" 2>/dev/null || true
  systemctl disable "${service_name}.service" 2>/dev/null || true
  if legacy_openresty_service_matches; then
    systemctl stop "${legacy_service_name}.service" 2>/dev/null || true
    systemctl disable "${legacy_service_name}.service" 2>/dev/null || true
  elif systemctl is-active --quiet "${legacy_service_name}.service"; then
    systemctl stop "${legacy_service_name}.service" 2>/dev/null || true
  fi
  adopt_legacy_openresty_enablement
  systemctl enable --now "${service_name}.service"
else
  start_openresty
fi
persist_install_parameters
emit_progress 100 configure_completed "OpenResty 配置和服务部署完成"
echo "OpenResty configuration and service installed."
