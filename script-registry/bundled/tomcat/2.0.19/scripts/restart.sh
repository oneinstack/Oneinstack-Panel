#!/usr/bin/env bash
set -Eeuo pipefail
source "$(dirname "$0")/common.sh"
require_root
load_persisted_parameters
ensure_managed_service_ownership
ensure_default_data_root_access
normalize_selinux_contexts "${install_dir}" "${java_home}" "${data_dir}"
validate_java_runtime_user_access
write_managed_service
systemctl daemon-reload
systemctl restart "${service_name}.service"
validate_runtime
