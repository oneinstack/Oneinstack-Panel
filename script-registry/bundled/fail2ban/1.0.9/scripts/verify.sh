#!/usr/bin/env bash
set -Eeuo pipefail
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"

require_root
validate_inputs
require_command fail2ban-client
[[ -x "${helper_path}" && -r "${action_path}" && -r "${filter_path}" ]] || die "OneinStack Fail2ban integration is incomplete."
[[ -x "${redis_acl_helper}" && -r "${redis_auth_filter_path}" ]] || die "OneinStack Redis Fail2ban integration is incomplete."
[[ -r "${defaults_path}" ]] || die "OneinStack Fail2ban default configuration is missing."
systemctl is-active --quiet "${service_name}" || die "Fail2ban service is not active."
systemctl is-active --quiet "${redis_acl_service}" || die "OneinStack Redis ACL collector is not active."
fail2ban-client -t
validate_runtime_version
wait_for_service_ready
commit_migration
echo "Fail2ban verification passed."
