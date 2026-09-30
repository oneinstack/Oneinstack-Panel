#!/usr/bin/env bash
set -Eeuo pipefail
# shellcheck source=common.sh
source "$(dirname -- "${BASH_SOURCE[0]}")/common.sh"
require_root
validate_install
detect_host
ensure_unowned_safe
validate_bundle
install_dependencies
archive="$(release_archive)"
verify_sha "$archive" "$(source_sha)"
ensure_requested_port_available

install -d -m 0700 -- "$state_dir"
if managed && [[ -f "$rollback_dir/added-acl" ]]; then
  cat "$rollback_dir/added-acl" >>"$state_dir/added-acl"
fi
rm -rf -- "$rollback_dir"
install -d -m 0700 -- "$rollback_dir"
had_managed=false; was_active=false; was_enabled=false; created_data=false
if managed; then
  had_managed=true
  service_active && was_active=true
  systemctl is-enabled --quiet webdav.service 2>/dev/null && was_enabled=true
  cp -a -- "$install_dir" "$rollback_dir/install"
  [[ -f "$config_file" ]] && cp -a -- "$config_file" "$rollback_dir/config.yaml"
  [[ -f "$unit_file" ]] && cp -a -- "$unit_file" "$rollback_dir/webdav.service"
  [[ -f "$data_dir/config.yaml" ]] && cp -a -- "$data_dir/config.yaml" "$rollback_dir/legacy-config.yaml"
else
  [[ -d "$data_dir" ]] || created_data=true
fi
printf '%s\n' "$had_managed" >"$rollback_dir/had-managed"
printf '%s\n' "$was_active" >"$rollback_dir/was-active"
printf '%s\n' "$was_enabled" >"$rollback_dir/was-enabled"
printf '%s\n' "$created_data" >"$rollback_dir/created-data"

rollback_on_error() {
  local code="$1"
  trap - ERR INT TERM
  "$(dirname -- "${BASH_SOURCE[0]}")/rollback.sh" || true
  exit "$code"
}
trap 'rollback_on_error $?' ERR
trap 'rollback_on_error 130' INT
trap 'rollback_on_error 143' TERM

if id webdav >/dev/null 2>&1; then
  existing_home="$(getent passwd webdav | cut -d: -f6)"
  [[ "$existing_home" == "$data_dir" ]] ||
    die 'RUN_USER_CONFLICT: existing webdav account belongs to another installation.'
else
  useradd --system --home-dir "$data_dir" --shell /usr/sbin/nologin webdav
fi
[[ -d "$data_dir" ]] || install -d -m 0750 -- "$data_dir"
chown webdav:webdav "$data_dir"
ensure_traversal_acl "$data_dir" "$rollback_dir/added-acl"
install -d -m 0755 -- "$install_dir"
install -d -m 0750 -o root -g webdav -- "$config_dir"
ensure_traversal_acl "$config_dir" "$rollback_dir/added-acl" read
hash_password
temp_dir="$(mktemp -d "$rollback_dir/release.XXXXXX")"
if ! tar -xOzf "$archive" webdav >"$temp_dir/webdav" 2>/dev/null; then
  tar -xOzf "$archive" ./webdav >"$temp_dir/webdav" 2>/dev/null ||
    die 'SOURCE_ARCHIVE_INVALID: WebDAV binary is missing.'
fi
[[ -s "$temp_dir/webdav" ]] || die 'SOURCE_ARCHIVE_INVALID: binary is empty.'
install -m 0755 -- "$temp_dir/webdav" "$install_dir/webdav"
[[ "$("$install_dir/webdav" version 2>/dev/null | grep -Eo '5\.16\.0' | head -n1)" == 5.16.0 ]] ||
  die 'RUNTIME_VERSION_MISMATCH: installed binary is not WebDAV 5.16.0.'
write_config "$config_file"
runuser -u webdav -- test -r "$config_file" ||
  die 'CONFIG_PERMISSION_DENIED: WebDAV service user cannot read its configuration.'
if [[ -f "$data_dir/config.yaml" ]]; then
  [[ "$had_managed" == true ]] || die 'EXTERNAL_DATA_CONFLICT: config.yaml is not managed.'
  rm -f -- "$data_dir/config.yaml"
fi
[[ "$was_active" == true ]] && systemctl stop webdav.service
write_unit
systemctl enable webdav.service
systemctl start webdav.service
verify_runtime
printf '%s\n' "$software_version" >"$state_dir/version"
printf '%s\n' "$install_dir" >"$state_dir/install-dir"
printf '%s\n' "$data_dir" >"$state_dir/data-dir"
printf 'managed\n' >"$state_dir/ownership"
: >"$state_dir/installed"
rm -f -- "$state_dir/retained-data"
trap - ERR INT TERM
emit_progress 100 install_completed 'WebDAV 5.16.0 installed and authenticated WebDAV probe passed'
