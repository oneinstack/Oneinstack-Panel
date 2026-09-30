#!/usr/bin/env bash
set -Eeuo pipefail
# shellcheck source=common.sh
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"

require_root
load_persisted_parameters
validate_inputs
detect_platform
verify_runtime
report_phpmyadmin_integration
printf 'component=caddy\npackageVersion=%s\nsoftwareVersion=%s\nactualVersion=%s\nservice=%s\nport=%s\nrevision=%s\n' \
  "${package_version}" "${software_version}" "$(current_version)" "${service_name}" "${caddy_port}" "$(config_revision)"
emit_progress 100 verify_complete "Caddy runtime verification passed."
