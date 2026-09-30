#!/usr/bin/env bash
set -Eeuo pipefail
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"
validate_inputs
check_host
emit_progress 10 validate_config "正在验证 Nginx 配置"
"${install_dir}/sbin/nginx" -t
verify_runtime_permissions
emit_progress 55 verify_version "正在核对 Nginx 版本"
"${install_dir}/sbin/nginx" -v 2>&1 | grep -Fq "nginx/${software_version}"
configured_port="$(configured_default_site_port "${install_dir}/conf/conf.d/default.conf" || true)"
if [[ "${ONEINSTACK_PARAMETER_NGINX_PORT_EXPLICIT:-false}" == "true" ]]; then
  [[ -z "${configured_port}" || "${configured_port}" == "${nginx_port}" ]] ||
    die "Nginx default site listens on port ${configured_port}, but requested port is ${nginx_port}."
elif [[ -n "${configured_port}" ]]; then
  nginx_port="${configured_port}"
fi
require_command ss
listener="$(ss -H -ltn "sport = :${nginx_port}" 2>/dev/null || true)"
[[ -n "${listener}" ]] || die "Nginx is not listening on configured port ${nginx_port}."
require_command curl
http_status="$(curl --proto '=http' --connect-timeout 5 --max-time 10 --silent --show-error \
  --output /dev/null --write-out '%{http_code}' "http://127.0.0.1:${nginx_port}/" || true)"
[[ "${http_status}" =~ ^2[0-9][0-9]$ ]] ||
  die "Nginx did not return an HTTP 2xx response on configured port ${nginx_port}."
if [[ -S "${php_fpm_socket}" ]]; then
  php_probe="${web_root}/default/.oneinstack-php-probe-${BASHPID}.php"
  trap 'rm -f -- "${php_probe}"' EXIT
  printf '%s\n' '<?php echo "oneinstack-php-ok";' >"${php_probe}"
  chown "${run_user}:${run_group}" "${php_probe}"
  php_status="$(curl --proto '=http' --connect-timeout 5 --max-time 10 --silent --show-error \
    --output - --write-out $'\n%{http_code}' "http://127.0.0.1:${nginx_port}/$(basename -- "${php_probe}")" || true)"
  php_http_status="${php_status##*$'\n'}"
  php_body="${php_status%$'\n'*}"
  [[ "${php_http_status}" =~ ^2[0-9][0-9]$ && "${php_body}" == *oneinstack-php-ok* ]] ||
    die "Nginx could not execute a PHP dynamic page through ${php_fpm_socket}."
  rm -f -- "${php_probe}"
  trap - EXIT
fi
commit_external_nginx
emit_progress 90 finalize_state "正在确认 Nginx 安装状态"
mv -f -- "${state_dir}/pending-version" "${state_dir}/version"
rm -rf -- "${rollback_dir}"
emit_progress 100 verify_completed "Nginx 启动和健康检查通过"
echo "Nginx ${software_version} verification passed."
