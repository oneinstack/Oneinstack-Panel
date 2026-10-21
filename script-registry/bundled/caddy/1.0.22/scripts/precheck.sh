#!/usr/bin/env bash
set -Eeuo pipefail
# shellcheck source=common.sh
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"

require_root
validate_inputs
detect_platform
install -d -m 0750 -- "${state_dir}"
require_commands
systemd_available || die_code CADDY_SYSTEMD_REQUIRED "Caddy requires a running systemd service manager."
precheck_runtime_conflicts
if [[ "${install_mode}" == "offline" ]]; then
  artifact_metadata
  resolve_offline_artifact >/dev/null
fi
available_kb="$(df -Pk "$(dirname -- "${install_dir}")" 2>/dev/null | awk 'NR == 2 {print $4}')"
[[ "${available_kb:-0}" -ge 102400 ]] || die_code CADDY_DISK_SPACE_LOW "At least 100 MiB free space is required below /usr/local."
emit_progress 100 precheck_complete "Caddy precheck passed for ${detected_os_id} ${detected_os_version} ${detected_architecture} (${install_mode})."
