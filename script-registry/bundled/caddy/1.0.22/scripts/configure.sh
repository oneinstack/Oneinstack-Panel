#!/usr/bin/env bash
set -Eeuo pipefail
script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=common.sh
source "${script_dir}/common.sh"

require_root
load_persisted_parameters
validate_inputs
[[ -x "${binary}" && -r "${caddyfile}" && -r "${managed_config}" ]] || die_code CADDY_NOT_INSTALLED "Caddy managed configuration is unavailable."
export ONEINSTACK_CONFIG_OPERATION=apply
configuration_revision="$(config_revision)"
export ONEINSTACK_CONFIG_REVISION="${configuration_revision}"
export ONEINSTACK_CONFIG_PORT="${CADDY_PORT:-${caddy_port}}"
export ONEINSTACK_CONFIG_PHP_FPM_SOCKET="${PHP_FPM_SOCKET:-${php_fpm_socket}}"
export ONEINSTACK_CONFIG_WEB_ROOT="${WEB_ROOT:-${web_root}}"
export ONEINSTACK_CONFIG_LOG_DIR="${LOG_DIR:-${log_dir}}"
exec "${script_dir}/config.sh"
