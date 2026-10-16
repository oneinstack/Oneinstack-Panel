#!/usr/bin/env bash
set -Eeuo pipefail
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"

require_root
validate_inputs
check_host
require_command systemctl
require_command install
require_command realpath
if [[ "${install_mode}" == "offline" ]]; then
  has_local_packages || die "Offline Fail2ban ${software_version} package artifacts are missing; exact source runtimes require Center online access."
fi
echo "Fail2ban component precheck passed."
