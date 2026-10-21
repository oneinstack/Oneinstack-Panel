#!/usr/bin/env bash
set -Eeuo pipefail
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"
validate_inputs
check_host
emit_progress 10 validate_config "正在验证 Tengine 配置"
"$(tengine_runtime_binary)" -t -p "${install_dir}/" -c "$(tengine_main_config)"
verify_runtime_permissions
emit_progress 55 verify_version "正在核对 Tengine 版本"
version_output="$("$(tengine_runtime_binary)" -v 2>&1 || true)"
actual_version="$(sed -nE 's/^Tengine version: Tengine\/([0-9]+\.[0-9]+\.[0-9]+).*$/\1/p' <<<"${version_output}")"
[[ "${actual_version}" == "${software_version}" ]] ||
  die "Tengine version mismatch: expected ${software_version}, detected ${actual_version:-unknown}."
configured_port="$(configured_default_site_port "${install_dir}/conf/conf.d/default.conf" || true)"
if [[ "${ONEINSTACK_PARAMETER_TENGINE_PORT_EXPLICIT:-false}" == "true" ]]; then
  [[ -z "${configured_port}" || "${configured_port}" == "${tengine_port}" ]] ||
    die "Tengine default site listens on port ${configured_port}, but requested port is ${tengine_port}."
elif [[ -n "${configured_port}" ]]; then
  tengine_port="${configured_port}"
fi
require_command ss
listener="$(ss -H -ltn "sport = :${tengine_port}" 2>/dev/null || true)"
[[ -n "${listener}" ]] || die "Tengine is not listening on configured port ${tengine_port}."
require_command curl
http_status="$(curl --proto '=http' --connect-timeout 5 --max-time 10 --silent --show-error \
  --output /dev/null --write-out '%{http_code}' "http://127.0.0.1:${tengine_port}/" || true)"
[[ "${http_status}" =~ ^2[0-9][0-9]$ ]] ||
  die "Tengine did not return an HTTP 2xx response on configured port ${tengine_port}."

tengine_report_phpmyadmin_integration
commit_external_tengine
emit_progress 90 finalize_state "正在确认 Tengine 安装状态"
mv -f -- "${state_dir}/pending-version" "${state_dir}/version"
rm -rf -- "${rollback_dir}"
emit_progress 100 verify_completed "Tengine 启动和健康检查通过"
echo "Tengine ${software_version} verification passed."
