#!/usr/bin/env bash
set -Eeuo pipefail
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"
require_root; validate_inputs; ensure_account
[[ "${lifecycle_action}" == "install" || "${lifecycle_action}" == "upgrade" ]] ||
  die_code INVALID_ACTION "PHP configure script received an invalid lifecycle action"
emit_progress 10 prepare_directories "正在创建 PHP-FPM 配置目录"
install -d -m 0755 -- "${install_dir}/etc/php-fpm.d" "${install_dir}/etc/php.d" "${log_dir}" "${state_dir}"
upload_max_filesize="${ONEINSTACK_CONFIG_UPLOAD_MAX_FILESIZE:-2}"
post_max_size="${ONEINSTACK_CONFIG_POST_MAX_SIZE:-8}"
max_execution_time="${ONEINSTACK_CONFIG_MAX_EXECUTION_TIME:-30}"
pm_max_children="${ONEINSTACK_CONFIG_PM_MAX_CHILDREN:-32}"
pm_start_servers="${ONEINSTACK_CONFIG_PM_START_SERVERS:-4}"
pm_min_spare_servers="${ONEINSTACK_CONFIG_PM_MIN_SPARE_SERVERS:-2}"
pm_max_spare_servers="${ONEINSTACK_CONFIG_PM_MAX_SPARE_SERVERS:-8}"
memory_value="${ONEINSTACK_CONFIG_MEMORY_LIMIT:-}"
if [[ -z "${memory_value}" ]]; then
  if [[ "${memory_limit}" =~ ^([0-9]+)M$ ]]; then
    memory_value="${BASH_REMATCH[1]}"
  elif [[ "${memory_limit}" =~ ^([0-9]+)G$ ]]; then
    memory_value="$((BASH_REMATCH[1] * 1024))"
  else
    die_code INVALID_PARAMETER "PHP memory limit is invalid"
  fi
fi
emit_progress 30 write_php_config "正在写入 PHP 运行配置"
managed_ini_tmp="$(mktemp "${managed_ini_file}.tmp.XXXXXX")"
cat >"${managed_ini_tmp}" <<EOF
; Oneinstack managed PHP settings
expose_php = Off
memory_limit = ${memory_value}M
upload_max_filesize = ${upload_max_filesize}M
post_max_size = ${post_max_size}M
max_execution_time = ${max_execution_time}
date.timezone = Asia/Shanghai
cgi.fix_pathinfo = 0
session.cookie_httponly = 1
opcache.enable = 1
opcache.enable_cli = 0
opcache.memory_consumption = 128
EOF
chmod 0640 "${managed_ini_tmp}"
chown root:"${run_group}" "${managed_ini_tmp}"
mv -- "${managed_ini_tmp}" "${managed_ini_file}"
emit_progress 48 write_fpm_config "正在写入 PHP-FPM 池配置"
fpm_tmp="$(mktemp "${fpm_config_file}.tmp.XXXXXX")"
cat >"${fpm_tmp}" <<EOF
[global]
pid = ${pid_file}
error_log = ${log_file}
include=${install_dir}/etc/php-fpm.d/*.conf
EOF
chmod 0640 "${fpm_tmp}"
chown root:"${run_group}" "${fpm_tmp}"
mv -- "${fpm_tmp}" "${fpm_config_file}"
pool_tmp="$(mktemp "${pool_config_file}.tmp.XXXXXX")"
cat >"${pool_tmp}" <<EOF
[www]
user = ${run_user}
group = ${run_group}
listen = ${socket_path}
listen.owner = ${run_user}
listen.group = ${run_group}
listen.mode = 0660
pm = dynamic
pm.max_children = ${pm_max_children}
pm.start_servers = ${pm_start_servers}
pm.min_spare_servers = ${pm_min_spare_servers}
pm.max_spare_servers = ${pm_max_spare_servers}
pm.max_requests = 500
catch_workers_output = yes
security.limit_extensions = .php
EOF
if [[ "${patch_version}" != "5.3.29" ]]; then
  printf 'clear_env = yes\n' >>"${pool_tmp}"
fi
chmod 0640 "${pool_tmp}"
chown root:"${run_group}" "${pool_tmp}"
mv -- "${pool_tmp}" "${pool_config_file}"
migrate_external_php_config
normalize_runtime_permissions
emit_progress 68 write_service "正在写入 PHP-FPM systemd 服务"
if systemd_available; then
  unit_tmp="$(mktemp "${unit_file}.tmp.XXXXXX")"
  cat >"${unit_tmp}" <<EOF
[Unit]
Description=The PHP FastCGI Process Manager
After=network.target
[Service]
Type=simple
PIDFile=${pid_file}
ExecStart=${install_dir}/sbin/php-fpm --nodaemonize --fpm-config ${fpm_config_file}
ExecReload=/bin/kill -USR2 \$MAINPID
PrivateTmp=true
ProtectSystem=full
Restart=on-failure
[Install]
WantedBy=multi-user.target
EOF
  chmod 0644 "${unit_tmp}"
  mv -- "${unit_tmp}" "${unit_file}"
else
  [[ ! -f "${unit_file}" ]] || managed_unit_matches ||
    die_code EXTERNAL_SERVICE_CONFLICT "A non-Oneinstack PHP-FPM unit already owns the service name"
fi
emit_progress 82 validate_config "正在校验 PHP-FPM 配置"
"${install_dir}/sbin/php-fpm" --test --fpm-config "${install_dir}/etc/php-fpm.conf"
commit_external_php
emit_progress 92 service_start "正在启动 PHP-FPM 服务"
start_managed_service
wait_for_socket || die_code SERVICE_START_FAILED "PHP-FPM did not create the configured Unix socket"
write_runtime_parameters "${state_dir}/pending-runtime-params"
emit_progress 100 configure_completed "PHP-FPM 配置和服务部署完成"
echo "component=php"
echo "version=${software_version}"
echo "version_line=${software_version%.*}.x"
echo "action=${lifecycle_action}"
echo "configure=completed"
