#!/usr/bin/env bash
set -Eeuo pipefail
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"
require_root; validate_inputs
[[ -f "${state_dir}/version" ]] ||
  die "Managed MySQL state is missing; refusing to remove unowned resources."
removed_dir="${state_dir}/removed/$(date -u +%Y%m%dT%H%M%SZ)"
install -d -m 0750 -- "${removed_dir}"
systemctl disable --now mysql 2>/dev/null || true
systemctl disable --now mysqld 2>/dev/null || true
rm -f -- /etc/init.d/mysqld /etc/rc*.d/*mysqld
[[ ! -e "${install_dir}" ]] || mv -- "${install_dir}" "${removed_dir}/install"
[[ ! -e "${unit_file}" ]] || mv -- "${unit_file}" "${removed_dir}/mysql.service"
[[ ! -e "${config_file}" ]] || mv -- "${config_file}" "${removed_dir}/my.cnf"
systemctl daemon-reload
systemctl reset-failed mysql.service mysqld.service 2>/dev/null || true
rm -f -- "${state_dir}/version" "${state_dir}/patch-version" "${state_dir}/pending-version" "${state_dir}/pending-patch-version"
echo "MySQL removed. Database data in ${data_dir} was preserved; removed binaries are in ${removed_dir}."
