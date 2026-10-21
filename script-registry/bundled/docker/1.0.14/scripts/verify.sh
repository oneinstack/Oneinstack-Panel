#!/usr/bin/env bash
# shellcheck disable=SC1091,SC2154
set -Eeuo pipefail
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"
require_root
validate_inputs
check_host
require_command docker
require_command systemctl
systemctl is-enabled --quiet containerd.service || die "containerd service is not enabled."
systemctl is-enabled --quiet docker.service || die "Docker service is not enabled."
version_pair="$(docker_version_pair)" || die "Docker daemon is not responding."
[[ "${version_pair}" == "29.8.0|29.8.0" ]] || die "Unexpected Docker client/server versions: ${version_pair}"
normalize_runtime_permissions
write_state
commit_migration
emit_progress 100 verify_completed "Docker Engine ${version_pair} verification completed"
