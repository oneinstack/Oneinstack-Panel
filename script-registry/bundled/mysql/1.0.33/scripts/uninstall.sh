#!/usr/bin/env bash
set -Eeuo pipefail
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"
require_root; validate_inputs
[[ -f "${state_dir}/version" ]] ||
  die "Managed MySQL state is missing; refusing to remove unowned resources."
data_policy="${DATA_POLICY:-${MYSQL_DATA_POLICY:-preserve}}"
delete_confirm="${DELETE_DATA_CONFIRM:-${MYSQL_DELETE_DATA_CONFIRM:-false}}"
[[ "${data_policy}" == preserve || "${data_policy}" == delete ]] || die "DATA_POLICY must be preserve or delete."
if [[ "${data_policy}" == delete && "${delete_confirm}" != true ]]; then
  die "DATA_POLICY=delete requires DELETE_DATA_CONFIRM=true."
fi
archive_retention_days="${ARTIFACT_RETENTION_DAYS:-0}"
archive_retention_confirmed="${ARTIFACT_RETENTION_CONFIRMED:-false}"
[[ "${archive_retention_days}" == 0 || "${archive_retention_days}" == 30 ]] ||
  die "ARTIFACT_RETENTION_DAYS must be 0 or 30."
if [[ "${archive_retention_days}" == 30 ]]; then
  [[ "${archive_retention_confirmed}" == true ]] || die "Archive retention requires explicit confirmation."
  resolved_install="$(realpath -m -- "${install_dir}")"
  resolved_data="$(realpath -m -- "${data_dir}")"
  resolved_log="$(realpath -m -- "${log_dir}")"
  [[ "${resolved_data}" != "${resolved_install}" && "${resolved_data}" != "${resolved_install}/"* &&
     "${resolved_log}" != "${resolved_install}" && "${resolved_log}" != "${resolved_install}/"* ]] ||
    die "Data or log path is inside the install archive; automatic retention is unsafe."
fi
removed_dir="${state_dir}/removed/$(date -u +%Y%m%dT%H%M%SZ)"
install -d -m 0750 -- "${removed_dir}"
service_stop
if has_systemd; then
  systemctl disable mysql.service 2>/dev/null || true
  systemctl daemon-reload
  systemctl reset-failed mysql.service 2>/dev/null || true
fi
[[ ! -e "${install_dir}" ]] || mv -- "${install_dir}" "${removed_dir}/install"
[[ ! -e "${unit_file}" ]] || mv -- "${unit_file}" "${removed_dir}/mysql.service"
[[ ! -e "${config_file}" ]] || mv -- "${config_file}" "${removed_dir}/my.cnf"
rm -f -- "${state_dir}/version" "${state_dir}/patch-version" "${state_dir}/pending-version" "${state_dir}/pending-patch-version"
if [[ "${data_policy}" == delete ]]; then
  rm -rf -- "${data_dir}"
  [[ "${log_dir}" == "${data_dir}" ]] || rm -rf -- "${log_dir}"
fi
remove_managed_path_acl_entries "${managed_path_acl_file}"
rm -f -- "${managed_path_acl_file}"
printf '%s\0' \
  schema 1 component mysql packageVersion 1.0.33 \
  installPath "${install_dir}" dataPath "${data_dir}" logPath "${log_dir}" \
  taskId "${ONEINSTACK_TASK_ID:-}" contentType program-config \
  retentionDays "${archive_retention_days}" retentionConfirmed "${archive_retention_confirmed}" \
  createdAt "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  >"${removed_dir}/.oneinstack-artifact.meta"
chmod 0640 -- "${removed_dir}/.oneinstack-artifact.meta"
echo "MySQL removed; managed data and logs were ${data_policy}."
