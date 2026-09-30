#!/usr/bin/env bash
set -Eeuo pipefail
# shellcheck source=common.sh
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"

require_root
load_install_parameters
validate_inputs
validate_password
service_is_active || die "OpenSearch systemd service is not active."
wait_for_https || die "OpenSearch HTTPS listener did not become ready."
if ! verify_authenticated_health; then
  emit_allocation_diagnostics
  die "OpenSearch authentication or cluster health verification failed; data and rollback snapshot were preserved."
fi
[[ "$(actual_version)" == "${software_version}" ]] || die "OpenSearch runtime version does not match ${software_version}."
install -d -m 0750 -- "${state_dir}"
printf '%s\n' "${software_version}" >"${state_dir}/version"
printf 'managed\n' >"${state_dir}/ownership"
: >"${state_dir}/installed"
rm -f -- "${state_dir}/pending-version"
rm -rf -- "${rollback_dir}"
emit_progress 100 verify_completed "OpenSearch HTTPS、管理员认证和集群健康验证通过"
printf 'OpenSearch %s verification passed\n' "${software_version}"
