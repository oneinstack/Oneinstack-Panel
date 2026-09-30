#!/usr/bin/env bash
set -Eeuo pipefail
# shellcheck source=common.sh
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"
require_root; load_install_parameters; validate_inputs
managed_installation_present || die "Managed OpenSearch installation state is missing."
service_restart
if ! wait_for_service || ! wait_for_https; then die "OpenSearch did not become HTTPS-ready after restart."; fi
