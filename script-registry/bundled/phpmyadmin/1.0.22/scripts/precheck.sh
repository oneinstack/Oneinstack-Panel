#!/usr/bin/env bash
set -Eeuo pipefail
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"
require_root
validate_inputs
validate_database_password
check_host
check_component_prerequisites
for command_name in curl tar gzip sha256sum realpath sed find; do require_command "${command_name}"; done
if [[ "${install_mode}" == offline ]] &&
  { ! command -v getfacl >/dev/null 2>&1 || ! command -v setfacl >/dev/null 2>&1; }; then
  phpmyadmin_die_code OFFLINE_BUNDLE_DEPENDENCY_MISSING "Offline phpMyAdmin requires ACL utilities for managed Web Server paths."
fi
available_kib="$(df -Pk /usr/local | awk 'NR==2 {print $4}')"
[[ "${available_kib}" =~ ^[0-9]+$ && "${available_kib}" -ge 4194304 ]] ||
  die "At least 4 GiB of free space below /usr/local is required."
emit_progress 10 precheck_completed "${component_name} environment precheck completed"
