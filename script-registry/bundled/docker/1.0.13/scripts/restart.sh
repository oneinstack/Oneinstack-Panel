#!/usr/bin/env bash
# shellcheck disable=SC1091,SC2154
set -Eeuo pipefail
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"
require_root
validate_inputs
require_command systemctl
ensure_service_started containerd.service "containerd service"
ensure_service_started docker.service "Docker service"
docker_version >/dev/null || die "Docker daemon did not become ready after restart."
emit_progress 100 restart_completed "Docker Engine restarted"
