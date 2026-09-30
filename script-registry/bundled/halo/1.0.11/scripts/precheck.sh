#!/usr/bin/env bash
set -Eeuo pipefail
# shellcheck source=common.sh
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"

require_root
require_command realpath
[[ "${install_mode}" == center || "${install_mode}" == offline ]] || die "INVALID_INSTALL_MODE: expected center or offline."
validate_inputs
detect_host
select_artifacts
require_command sha256sum
has_systemd || die "HOST_INIT_UNSUPPORTED: Halo requires a running systemd instance."
if [[ "${install_mode}" == center ]]; then verify_checksum "${bundled_adoptium_key}" "${adoptium_key_sha256}"; fi
check_existing_ownership
validate_offline_bundle
if [[ "${database_type}" == h2 ]]; then
  printf 'WARNING: H2 is not recommended for production; select PostgreSQL, MySQL, or MariaDB for production workloads.\n' >&2
fi
emit_progress 100 precheck_completed "Halo 只读预检通过；主机、参数、所有权、端口和离线身份均已校验"
