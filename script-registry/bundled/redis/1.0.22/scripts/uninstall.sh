#!/usr/bin/env bash
set -Eeuo pipefail
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"
require_root; validate_inputs
[[ -f "${state_dir}/version" ]] ||
  die "Managed Redis state is missing; refusing to remove unowned resources."
archive_retention_days="${ARTIFACT_RETENTION_DAYS:-0}"
archive_retention_confirmed="${ARTIFACT_RETENTION_CONFIRMED:-false}"
[[ "${archive_retention_days}" == 0 || "${archive_retention_days}" == 30 ]] ||
  die "ARTIFACT_RETENTION_DAYS must be 0 or 30."
if [[ "${archive_retention_days}" == 30 ]]; then
  [[ "${archive_retention_confirmed}" == true ]] || die "Archive retention requires explicit confirmation."
  resolved_install="$(realpath -m -- "${install_dir}")"
  resolved_data="$(realpath -m -- "${data_dir}")"
  [[ "${resolved_data}" != "${resolved_install}" && "${resolved_data}" != "${resolved_install}/"* ]] ||
    die "Data path is inside the install archive; automatic retention is unsafe."
fi
removed_dir="${state_dir}/removed/$(date -u +%Y%m%dT%H%M%SZ)"
install -d -m 0750 -- "${removed_dir}"
systemctl disable --now redis 2>/dev/null || true
[[ ! -e "${install_dir}" ]] || mv -- "${install_dir}" "${removed_dir}/install"
[[ ! -e "${unit_file}" ]] || mv -- "${unit_file}" "${removed_dir}/redis.service"
systemctl daemon-reload
remove_managed_path_acl_entries "${managed_path_acl_file}"
rm -f -- "${state_dir}/version" "${state_dir}/pending-version"
rm -f -- "${managed_path_acl_file}"
printf '%s\0' \
  schema 1 component redis packageVersion 1.0.22 \
  installPath "${install_dir}" dataPath "${data_dir}" logPath "" \
  taskId "${ONEINSTACK_TASK_ID:-}" contentType program-config \
  retentionDays "${archive_retention_days}" retentionConfirmed "${archive_retention_confirmed}" \
  createdAt "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  >"${removed_dir}/.oneinstack-artifact.meta"
chmod 0640 -- "${removed_dir}/.oneinstack-artifact.meta"
echo "Redis removed. Data in ${data_dir} was preserved; removed binaries are in ${removed_dir}."
