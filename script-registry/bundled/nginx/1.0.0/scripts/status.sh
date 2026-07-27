#!/usr/bin/env bash
set -Eeuo pipefail
source "$(dirname "$0")/common.sh"
validate_inputs

read_state() {
  local property="$1" value
  if ! systemd_available; then
    case "${property}" in
      LoadState) [[ -x "${nginx_binary}" ]] && value="loaded" || value="not-found" ;;
      ActiveState) nginx_is_running && value="active" || value="inactive" ;;
      SubState) nginx_is_running && value="running" || value="dead" ;;
      UnitFileState) value="direct" ;;
    esac
    printf '%s' "${value}"
    return
  fi
  value="$(systemctl show nginx.service --property="${property}" --value 2>/dev/null || true)"
  [[ "${value}" =~ ^[a-z][a-z0-9_-]{0,31}$ ]] || value="unknown"
  printf '%s' "${value}"
}

runtime_version=""
if [[ -x "${install_dir}/sbin/nginx" ]]; then
  runtime_version="$("${install_dir}/sbin/nginx" -v 2>&1 | grep -Eo '[0-9]+(\.[0-9]+){1,2}' | head -n1 || true)"
fi

printf 'component=nginx\n'
printf 'service=nginx\n'
printf 'load_state=%s\n' "$(read_state LoadState)"
printf 'active_state=%s\n' "$(read_state ActiveState)"
printf 'sub_state=%s\n' "$(read_state SubState)"
printf 'unit_file_state=%s\n' "$(read_state UnitFileState)"
printf 'runtime_version=%s\n' "${runtime_version}"
printf 'can_reload=true\n'
