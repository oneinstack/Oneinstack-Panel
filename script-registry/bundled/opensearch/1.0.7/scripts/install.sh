#!/usr/bin/env bash
set -Eeuo pipefail
# shellcheck source=common.sh
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"

require_root
load_install_parameters
validate_inputs
validate_password
validate_kernel_settings
[[ "${install_mode}" != offline ]] || validate_offline_bundle
emit_progress 8 dependencies "正在准备 OpenSearch 运行依赖"
install_dependencies
for command in curl sha256sum sha512sum tar systemctl ss; do require_command "${command}"; done
gpg_binary >/dev/null || die "Required command not found: gpg or gpg2"
emit_progress 22 artifact_verify "正在解析并验证 OpenSearch 官方制品、SHA-512 与 GPG 签名"
release="$(resolve_release)"
install -d -m 0755 -- "$(dirname -- "${install_dir}")"
staging="$(mktemp -d "$(dirname -- "${install_dir}")/.opensearch-stage.XXXXXX")"
cleanup_staging() { [[ -z "${staging:-}" ]] || rm -rf -- "${staging}"; }
trap cleanup_staging EXIT
tar -xzf "${release}" -C "${staging}" --strip-components=1
[[ -x "${staging}/bin/opensearch" && -x "${staging}/jdk/bin/java" && -d "${staging}/plugins/opensearch-security" ]] ||
  die "Verified OpenSearch archive does not contain the expected binary, bundled JDK, and Security plugin."
emit_progress 55 install_snapshot "正在创建可回滚程序与 systemd 快照"
snapshot_runtime
mv -- "${staging}" "${install_dir}"
staging=""
ensure_account
chown -R "${run_user}:${run_group}" "${install_dir}"
install -d -o "${run_user}" -g "${run_group}" -m 0750 -- "${data_dir}" "${log_dir}"
install -d -m 0750 -- "${state_dir}"
printf '%s\n' "${software_version}" >"${state_dir}/pending-version"
emit_progress 100 install_completed "OpenSearch 程序已部署；等待安全配置和运行验证"
