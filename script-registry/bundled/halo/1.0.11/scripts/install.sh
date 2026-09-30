#!/usr/bin/env bash
set -Eeuo pipefail
# shellcheck source=common.sh
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"

require_root
require_command realpath
validate_inputs
detect_host
select_artifacts
has_systemd || die "HOST_INIT_UNSUPPORTED: Halo requires systemd."
check_existing_ownership
validate_offline_bundle
emit_progress 8 dependency_install "正在安装 Halo 生命周期所需的最小系统工具"
install_dependencies
for command_name in getfacl setfacl sha256sum tar timeout; do require_command "${command_name}"; done
gpg_command >/dev/null
[[ "${install_mode}" == offline ]] || require_command curl
install -d -m 0750 -- "${state_dir}"
snapshot_transaction
restored=false
restore_on_error() {
  local code="${1:-$?}"
  if [[ "${restored}" != true ]]; then restored=true; restore_transaction; fi
  exit "${code}"
}
trap 'restore_on_error $?' ERR
trap 'restore_on_error 130' INT
trap 'restore_on_error 143' TERM
emit_progress 28 runtime_identity "正在创建非 root Halo 运行身份与受管路径"
ensure_runtime_account
rm -rf -- "${install_dir}"
prepare_runtime_directories
emit_progress 45 artifact_download "正在获取并验证 Halo JAR、Temurin JRE、签名和发布者指纹"
mapfile -t artifact_paths < <(prepare_artifacts)
((${#artifact_paths[@]} == 4)) || die "ARTIFACT_PREPARATION_FAILED: expected four verified inputs."
emit_progress 75 artifact_install "正在安装固定版本的 Halo JAR 与私有 Temurin Java 21"
install_runtime_artifacts "${artifact_paths[0]}" "${artifact_paths[1]}"
printf '%s\n' "${software_version}" >"${state_dir}/pending-version"
printf '%s\n' "${jar_sha256}" >"${state_dir}/pending-jar-sha256"
trap - ERR INT TERM
emit_progress 100 install_completed "Halo 程序和私有 JRE 已安装，等待配置与健康验证"
