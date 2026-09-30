#!/usr/bin/env bash
set -Eeuo pipefail
# shellcheck source=common.sh
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"

require_root
detect_host
if centos7_eol_runtime_profile; then
  # shellcheck source=container.sh
  source "${script_dir}/container.sh"
  container_verify
  exit 0
fi
is_managed || die 'NOT_MANAGED: ClamAV is not managed by Oneinstack.'
load_managed_state
[[ -x "${clamd_binary}" && -x "${clamdscan_binary}" && -x "${sigtool_binary}" && -f "${clamd_config}" && -f "${freshclam_config}" ]] ||
  die 'RUNTIME_PROFILE_INVALID: managed runtime files are incomplete.'
ensure_cvd_certificates
validate_freshclam_config
validate_database_files "${data_dir}"
validate_database_age
is_healthy || { health_failure_detail; die 'SERVICE_NOT_READY: ClamAV daemon or Unix Socket probe is unavailable.'; }
printf '%s verification passed; service=%s socket=%s runtime=%s database_age_hours=%s\n' "${component_name}" "${service_name}" "${socket_path}" "$(clamscan --version 2>/dev/null | awk '{print $2}' | head -n1)" "$(database_age_hours_from_data)"
