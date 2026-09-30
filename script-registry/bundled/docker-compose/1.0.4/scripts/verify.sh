#!/usr/bin/env bash
# shellcheck disable=SC1091,SC2154
set -Eeuo pipefail
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"

require_root
validate_inputs
check_host
check_engine
runtime_version="$(compose_version)" || die "Docker Compose command is not responding."
[[ "${runtime_version}" == "5.5.1" ]] || die "Unexpected Docker Compose version: ${runtime_version}"
write_state
commit_migration
emit_progress 100 verify.completed "Docker Compose ${runtime_version} verification completed"
