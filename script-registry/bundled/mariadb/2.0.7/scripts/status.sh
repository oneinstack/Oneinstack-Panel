#!/usr/bin/env bash
set -Eeuo pipefail
# shellcheck source=common.sh
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"
validate_inputs

read_state() {
  local property="$1" value
  value="$(service_status_value "${property}")"
  [[ "${value}" =~ ^[a-z][a-z0-9_-]{0,31}$ ]] || value=unknown
  printf '%s' "${value}"
}
config_value() {
  local key="$1" fallback="$2" value
  value="$(sed -nE "s/^[[:space:]]*${key}[[:space:]]*=[[:space:]]*([^[:space:]#]+).*/\\1/p" "${config_file}" 2>/dev/null | tail -n1)"
  printf '%s' "${value:-${fallback}}"
}
unit_value() {
  local key="$1" fallback="$2" value
  value="$(sed -nE "s/^[[:space:]]*${key}=[[:space:]]*([^[:space:]#]+).*/\\1/p" "${unit_file}" 2>/dev/null | tail -n1)"
  printf '%s' "${value:-${fallback}}"
}

runtime_port="${mysql_port}"
runtime_bind_address="${bind_address}"
runtime_install_dir="${install_dir}"
runtime_data_dir="${data_dir}"
runtime_log_dir="${log_dir}"
runtime_user="${run_user}"
runtime_group="${run_group}"
if [[ -f "${config_file}" ]]; then
  runtime_port="$(config_value port "${runtime_port}")"
  runtime_bind_address="$(config_value bind-address "${runtime_bind_address}")"
  runtime_install_dir="$(config_value basedir "${runtime_install_dir}")"
  runtime_data_dir="$(config_value datadir "${runtime_data_dir}")"
  runtime_log_dir="$(dirname -- "$(config_value log-error "${runtime_log_dir}/mariadb-error.log")")"
  runtime_user="$(config_value user "${runtime_user}")"
fi
if [[ -f "${unit_file}" ]]; then
  runtime_user="$(unit_value User "${runtime_user}")"
  runtime_group="$(unit_value Group "${runtime_group}")"
fi
runtime_version=""
if [[ -x "${runtime_install_dir}/bin/mariadbd" ]]; then
  runtime_version="$("${runtime_install_dir}/bin/mariadbd" --version 2>&1 |
    grep -Eo '[0-9]+\.[0-9]+\.[0-9]+' | head -n1 || true)"
fi

printf 'component=mariadb\n'
printf 'service=mariadb\n'
printf 'port=%s\n' "${runtime_port}"
printf 'bind_address=%s\n' "${runtime_bind_address}"
printf 'install_dir=%s\n' "${runtime_install_dir}"
printf 'data_dir=%s\n' "${runtime_data_dir}"
printf 'log_dir=%s\n' "${runtime_log_dir}"
printf 'run_user=%s\n' "${runtime_user}"
printf 'run_group=%s\n' "${runtime_group}"
printf 'load_state=%s\n' "$(read_state LoadState)"
printf 'active_state=%s\n' "$(read_state ActiveState)"
printf 'sub_state=%s\n' "$(read_state SubState)"
printf 'unit_file_state=%s\n' "$(read_state UnitFileState)"
printf 'runtime_version=%s\n' "${runtime_version}"
printf 'can_reload=false\n'
