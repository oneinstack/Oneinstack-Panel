#!/usr/bin/env bash
set -Eeuo pipefail
# shellcheck source=common.sh
source "$(dirname -- "${BASH_SOURCE[0]}")/common.sh"
require_root
[[ -f "$rollback_dir/had-managed" ]] || exit 0
systemctl stop webdav.service >/dev/null 2>&1 || true
systemctl disable webdav.service >/dev/null 2>&1 || true
if [[ "$(cat "$rollback_dir/had-managed")" == true ]]; then
  if [[ -d "$rollback_dir/install" ]]; then
    install -d -m 0755 -- "$install_dir"
    cp -a -- "$rollback_dir/install/." "$install_dir/"
  fi
  if [[ -f "$rollback_dir/config.yaml" ]]; then
    cp -a -- "$rollback_dir/config.yaml" "$config_file"
  else
    rm -f -- "$config_file"
    rmdir -- "$config_dir" 2>/dev/null || true
  fi
  [[ -f "$rollback_dir/webdav.service" ]] && cp -a -- "$rollback_dir/webdav.service" "$unit_file"
  [[ -f "$rollback_dir/legacy-config.yaml" ]] && cp -a -- "$rollback_dir/legacy-config.yaml" "$data_dir/config.yaml"
  systemctl daemon-reload
  if [[ "$(cat "$rollback_dir/was-enabled")" == true ]]; then
    systemctl enable webdav.service
  fi
  if [[ "$(cat "$rollback_dir/was-active")" == true ]]; then
    systemctl start webdav.service
  fi
else
  rm -f -- "$install_dir/webdav"
  rmdir -- "$install_dir" 2>/dev/null || true
  rm -f -- "$config_file" "$unit_file"
  rmdir -- "$config_dir" 2>/dev/null || true
  [[ "$(cat "$rollback_dir/created-data")" == true ]] && rmdir -- "$data_dir" 2>/dev/null || true
  rm -f -- "$state_dir/installed" "$state_dir/version" "$state_dir/ownership" "$state_dir/install-dir"
  [[ -f "$state_dir/retained-data" ]] || rm -f -- "$state_dir/data-dir"
  systemctl daemon-reload
fi
remove_added_acl "$rollback_dir/added-acl"
rm -rf -- "$rollback_dir"
printf 'WebDAV rollback completed\n'
