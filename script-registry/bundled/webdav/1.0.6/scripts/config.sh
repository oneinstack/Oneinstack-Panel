#!/usr/bin/env bash
set -Eeuo pipefail
# shellcheck source=common.sh
source "$(dirname -- "${BASH_SOURCE[0]}")/common.sh"
require_root
managed || die 'MANAGED_STATE_MISSING: WebDAV installation is not managed.'
read_config
revision() { sha256sum "$config_file" | awk '{print $1}'; }
operation="${ONEINSTACK_CONFIG_OPERATION:-get}"
if [[ "$operation" == get ]]; then
  printf 'component=webdav\nrevision=%s\napply_mode=restart\n' "$(revision)"
  printf 'webdavPort=%s\nbindAddress=%s\nprefix=%s\npermissions=%s\nbehindProxy=%s\ntls=%s\ntlsCert=%s\ntlsKey=%s\n' \
    "$port" "$bind_address" "$prefix" "$permissions" "$behind_proxy" "$tls" "$tls_cert" "$tls_key"
  printf 'connection.port=%s\nconnection.bindAddress=%s\nconnection.username=%s\nconnection.passwordConfigured=true\n' \
    "$port" "$bind_address" "$username"
  exit 0
fi
[[ "$operation" == apply ]] || die 'INVALID_CONFIG_OPERATION: expected get or apply.'
expected_revision="${ONEINSTACK_CONFIG_REVISION:-}"
[[ "$expected_revision" =~ ^[0-9a-f]{64}$ ]] || die 'INVALID_CONFIG_REVISION: expected SHA-256.'
[[ "$(revision)" == "$expected_revision" ]] || { printf 'CONFIG_CONFLICT: WebDAV configuration changed since preview.\n' >&2; exit 75; }
old_port="$port"
port="${ONEINSTACK_CONFIG_WEBDAV_PORT:-}"
bind_address="${ONEINSTACK_CONFIG_BIND_ADDRESS:-}"
prefix="${ONEINSTACK_CONFIG_PREFIX:-}"
permissions="${ONEINSTACK_CONFIG_PERMISSIONS:-}"
behind_proxy="${ONEINSTACK_CONFIG_BEHIND_PROXY:-}"
tls="${ONEINSTACK_CONFIG_TLS:-}"
tls_cert="${ONEINSTACK_CONFIG_TLS_CERT:-}"
tls_key="${ONEINSTACK_CONFIG_TLS_KEY:-}"
validate_config_values
if [[ "$port" != "$old_port" ]] && port_listening "$port"; then die "PORT_CONFLICT: WebDAV port $port is occupied."; fi
if [[ "$tls" == true ]]; then
  if ! runuser -u webdav -- test -r "$tls_cert" ||
     ! runuser -u webdav -- test -r "$tls_key"; then
    die 'TLS_PERMISSION_DENIED: WebDAV service user cannot read certificate or key.'
  fi
fi
backup_root="$state_dir/config-backups"
install -d -m 0700 -- "$backup_root"
backup="$(mktemp -d "$backup_root/config.XXXXXX")"
cp -a -- "$config_file" "$backup/config.yaml"
candidate="$(mktemp "$config_dir/.config.XXXXXX")"
write_config "$candidate"
was_active=false
service_active && was_active=true
committed=false
restore_on_failure() {
  local code="$1"
  local recovered=true
  trap - ERR INT TERM
  if [[ "$committed" == true ]]; then
    if ! cp -a -- "$backup/config.yaml" "$config_file"; then
      printf 'CONFIG_ROLLBACK_FAILED: could not restore the previous configuration.\n' >&2
      recovered=false
    fi
    if [[ "$was_active" == true ]]; then
      if ! systemctl restart webdav.service; then
        printf 'CONFIG_ROLLBACK_FAILED: could not restart the previous service.\n' >&2
        recovered=false
      fi
    elif ! systemctl stop webdav.service; then
      printf 'CONFIG_ROLLBACK_FAILED: could not restore the stopped service state.\n' >&2
      recovered=false
    fi
  fi
  rm -f -- "$candidate"
  [[ "$recovered" == true ]] || exit 1
  exit "$code"
}
trap 'restore_on_failure $?' ERR
trap 'restore_on_failure 130' INT
trap 'restore_on_failure 143' TERM
if ! mv -- "$candidate" "$config_file"; then restore_on_failure 1; fi
committed=true
if ! systemctl restart webdav.service; then restore_on_failure 1; fi
if ! (verify_runtime); then restore_on_failure 1; fi
if [[ "$was_active" != true ]]; then
  if ! systemctl stop webdav.service || service_active; then restore_on_failure 1; fi
fi
trap - ERR INT TERM
emit_progress 100 config_applied 'WebDAV configuration applied and probed'
