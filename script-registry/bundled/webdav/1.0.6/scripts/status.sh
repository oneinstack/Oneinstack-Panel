#!/usr/bin/env bash
set -Eeuo pipefail
# shellcheck source=common.sh
source "$(dirname -- "${BASH_SOURCE[0]}")/common.sh"
read_state() {
  local value
  value="$(systemd_property "$1" || true)"
  [[ "$value" =~ ^[a-z][a-z0-9_-]{0,31}$ ]] || value="$2"
  printf '%s' "$value"
}
load_state=not-found; active_state=inactive; sub_state=dead; unit_file_state=disabled
if command -v systemctl >/dev/null 2>&1; then
  load_state="$(read_state LoadState not-found)"
  active_state="$(read_state ActiveState inactive)"
  sub_state="$(read_state SubState dead)"
  unit_file_state="$(read_state UnitFileState disabled)"
fi
runtime_version=
if managed && [[ -f "$state_dir/install-dir" ]]; then
  recorded_install_dir="$(cat "$state_dir/install-dir")"
  if [[ "$recorded_install_dir" == /* && ! -L "$recorded_install_dir" ]]; then
    install_dir="$recorded_install_dir"
  fi
fi
if [[ -x "$install_dir/webdav" ]]; then
  runtime_version="$("$install_dir/webdav" version 2>/dev/null | grep -Eo '[0-9]+\.[0-9]+\.[0-9]+' | head -n1 || true)"
fi
if [[ "$active_state" == active && -f "$state_dir/installed" ]]; then
  if ! command -v curl >/dev/null 2>&1; then
    sub_state='probe-unavailable'
  elif ! probe_target="$(read_config >/dev/null 2>&1; probe_url)"; then
    sub_state='config-invalid'
  else
    probe_code="$(curl --noproxy '*' --silent --insecure --max-time 3 --output /dev/null \
      --write-out '%{http_code}' -X PROPFIND -H 'Depth: 0' "$probe_target" 2>/dev/null || true)"
    [[ "$probe_code" == 401 ]] || sub_state='probe-failed'
  fi
fi
printf 'component=webdav\nservice=webdav\nload_state=%s\nactive_state=%s\nsub_state=%s\nunit_file_state=%s\nruntime_version=%s\ncan_reload=false\n' \
  "$load_state" "$active_state" "$sub_state" "$unit_file_state" "$runtime_version"
