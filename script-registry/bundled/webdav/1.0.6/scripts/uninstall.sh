#!/usr/bin/env bash
set -Eeuo pipefail
# shellcheck source=common.sh
source "$(dirname -- "${BASH_SOURCE[0]}")/common.sh"
require_root
managed || die 'MANAGED_STATE_MISSING: refusing to remove unowned WebDAV resources.'
install_dir="$(cat "$state_dir/install-dir")"
data_dir="$(cat "$state_dir/data-dir")"
validate_common
verify_managed_paths
preserve_data="${PRESERVE_DATA:-true}"
[[ "$preserve_data" == true || "$preserve_data" == false ]] || die 'INVALID_DATA_POLICY: PRESERVE_DATA must be true or false.'
systemctl stop webdav.service
systemctl disable webdav.service
rm -f -- "$unit_file"
systemctl daemon-reload
rm -f -- "$install_dir/webdav"
rmdir -- "$install_dir" 2>/dev/null || true
rm -f -- "$config_file"
rmdir -- "$config_dir" 2>/dev/null || true
if [[ "$preserve_data" == false ]]; then
  rm -rf -- "$data_dir"
else
  printf '%s\n' "$data_dir" >"$state_dir/retained-data"
fi
remove_added_acl
remove_added_acl "$rollback_dir/added-acl"
if [[ "$preserve_data" == true ]]; then
  rm -rf -- "$rollback_dir" "$state_dir/config-backups"
  rm -f -- "$state_dir/installed" "$state_dir/version" "$state_dir/ownership" "$state_dir/install-dir"
else
  rm -rf -- "$state_dir"
fi
emit_progress 100 uninstall_completed 'WebDAV managed files removed'
