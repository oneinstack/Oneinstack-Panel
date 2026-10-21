#!/usr/bin/env bash
# shellcheck disable=SC1091,SC2154
set -Eeuo pipefail
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"

require_root
validate_inputs
if [[ -d "${migration_dir}" ]]; then
  restore_previous
elif [[ -f "${state_dir}/installed.json" ]]; then
  compose_version >/dev/null || die "Managed Docker Compose command is unavailable."
  emit_progress 100 rollback.completed "Docker Compose managed installation remains healthy"
else
  emit_progress 100 rollback.not_required "No Docker Compose migration state requires rollback"
fi
