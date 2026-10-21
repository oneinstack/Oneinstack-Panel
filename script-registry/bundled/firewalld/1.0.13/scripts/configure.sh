#!/usr/bin/env bash
# shellcheck disable=SC2154
set -Eeuo pipefail
# shellcheck disable=SC1091,SC2154
source "$(dirname -- "${BASH_SOURCE[0]}")/common.sh"

require_root
validate_inputs
check_host
mkdir -p "${state_dir}"
write_default_managed_rules
firewalld_configuration_valid || die "CONFIG_INVALID" "firewalld configuration validation failed."
emit_progress 100 configure.completed "firewalld configuration is valid and preserved"
