#!/usr/bin/env bash
# shellcheck disable=SC1091,SC2154
set -Eeuo pipefail
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"

require_root
validate_inputs
[[ -f "${state_dir}/installed.json" ]] ||
  die "Managed Docker Compose installation state is missing; refusing to remove external plugins."

emit_progress 30 uninstall.plugin.removing "Removing the OneinStack-managed Docker Compose plugin"
if [[ -L "${plugin_path}" && "$(readlink -- "${plugin_path}")" == "${managed_root}"/* ]]; then
  rm -f -- "${plugin_path}"
elif [[ -f "${plugin_path}" ]]; then
  die "Docker Compose plugin is not managed by OneinStack; refusing to remove it."
fi
rm -rf -- "${version_root}"
if [[ -d "${state_dir}/migration" ]]; then
  restore_previous
elif [[ -d "${state_dir}/previous" ]]; then
  restore_previous_from_root "${state_dir}/previous"
fi
remove_state
emit_progress 100 uninstall.completed "Docker Compose plugin removed; Docker Engine and container data preserved"
