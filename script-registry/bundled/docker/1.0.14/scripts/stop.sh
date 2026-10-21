#!/usr/bin/env bash
# shellcheck disable=SC1091,SC2154
set -Eeuo pipefail
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"
require_root
validate_inputs
require_command systemctl

stop_unit docker.socket || die "Failed to stop docker.socket."
stop_unit docker.service || die "Failed to stop docker.service."
stop_unit containerd.service || die "Failed to stop containerd.service."

for unit in docker.service docker.socket containerd.service; do
  if systemctl is-active --quiet "${unit}" 2>/dev/null; then
    die "Docker unit remains active: ${unit}"
  fi
done

emit_progress 100 stop_completed "Docker Engine stopped"
