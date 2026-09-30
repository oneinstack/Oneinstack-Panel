#!/usr/bin/env bash
set -Eeuo pipefail
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"
require_root
validate_inputs
check_host
check_base_commands
check_upgrade_direction
install -d -m 0755 -- /usr/local/src
work_dir="$(mktemp -d /usr/local/src/oneinstack-redis.XXXXXX)"
trap cleanup_work EXIT
resolve_source
emit_progress 5 install_dependencies "正在安装 Redis 编译依赖"
install_dependencies
emit_progress 15 prepare_account "正在准备 Redis 运行账户"
ensure_account
emit_progress 42 extract "正在解压 Redis 源码"
tar -xzf "${source_path}" -C "${work_dir}"
source_dir="${work_dir}/redis-${software_version}"; [[ -d "${source_dir}" ]] || die "Unexpected Redis archive layout."
emit_progress 52 compile "正在编译 Redis"
make -C "${source_dir}" -j"$(nproc)" BUILD_TLS=yes
emit_progress 78 install_files "正在暂存 Redis 安装文件"
stage="${work_dir}/stage"; install -d -m 0755 -- "${stage}/bin" "${stage}/etc"
for binary in redis-server redis-cli redis-benchmark redis-check-aof redis-check-rdb; do
  install -m 0755 -- "${source_dir}/src/${binary}" "${stage}/bin/${binary}"
done
emit_progress 90 prepare_rollback "正在创建 Redis 回滚点"
prepare_rollback
install -d -m 0755 -- "$(dirname -- "${install_dir}")"
mv -- "${stage}" "${install_dir}"
printf '%s\n' "${software_version}" >"${state_dir}/pending-version"
printf '%s\n' "${source_sha256}" >"${state_dir}/pending-source-sha256"
printf '%s\n' "${install_mode}" >"${state_dir}/pending-install-mode"
emit_progress 100 install_completed "Redis 安装文件部署完成"
echo "Redis ${software_version} binaries installed."
