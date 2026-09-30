#!/usr/bin/env bash
set -Eeuo pipefail
# shellcheck source=common.sh
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"

require_root
validate_inputs
{ [[ -f "${state_dir}/version" ]] || legacy_managed_installation_present; } ||
  die "Managed MariaDB state is missing; refusing to remove unowned resources."

data_policy="${DATA_POLICY:-preserve}"
delete_confirm="${DELETE_DATA_CONFIRM:-false}"
[[ "${data_policy}" == preserve || "${data_policy}" == delete ]] ||
  die "DATA_POLICY must be preserve or delete."
if [[ "${data_policy}" == delete && "${delete_confirm}" != true ]]; then
  die "DATA_POLICY=delete requires DELETE_DATA_CONFIRM=true."
fi
if [[ "${data_policy}" == delete ]]; then
  managed_data_dir="$(persisted_runtime_value DATA_DIR || true)"
  managed_log_dir="$(persisted_runtime_value LOG_DIR || true)"
  [[ -n "${managed_data_dir}" && "${data_dir}" == "${managed_data_dir}" ]] ||
    die "Managed MariaDB state does not prove ownership of DATA_DIR; refusing deletion."
  [[ -n "${managed_log_dir}" && "${log_dir}" == "${managed_log_dir}" ]] ||
    die "Managed MariaDB state does not prove ownership of LOG_DIR; refusing deletion."
fi

removed_dir="${state_dir}/removed/$(date -u +%Y%m%dT%H%M%SZ)"
install -d -m 0750 -- "${removed_dir}"
service_stop
systemctl stop mysqld.service 2>/dev/null || true
systemctl disable mariadb.service 2>/dev/null || true
legacy_managed_installation_present && systemctl disable mysqld.service 2>/dev/null || true

[[ ! -e "${install_dir}" ]] || mv -- "${install_dir}" "${removed_dir}/install"
[[ ! -e "${unit_file}" ]] || mv -- "${unit_file}" "${removed_dir}/mariadb.service"
[[ ! -e "${config_file}" ]] || mv -- "${config_file}" "${removed_dir}/my.cnf"
if legacy_managed_installation_present; then
  [[ ! -e /etc/systemd/system/mysqld.service ]] ||
    mv -- /etc/systemd/system/mysqld.service "${removed_dir}/legacy-mysqld.service"
  [[ ! -e /etc/init.d/mysqld ]] || mv -- /etc/init.d/mysqld "${removed_dir}/legacy-mysqld.init"
fi
systemctl daemon-reload
systemctl reset-failed mariadb.service mysqld.service 2>/dev/null || true

rm -f -- "${state_dir}/version" "${state_dir}/patch-version" \
  "${state_dir}/pending-version" "${state_dir}/pending-patch-version" \
  "${state_dir}/install-parameters" "${legacy_state_file}"
if [[ "${data_policy}" == delete ]]; then
  rm -rf -- "${data_dir}"
  [[ "${log_dir}" == "${data_dir}" ]] || rm -rf -- "${log_dir}"
fi
printf 'MariaDB removed; managed data and logs were %s.\n' "${data_policy}"
