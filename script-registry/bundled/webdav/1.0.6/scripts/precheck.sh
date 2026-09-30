#!/usr/bin/env bash
set -Eeuo pipefail
# shellcheck source=common.sh
source "$(dirname -- "${BASH_SOURCE[0]}")/common.sh"
require_root
validate_install
detect_host
ensure_unowned_safe
validate_bundle
ensure_requested_port_available
emit_progress 100 precheck_completed 'WebDAV host, credentials, package source, and port validated'
