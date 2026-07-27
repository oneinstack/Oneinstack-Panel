#!/usr/bin/env bash
set -Eeuo pipefail
source "$(dirname "$0")/common.sh"
validate_inputs

read_state() {
  local property="$1" value
  value="$(systemctl show php-fpm.service --property="${property}" --value 2>/dev/null || true)"
  [[ "${value}" =~ ^[a-z][a-z0-9_-]{0,31}$ ]] || value="unknown"
  printf '%s' "${value}"
}

runtime_version=""
if [[ -x "${install_dir}/bin/php" ]]; then
  runtime_version="$("${install_dir}/bin/php" -v 2>&1 | grep -Eo '[0-9]+(\.[0-9]+){1,2}' | head -n1 || true)"
fi

printf 'component=php\n'
printf 'service=php-fpm\n'
printf 'load_state=%s\n' "$(read_state LoadState)"
printf 'active_state=%s\n' "$(read_state ActiveState)"
printf 'sub_state=%s\n' "$(read_state SubState)"
printf 'unit_file_state=%s\n' "$(read_state UnitFileState)"
printf 'runtime_version=%s\n' "${runtime_version}"
printf 'can_reload=true\n'
