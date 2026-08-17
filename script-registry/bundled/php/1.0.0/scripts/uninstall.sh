#!/usr/bin/env bash
set -Eeuo pipefail
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"
require_root; validate_inputs
[[ -f "${state_dir}/version" ]] ||
  die "Managed PHP state is missing; refusing to remove unowned resources."
removed_dir="${state_dir}/removed/$(date -u +%Y%m%dT%H%M%SZ)"
install -d -m 0750 -- "${removed_dir}"
systemctl disable --now php-fpm 2>/dev/null || true
[[ ! -e "${install_dir}" ]] || mv -- "${install_dir}" "${removed_dir}/install"
[[ ! -e "${unit_file}" ]] || mv -- "${unit_file}" "${removed_dir}/php-fpm.service"
systemctl daemon-reload
rm -f -- "${state_dir}/version" "${state_dir}/patch-version" "${state_dir}/pending-version" "${state_dir}/pending-patch-version"
echo "PHP-FPM removed. Website data was not modified; removed binaries are in ${removed_dir}."
