#!/usr/bin/env bash
set -Eeuo pipefail
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"

require_root
validate_inputs
check_host
apache_require_owned_installation
[[ -x "${install_dir}/bin/httpd" && -f "${config_file}" ]] || die "Managed Apache installation is missing."
APACHE_ALLOW_OWN_PORT=true apache_check_web_server_conflicts
apache_write_service_unit
"${install_dir}/bin/httpd" -t -f "${config_file}"
systemctl reload "${service_name}.service"
apache_wait_service
apache_verify_runtime
emit_progress 100 service_action_completed "${component_name} configuration reloaded"
