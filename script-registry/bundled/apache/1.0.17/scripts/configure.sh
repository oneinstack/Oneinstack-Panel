#!/usr/bin/env bash
set -Eeuo pipefail
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"

require_root
validate_inputs
check_host
apache_require_owned_installation
check_component_prerequisites
[[ -x "${install_dir}/bin/httpd" && -f "${config_file}" ]] || die "Managed Apache installation is missing."
APACHE_ALLOW_OWN_PORT=true apache_check_web_server_conflicts

max_request_workers="${ONEINSTACK_CONFIG_MAX_REQUEST_WORKERS:-$(apache_config_value MaxRequestWorkers 256)}"
keepalive_timeout="${ONEINSTACK_CONFIG_KEEPALIVE_TIMEOUT:-$(apache_config_value KeepAliveTimeout 5)}"
install -d -m 0750 -- "${config_backup_root}"
backup_dir="$(mktemp -d "${config_backup_root}/configure-$(date -u +%Y%m%dT%H%M%SZ)-XXXXXX")"
cp -a -- "${config_file}" "${backup_dir}/httpd.conf"
cp -a -- "${managed_config_file}" "${backup_dir}/oneinstack.conf"
configure_failure() {
  local status=$? restore_main restore_managed
  trap - ERR INT TERM
  set +e
  restore_main="$(mktemp "${config_file}.restore.XXXXXX")"
  restore_managed="$(mktemp "${managed_config_file}.restore.XXXXXX")"
  cp -p -- "${backup_dir}/httpd.conf" "${restore_main}"
  cp -p -- "${backup_dir}/oneinstack.conf" "${restore_managed}"
  mv -f -- "${restore_main}" "${config_file}"
  mv -f -- "${restore_managed}" "${managed_config_file}"
  if systemctl restart "${service_name}.service" 2>/dev/null; then
    (apache_wait_service) >/dev/null 2>&1 || true
    (apache_verify_runtime) >/dev/null 2>&1 || true
  fi
  exit "${status}"
}
trap 'configure_failure' ERR
trap 'configure_failure' INT TERM
apache_prepare_runtime
systemctl enable --now "${service_name}.service"
apache_wait_service
apache_verify_runtime
apache_persist_install_parameters
apache_write_state
trap - ERR INT TERM
emit_progress 100 configure_completed "${component_name} runtime configuration completed"
