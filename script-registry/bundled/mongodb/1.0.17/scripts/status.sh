#!/usr/bin/env bash
set -Eeuo pipefail
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"
validate_inputs

read_state() {
  local property="$1" value
  value="$(systemctl show mongod.service --property="${property}" --value 2>/dev/null || true)"
  [[ "${value}" =~ ^[a-z][a-z0-9_-]{0,31}$ ]] || value=unknown
  printf '%s' "${value}"
}
runtime_version=""; [[ -x "${install_dir}/bin/mongod" ]] && runtime_version="$(actual_version 2>/dev/null || true)"
runtime_port="${mongodb_port}"; runtime_bind="${mongodb_bind_ip}"
if [[ -f "${config_file}" ]]; then
  runtime_port="$(config_scalar net port "${runtime_port}")"
  runtime_bind="$(config_scalar net bindIp "${runtime_bind}")"
fi
printf 'component=mongodb\nservice=mongod\nport=%s\nbind_address=%s\n' "${runtime_port}" "${runtime_bind}"
printf 'install_dir=%s\ndata_dir=%s\nlog_dir=%s\nrun_user=%s\nrun_group=%s\n' "${install_dir}" "${data_dir}" "${log_dir}" "${run_user}" "${run_group}"
printf 'load_state=%s\nactive_state=%s\nsub_state=%s\nunit_file_state=%s\n' "$(read_state LoadState)" "$(read_state ActiveState)" "$(read_state SubState)" "$(read_state UnitFileState)"
printf 'runtime_version=%s\ncan_reload=false\n' "${runtime_version}"
