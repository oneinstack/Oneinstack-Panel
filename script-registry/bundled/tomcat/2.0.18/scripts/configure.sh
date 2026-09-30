#!/usr/bin/env bash
set -Eeuo pipefail
script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
source "${script_dir}/common.sh"

require_root
load_persisted_parameters
validate_host
validate_scalar_inputs
[[ -f "${data_dir}/conf/server.xml" && -f "${data_dir}/bin/setenv.sh" ]] || die "managed Tomcat configuration is unavailable"
export ONEINSTACK_CONFIG_OPERATION=apply
export ONEINSTACK_CONFIG_REVISION="$(config_revision)"
exec "${script_dir}/config.sh"
