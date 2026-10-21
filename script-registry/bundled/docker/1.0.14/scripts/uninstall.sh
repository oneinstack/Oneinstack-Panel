#!/usr/bin/env bash
# shellcheck disable=SC1091,SC2154
set -Eeuo pipefail
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"

require_root
validate_inputs
require_command systemctl
[[ -f "${state_dir}/installed.json" ]] ||
  die "Managed Docker installation state is missing; refusing to remove external Docker files."

docker_unit_load_state() {
  systemd_property "$1" LoadState
}

stop_docker_units() {
  local unit load_state
  for unit in docker.socket docker.service containerd.service; do
    load_state="$(docker_unit_load_state "${unit}")"
    [[ "${load_state}" == "not-found" ]] && continue
    stop_and_disable_unit "${unit}" || die "Failed to stop and disable ${unit}."
  done
}

verify_docker_units_stopped() {
  local unit
  for unit in docker.service docker.socket containerd.service; do
    systemctl is-active --quiet "${unit}" 2>/dev/null && die "Docker unit remains active: ${unit}"
    systemctl is-enabled --quiet "${unit}" 2>/dev/null && die "Docker unit remains enabled: ${unit}"
  done
  return 0
}

verify_managed_units_removed() {
  local unit fragment
  for unit in docker.service docker.socket containerd.service; do
    fragment="$(systemd_property "${unit}" FragmentPath)"
    [[ "${fragment}" == "/etc/systemd/system/${unit}" ]] &&
      die "Managed Docker unit remains loaded: ${unit}"
  done
  return 0
}

emit_progress 20 uninstall.service.stopping "Stopping managed Docker service"
stop_docker_units
verify_docker_units_stopped
emit_progress 55 uninstall.binary.removing "Removing OneinStack-managed Docker binaries and units"
remove_managed_links
rm -rf -- "${version_root}"
rm -f -- /etc/systemd/system/containerd.service /etc/systemd/system/docker.service /etc/systemd/system/docker.socket
systemctl daemon-reload
verify_managed_units_removed
if [[ -d "${state_dir}/previous" ]]; then
  restore_snapshot "${state_dir}/previous"
fi
# Do not remove Docker data, containerd data, images, volumes, containers, or daemon.json.
remove_state
emit_progress 100 uninstall_completed "Managed Docker binaries removed; Docker data and configuration preserved"
