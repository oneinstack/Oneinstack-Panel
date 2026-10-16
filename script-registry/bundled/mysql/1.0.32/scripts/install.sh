#!/usr/bin/env bash
set -Eeuo pipefail
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"
require_root; validate_inputs; check_host
if [[ "${install_mode}" == "offline" ]]; then validate_offline_bundle; fi
emit_progress 5 install_dependencies "正在安装 MySQL 运行依赖"
install_dependencies
emit_progress 15 prepare_account "正在准备 MySQL 运行账户"
ensure_account
work_dir="$(mktemp -d /usr/local/src/oneinstack-mysql.XXXXXX)"
trap 'rm -rf -- "${work_dir}"' EXIT
archive="${work_dir}/mysql.tar.xz"
emit_progress 22 download "正在下载 MySQL 安装包"
download_verified "${archive}"
emit_progress 40 verify_checksum "MySQL 安装包校验完成"
emit_progress 48 extract "正在解压 MySQL 安装包"
stage="${work_dir}/stage"; install -d -m 0755 -- "${stage}"
tar -xJf "${archive}" --strip-components=1 -C "${stage}"
[[ -x "${stage}/bin/mysqld" ]] || die "Staged mysqld binary is missing."
emit_progress 62 verify_runtime_dependencies "正在检查 MySQL 运行库依赖"
require_command ldd
missing_libraries=""
for runtime_binary in mysqld mysql mysqladmin; do
  runtime_ldd_output="$(ldd "${stage}/bin/${runtime_binary}" 2>&1)" ||
    die "MySQL runtime dependencies could not be inspected for ${runtime_binary}."
  runtime_missing_libraries="$(awk '$2 == "=>" && $3 == "not" && $4 == "found" {print $1}' <<<"${runtime_ldd_output}")"
  [[ -z "${runtime_missing_libraries}" ]] || missing_libraries+="${runtime_missing_libraries}"$'\n'
done
missing_libraries="$(sort -u <<<"${missing_libraries}" | sed '/^$/d' | paste -sd, -)"
[[ -z "${missing_libraries}" ]] ||
  die "MySQL runtime libraries are missing after dependency installation: ${missing_libraries}."
emit_progress 75 prepare_rollback "正在创建 MySQL 回滚点"
prepare_rollback
emit_progress 85 install_files "正在部署 MySQL 安装文件"
install -d -m 0755 -- "$(dirname -- "${install_dir}")"
mv -- "${stage}" "${install_dir}"
chown -R root:root "${install_dir}"
install -d -o "${run_user}" -g "${run_group}" -m 0750 -- "${install_dir}/mysql-files"
printf '%s\n' "${software_version}" >"${state_dir}/pending-version"
printf '%s\n' "${patch_version}" >"${state_dir}/pending-patch-version"
emit_progress 100 install_completed "MySQL 安装文件部署完成"
echo "MySQL ${patch_version} binaries installed."
