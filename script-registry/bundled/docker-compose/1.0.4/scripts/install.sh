#!/usr/bin/env bash
# shellcheck disable=SC1091,SC2154
set -Eeuo pipefail
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"

require_root
validate_inputs
check_host
check_engine

if [[ -f "${state_dir}/installed.json" ]] && compose_version >/dev/null 2>&1; then
  emit_progress 100 already_installed "Docker Compose is already healthy"
  exit 0
fi

install_host_dependencies
resolve_artifact
snapshot_existing
rollback_install() {
  local status="$?"
  trap - EXIT
  if [[ "${status}" -ne 0 && -d "${migration_dir}" ]]; then
    cleanup_artifact
    if ! restore_previous; then
      printf 'ERROR: Docker Compose installation rollback failed; migration state was preserved.\n' >&2
      status=1
    fi
  fi
  exit "${status}"
}
trap rollback_install EXIT
emit_progress 65 install.plugin "Installing Docker Compose CLI plugin"
install_versioned_plugin
cleanup_artifact
runtime_version="$(compose_version)" || die "Docker Compose command verification failed."
[[ "${runtime_version}" == "5.5.1" ]] || die "Unexpected Docker Compose version: ${runtime_version}"
write_state
commit_migration
trap - EXIT
emit_progress 100 install.completed "Docker Compose ${runtime_version} installed"
