#!/usr/bin/env bash
set -Eeuo pipefail
# shellcheck source=common.sh
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"

validate_inputs
verify_runtime_permissions
runuser -u "${run_user}" -- test -w "${log_dir}" ||
  die "MariaDB runtime user cannot write the managed log directory."
[[ -f "${config_file}" && -x "${install_dir}/bin/mariadbd" ]] ||
  die "MariaDB runtime files are missing."
"${install_dir}/bin/mariadbd" --defaults-file="${config_file}" --help --verbose >/dev/null ||
  die "MariaDB configuration validation failed."

emit_progress 18 service_status "正在检查 MariaDB 服务状态"
service_is_active || die "MariaDB service is not active."
systemctl is-enabled --quiet mariadb.service || die "MariaDB service is not enabled."
[[ -S /run/mariadb/mariadb.sock ]] || die "MariaDB socket is not available."

emit_progress 38 health_check "正在执行 MariaDB 认证健康检查"
[[ -n "${mysql_password}" ]] ||
  die "MYSQL_PASSWORD is required for the authenticated MariaDB health check."
client_file="$(mktemp "${state_dir}/verify-client.XXXXXX")"
chmod 0600 "${client_file}"
trap 'rm -f -- "${client_file}"' EXIT
{
  printf '[client]\n'
  printf 'user=%s\n' "${mysql_username}"
  printf "password='%s'\n" "${mysql_password}"
  printf 'host=127.0.0.1\n'
  printf 'port=%s\n' "${mysql_port}"
  printf 'protocol=tcp\n'
} >"${client_file}"
"${install_dir}/bin/mariadb" --defaults-file="${client_file}" \
  --batch --skip-column-names --execute='SELECT 1' | grep -Fxq '1' ||
  die "MariaDB TCP protocol health check failed."

if command -v ss >/dev/null 2>&1; then
  ss -H -ltn "sport = :${mysql_port}" 2>/dev/null | grep -Eq "[:.]${mysql_port}[[:space:]]" ||
    die "MariaDB target port is not listening."
else
  port_hex="$(printf '%04X' "${mysql_port}")"
  awk -v port="${port_hex}" 'NR > 1 { split($2, endpoint, ":"); if (endpoint[2] == port && $4 == "0A") found=1 } END {exit !found}' \
    /proc/net/tcp /proc/net/tcp6 2>/dev/null ||
    die "MariaDB target port is not listening."
fi

emit_progress 68 verify_version "正在核对 MariaDB 精确版本"
runtime_version="$("${install_dir}/bin/mariadbd" --version 2>&1 |
  grep -Eo '[0-9]+\.[0-9]+\.[0-9]+' | head -n1)"
[[ "${runtime_version}" == "${patch_version}" ]] ||
  die "Managed MariaDB runtime version is ${runtime_version:-unknown}, expected ${patch_version}."

emit_progress 86 finalize_state "正在确认 MariaDB 受管状态"
commit_legacy_service
if [[ -f "${state_dir}/pending-version" ]]; then
  mv -f -- "${state_dir}/pending-version" "${state_dir}/version"
  mv -f -- "${state_dir}/pending-patch-version" "${state_dir}/patch-version"
else
  [[ -f "${state_dir}/version" && "$(<"${state_dir}/version")" == "${patch_version}" ]] ||
    die "MariaDB pending installation state is missing."
fi
rm -f -- "${legacy_state_file}" "${state_dir}/initialized-this-run"
rm -rf -- "${rollback_dir}"
emit_progress 100 verify_completed "MariaDB 启动和认证健康检查通过"
printf 'MariaDB %s verification passed.\n' "${runtime_version}"
