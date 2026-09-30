#!/usr/bin/env bash
set -Eeuo pipefail
# shellcheck source=common.sh
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"

require_root
load_install_parameters
validate_inputs
validate_password
[[ -x "${install_dir}/bin/opensearch" && -x "${install_dir}/jdk/bin/java" ]] || die "OpenSearch runtime is not installed."
ensure_account
install -d -o "${run_user}" -g "${run_group}" -m 0750 -- "${data_dir}" "${log_dir}"
emit_progress 12 security_config "正在生成 OpenSearch Security 配置；管理员密码不会写入日志或服务单元"
configure_demo_security
candidate="$(mktemp "${install_dir}/config/.opensearch.yml.XXXXXX")"
write_managed_config "${candidate}" "${config_file}"
mv -f -- "${candidate}" "${config_file}"
write_jvm_options "${jvm_options_file}"
chown -R "${run_user}:${run_group}" "${install_dir}/config" "${data_dir}" "${log_dir}"
write_unit
persist_install_parameters
emit_progress 55 service_start "正在启动 OpenSearch 并初始化安全索引"
service_start
if ! wait_for_service; then
  emit_startup_diagnostics
  die "OpenSearch systemd service did not become active."
fi
if ! wait_for_https_listener; then
  emit_startup_diagnostics
  die "OpenSearch HTTPS listener did not become reachable before Security initialization."
fi
emit_progress 75 security_initialize "正在初始化 OpenSearch Security 索引"
if ! initialize_security_index; then
  emit_startup_diagnostics
  die "OpenSearch Security index initialization failed."
fi
if ! wait_for_https; then
  emit_startup_diagnostics
  die "OpenSearch HTTPS listener did not become ready after Security initialization."
fi
: >"${state_dir}/password-configured"
emit_progress 100 configure_completed "OpenSearch 单节点、安全插件、JVM 与 systemd 配置已生效"
