#!/usr/bin/env bash
# shellcheck disable=SC1091,SC2154
set -Eeuo pipefail
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"
require_root
validate_inputs
check_host
require_command systemctl
check_kernel_and_cgroups

if [[ -f "${state_dir}/installed.json" ]] && command -v docker >/dev/null 2>&1 && docker_version >/dev/null 2>&1; then
  emit_progress 100 already_installed "Docker Engine is already healthy"
  exit 0
fi

install_host_dependencies
check_systemd_and_networking
resolve_artifact
snapshot_existing
rollback_install() {
  local status="$?"
  trap - EXIT
  if [[ "${status}" -ne 0 && -d "${migration_dir}" ]]; then
    cleanup_artifact
    rm -rf -- "${version_root}"
    if ! restore_existing; then
      printf 'ERROR: Docker Engine installation rollback failed; migration state was preserved.\n' >&2
      status=1
    fi
  fi
  exit "${status}"
}
trap rollback_install EXIT
emit_progress 55 install_docker "Installing versioned Docker Engine binaries"
install_versioned_binaries
cleanup_artifact
getent group docker >/dev/null || groupadd --system docker
write_unit_files
systemctl daemon-reload
systemctl enable containerd.service docker.socket docker.service
ensure_service_started containerd.service "containerd service"
ensure_service_started docker.socket "Docker socket"
ensure_service_started docker.service "Docker service"
emit_progress 85 verify_installation "Verifying Docker daemon"
version_pair="$(docker_version_pair)" || die "Docker daemon is not responding after installation."
[[ "${version_pair}" == "29.8.0|29.8.0" ]] || die "Unexpected Docker client/server versions: ${version_pair}"
normalize_runtime_permissions
write_state
commit_migration
trap - EXIT
emit_progress 100 install_completed "Docker Engine ${version_pair} installed"
