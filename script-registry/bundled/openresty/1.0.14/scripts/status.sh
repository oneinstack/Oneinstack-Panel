#!/usr/bin/env bash
# shellcheck source=common.sh
set -Eeuo pipefail
source "$(dirname "$0")/common.sh"
validate_inputs

read_state() {
  local property="$1" value
  if ! systemd_available; then
    case "${property}" in
      LoadState) [[ -n "${runtime_binary}" ]] && value="loaded" || value="not-found" ;;
      ActiveState) openresty_is_running && value="active" || value="inactive" ;;
      SubState) openresty_is_running && value="running" || value="dead" ;;
      UnitFileState) value="direct" ;;
    esac
    printf '%s' "${value}"
    return
  fi
  status_unit="${service_name}.service"
  if legacy_openresty_service_matches && systemctl is-active --quiet "${legacy_service_name}.service" && ! systemctl is-active --quiet "${service_name}.service"; then
    status_unit="${legacy_service_name}.service"
  fi
  value="$(systemctl_property_value "${status_unit}" "${property}")"
  [[ "${value}" =~ ^[a-z][a-z0-9_-]{0,31}$ ]] || value="unknown"
  printf '%s' "${value}"
}

runtime_version=""
runtime_binary="$(openresty_runtime_binary || true)"
if [[ -n "${runtime_binary}" ]]; then
  runtime_version="$("${runtime_binary}" -v 2>&1 | grep -Eo '[0-9]+(\.[0-9]+){1,3}' | head -n1 || true)"
fi

printf 'component=openresty\n'
printf 'service=%s\n' "${service_name}"
printf 'load_state=%s\n' "$(read_state LoadState)"
printf 'active_state=%s\n' "$(read_state ActiveState)"
printf 'sub_state=%s\n' "$(read_state SubState)"
printf 'unit_file_state=%s\n' "$(read_state UnitFileState)"
printf 'runtime_version=%s\n' "${runtime_version}"
printf 'can_reload=true\n'
