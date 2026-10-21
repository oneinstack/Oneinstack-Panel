#!/usr/bin/env bash
# shellcheck disable=SC1091,SC2154
set -Eeuo pipefail
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"
require_root
validate_inputs
require_command systemctl
if [[ -d "${migration_dir}" ]]; then
  restore_existing
else
  ensure_service_started containerd.service "containerd service"
  ensure_service_started docker.service "Docker service"
  docker_version_pair >/dev/null || die "Docker daemon did not become ready during rollback."
  emit_progress 100 rollback_completed "Docker Engine service restored"
fi
