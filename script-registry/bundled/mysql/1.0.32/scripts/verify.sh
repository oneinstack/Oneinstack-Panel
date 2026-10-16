#!/usr/bin/env bash
set -Eeuo pipefail
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"
validate_inputs
verify_runtime_permissions
runuser -u "${run_user}" -- test -w "${log_dir}" ||
  die "MySQL runtime user cannot write the managed log directory."
[[ -f "${config_file}" && -x "${install_dir}/bin/mysqld" ]] || die "MySQL runtime files are missing."
"${install_dir}/bin/mysqld" --defaults-file="${config_file}" --validate-config --user="${run_user}" >/dev/null 2>&1 ||
  die "MySQL configuration validation failed."
emit_progress 15 service_status "正在检查 MySQL 服务状态"
service_is_active || die "MySQL service is not active."
[[ -S /run/mysqld/mysqld.sock ]] || die "MySQL socket is not available."
emit_progress 40 health_check "正在执行 MySQL 连接健康检查"
client_file=""
login_path_file="$(mktemp "${state_dir}/verify-login-path.XXXXXX")"
chmod 0600 "${login_path_file}"
export MYSQL_TEST_LOGIN_FILE="${login_path_file}"
cleanup_client() {
  [[ -z "${client_file}" ]] || rm -f -- "${client_file}"
  rm -f -- "${login_path_file}"
}
trap cleanup_client EXIT
if [[ -n "${mysql_password}" ]]; then
  client_file="$(mktemp "${state_dir}/verify-client.XXXXXX")"
  chmod 0600 "${client_file}"
  cat >"${client_file}" <<EOF
[client]
user=${mysql_username}
password='${mysql_password}'
host=127.0.0.1
port=${mysql_port}
protocol=tcp
get-server-public-key
EOF
  "${install_dir}/bin/mysql" --defaults-file="${client_file}" --user="${mysql_username}" \
    --host=127.0.0.1 --port="${mysql_port}" --protocol=tcp \
    --batch --skip-column-names --execute='SELECT 1' | grep -Fxq '1' ||
    die "MySQL TCP protocol health check failed."
else
  "${install_dir}/bin/mysqladmin" --protocol=socket --socket=/run/mysqld/mysqld.sock --user="${mysql_username}" ping >/dev/null 2>&1 ||
    die "MySQL socket health check failed; provide the existing root password for verification."
fi
if command -v ss >/dev/null 2>&1; then
  ss -H -ltn "sport = :${mysql_port}" 2>/dev/null | grep -Eq "[:.]${mysql_port}[[:space:]]" ||
    die "MySQL target port is not listening."
else
  port_hex="$(printf '%04X' "${mysql_port}")"
  awk -v port="${port_hex}" 'NR > 1 { split($2, endpoint, ":"); if (endpoint[2] == port && $4 == "0A") found=1 } END {exit !found}' /proc/net/tcp /proc/net/tcp6 2>/dev/null ||
    die "MySQL target port is not listening."
fi
emit_progress 65 verify_version "正在核对 MySQL 版本"
"${install_dir}/bin/mysqld" --version | grep -Eq "Ver ${patch_version}([[:space:]]|$)" ||
  die "Managed MySQL binary version is not ${patch_version}."
commit_external_mysql
emit_progress 88 finalize_state "正在确认 MySQL 安装状态"
if [[ -f "${state_dir}/pending-version" ]]; then
  mv -f -- "${state_dir}/pending-version" "${state_dir}/version"
  mv -f -- "${state_dir}/pending-patch-version" "${state_dir}/patch-version"
else
  [[ -f "${state_dir}/version" && "$(<"${state_dir}/version")" == "${patch_version}" ]] ||
    die "MySQL pending installation state is missing."
fi
rm -f -- "${state_dir}/initialized-this-run"
rm -rf -- "${rollback_dir}"
emit_progress 100 verify_completed "MySQL 启动和健康检查通过"
echo "MySQL ${patch_version} verification passed."
