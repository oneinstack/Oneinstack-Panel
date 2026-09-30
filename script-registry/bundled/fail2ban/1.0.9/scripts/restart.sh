#!/usr/bin/env bash
set -Eeuo pipefail
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"
require_root; validate_inputs; require_command systemctl
fail2ban-client -t
systemctl restart "${service_name}"
wait_for_service_ready
