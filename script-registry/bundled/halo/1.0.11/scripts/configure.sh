#!/usr/bin/env bash
set -Eeuo pipefail
# shellcheck source=common.sh
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"

require_root
validate_inputs
[[ -d "${rollback_dir}" && -f "${state_dir}/pending-version" ]] || die "TRANSACTION_STATE_MISSING: run install before configure."
[[ -x "${jre_dir}/bin/java" && -f "${jar_file}" ]] || die "ARTIFACT_MISSING: managed Halo runtime is incomplete."
restored=false
restore_on_error() {
  local code="${1:-$?}"
  if [[ "${restored}" != true ]]; then restored=true; restore_transaction; fi
  exit "${code}"
}
trap 'restore_on_error $?' ERR
trap 'restore_on_error 130' INT
trap 'restore_on_error 143' TERM
emit_progress 18 config_secret "正在写入受保护的数据库凭据环境文件"
write_environment_file
emit_progress 38 config_application "正在生成 root:halo 0640 的 application.yaml"
write_application_config
migrate_legacy_data
chown -R "${run_user}:${run_group}" "${data_dir}"
persist_install_parameters
emit_progress 62 config_service "正在生成非 root systemd 服务与安全限制"
write_service_unit
emit_progress 78 config_start "正在启动 Halo 并等待 readiness"
service_start
wait_for_readiness || die "READINESS_FAILED: Halo did not become ready after configuration."
trap - ERR INT TERM
emit_progress 100 configure_completed "Halo 配置已写入并通过启动探测"
