#!/usr/bin/env bash
set -Eeuo pipefail
# shellcheck source-path=SCRIPTDIR
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"
require_root
load_runtime_state
validate_common
validate_runtime_prerequisites
verify_managed_installation
write_runtime_state
write_installed_state
scheme=http
[[ "${web_port}" != 443 ]] || scheme=https
printf 'Adminer verification passed: %s://%s:%s%s\n' "${scheme}" "${web_host}" "${web_port}" "${public_path}"
emit_progress 100 verify_completed "Adminer Web Server, PHP-FPM, route, and HTTP verification passed"
