#!/usr/bin/env bash
# shellcheck source=common.sh
set -Eeuo pipefail
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"
validate_inputs
check_host
emit_progress 10 validate_config "正在验证 OpenResty 配置"
"$(openresty_runtime_binary)" -t -p "${install_dir}/nginx/" -c "$(openresty_main_config)"
verify_runtime_permissions
emit_progress 55 verify_version "正在核对 OpenResty 版本"
version_output="$("$(openresty_runtime_binary)" -v 2>&1 || true)"
actual_version="$(sed -nE 's|^nginx version: openresty/([0-9]+(\.[0-9]+){3}).*$|\1|p' <<<"${version_output}")"
[[ "${actual_version}" == "${software_version}" ]] ||
  die "OpenResty version mismatch: expected ${software_version}, detected ${actual_version:-unknown}."
if [[ "${os_id}" == "centos" && "${os_major}" == "7" ]]; then
  ssl_build_output="$("$(openresty_runtime_binary)" -V 2>&1 || true)"
  grep -Eq '^built with OpenSSL 1\.1\.1' <<<"${ssl_build_output}" ||
    die "CentOS 7 OpenResty was not built against the required openssl11-devel runtime."
fi
openresty_is_running || die "OpenResty service or master process is not active."
if systemd_available; then
  [[ "$(systemctl_property_value "${service_name}.service" LoadState)" == "loaded" ]] || die "OpenResty systemd unit is not loaded."
  systemctl is-active --quiet "${service_name}.service" || die "OpenResty systemd unit is not active."
  main_pid="$(systemctl_property_value "${service_name}.service" MainPID)"
  [[ "${main_pid}" =~ ^[1-9][0-9]*$ ]] || die "OpenResty systemd MainPID is unavailable."
  process_binary="$(readlink -f "/proc/${main_pid}/exe" 2>/dev/null || true)"
  [[ "${process_binary}" == "$(readlink -f "$(openresty_runtime_binary)")" ]] || die "OpenResty systemd process does not use the managed binary."
fi
configured_port="$(configured_default_site_port "${install_dir}/nginx/conf/conf.d/default.conf" || true)"
if [[ "${ONEINSTACK_PARAMETER_OPENRESTY_PORT_EXPLICIT:-false}" == "true" ]]; then
  [[ -z "${configured_port}" || "${configured_port}" == "${openresty_port}" ]] ||
    die "OpenResty default site listens on port ${configured_port}, but requested port is ${openresty_port}."
elif [[ -n "${configured_port}" ]]; then
  openresty_port="${configured_port}"
fi
require_command ss
listener="$(ss -H -ltn "sport = :${openresty_port}" 2>/dev/null || true)"
[[ -n "${listener}" ]] || die "OpenResty is not listening on configured port ${openresty_port}."
require_command curl
http_status="$(curl --proto '=http' --connect-timeout 5 --max-time 10 --silent --show-error \
  --output /dev/null --write-out '%{http_code}' "http://127.0.0.1:${openresty_port}/" || true)"
[[ "${http_status}" =~ ^2[0-9][0-9]$ ]] ||
  die "OpenResty did not return an HTTP 2xx response on configured port ${openresty_port}."

openresty_report_phpmyadmin_integration
commit_external_openresty
emit_progress 90 finalize_state "正在确认 OpenResty 安装状态"
mv -f -- "${state_dir}/pending-version" "${state_dir}/version"
rm -f -- "${state_dir}/installed.json"
rm -rf -- "${rollback_dir}"
emit_progress 100 verify_completed "OpenResty 启动和健康检查通过"
echo "OpenResty ${software_version} verification passed."
