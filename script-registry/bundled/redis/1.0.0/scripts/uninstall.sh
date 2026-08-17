#!/usr/bin/env bash
set -Eeuo pipefail
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"
require_root; validate_inputs
[[ -f "${state_dir}/version" ]] ||
  die "Managed Redis state is missing; refusing to remove unowned resources."
removed_dir="${state_dir}/removed/$(date -u +%Y%m%dT%H%M%SZ)"
install -d -m 0750 -- "${removed_dir}"
systemctl disable --now redis 2>/dev/null || true
[[ ! -e "${install_dir}" ]] || mv -- "${install_dir}" "${removed_dir}/install"
[[ ! -e "${unit_file}" ]] || mv -- "${unit_file}" "${removed_dir}/redis.service"
systemctl daemon-reload
rm -f -- "${state_dir}/version" "${state_dir}/pending-version"
echo "Redis removed. Data in ${data_dir} was preserved; removed binaries are in ${removed_dir}."
