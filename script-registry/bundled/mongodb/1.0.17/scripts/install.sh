#!/usr/bin/env bash
set -Eeuo pipefail
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"

require_root; validate_inputs; detect_host; select_source
[[ "${install_mode}" != offline ]] || validate_offline_bundle
if managed_installation_present && [[ "$(<"${state_dir}/version")" == "${software_version}" ]]; then
  emit_progress 100 already_installed "MongoDB ${software_version} 已安装，跳过二进制替换"
  exit 0
fi
emit_progress 8 install_dependencies "正在安装 MongoDB 运行依赖"
install_dependencies
emit_progress 20 prepare_account "正在准备 MongoDB 运行账号"
ensure_account
work_dir="$(mktemp -d /tmp/oneinstack-mongodb.XXXXXX)"
trap 'rm -rf -- "${work_dir}"' EXIT
server_file="${work_dir}/${server_archive}"
shell_file="${work_dir}/${mongosh_archive}"
emit_progress 28 fetch_server "正在获取并校验 MongoDB 官方制品"
obtain_verified_artifact "${server_archive}" "${server_url}" "${server_signature_url}" "${server_sha256}" \
  "${server_key_url}" "${server_key_sha256}" "${server_key_fingerprint}" "${server_file}"
emit_progress 42 fetch_shell "正在获取并校验 mongosh 2.10.0 制品"
obtain_verified_artifact "${mongosh_archive}" "${mongosh_url}" "${mongosh_signature_url}" "${mongosh_sha256}" \
  "${mongosh_key_url}" "${mongosh_key_sha256}" "${mongosh_key_fingerprint}" "${shell_file}"
stage="${work_dir}/stage"; shell_stage="${work_dir}/shell"
install -d -m 0755 -- "${stage}" "${shell_stage}"
tar -xzf "${server_file}" --strip-components=1 -C "${stage}"
tar -xzf "${shell_file}" --strip-components=1 -C "${shell_stage}"
[[ -x "${stage}/bin/mongod" ]] || die "ARTIFACT_INVALID: staged mongod is missing."
shell_binary="$(find "${shell_stage}" -type f -path '*/bin/mongosh' -print -quit)"
[[ -n "${shell_binary}" && -x "${shell_binary}" ]] || die "ARTIFACT_INVALID: staged mongosh is missing."
install -m 0755 -- "${shell_binary}" "${stage}/bin/mongosh"
"${stage}/bin/mongod" --version | grep -Fq "db version v${software_version}" || die "ARTIFACT_VERSION_MISMATCH: mongod version is not ${software_version}."
"${stage}/bin/mongosh" --version | grep -Fqx "${mongosh_version}" || die "ARTIFACT_VERSION_MISMATCH: mongosh version is not ${mongosh_version}."
emit_progress 72 prepare_rollback "正在创建 MongoDB 事务回滚点"
prepare_rollback
emit_progress 84 install_files "正在部署 MongoDB 受管二进制"
install -d -m 0755 -- "$(dirname -- "${install_dir}")"
mv -- "${stage}" "${install_dir}"
chown -R root:root "${install_dir}"
printf '%s\n' "${software_version}" >"${state_dir}/pending-version"
printf '%s\n' "${server_sha256}" >"${state_dir}/pending-source-sha256"
emit_progress 100 install_completed "MongoDB ${software_version} 二进制部署完成"
