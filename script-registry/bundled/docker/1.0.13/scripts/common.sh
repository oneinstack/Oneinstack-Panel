#!/usr/bin/env bash
set -Eeuo pipefail
umask 027

component_id="docker"
software_version="${SOFTWARE_VERSION:-29.8.0}"
component_version="1.0.13"
service_name="docker"
state_root="${ONEINSTACK_COMPONENT_STATE:-/var/lib/oneinstack/components}"
state_dir="${state_root}/${component_id}"
migration_dir="${state_dir}/migration"
component_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
docker_config_dir="/etc/docker"
managed_root="/usr/local/lib/oneinstack/docker"
version_root="${managed_root}/${software_version}"
version_bin_dir="${version_root}/bin"
docker_socket="/run/docker.sock"
containerd_socket="/run/containerd/containerd.sock"

host_arch=""
system_id=""
system_version=""
package_manager=""
install_mode="${ONEINSTACK_INSTALL_MODE:-center}"
offline_package_path="${ONEINSTACK_OFFLINE_PACKAGE_PATH:-}"
artifact_name="docker-29.8.0.tgz"
artifact_sha256=""
artifact_url=""
artifact_path=""
artifact_temp_path=""

die() { printf 'ERROR: %s\n' "$*" >&2; exit 1; }
emit_progress() {
  local percent="$1" code="$2" message="$3" fd="${ONEINSTACK_PROGRESS_FD:-}"
  [[ "${fd}" =~ ^[0-9]+$ ]] || return 0
  message="${message//\\/\\\\}"; message="${message//\"/\\\"}"; message="${message//$'\n'/ }"
  {
    printf '{"type":"progress","percent":%s,"code":"%s","message":"%s"}\n' \
      "${percent}" "${code}" "${message}" >&"${fd}"
  } 2>/dev/null || true
}
require_root() { [[ "$(id -u)" -eq 0 ]] || die "This action must run as root."; }
require_command() { command -v "$1" >/dev/null 2>&1 || die "Required command not found: $1"; }

validate_inputs() {
  [[ "${software_version}" == "29.8.0" ]] || die "Unsupported Docker Engine software version: ${software_version}"
  [[ "${install_mode}" == "center" || "${install_mode}" == "offline" ]] ||
    die "ONEINSTACK_INSTALL_MODE must be center or offline."
  [[ "${state_root}" == /* && "$(realpath -m -- "${state_root}")" == "${state_root}" ]] ||
    die "ONEINSTACK_COMPONENT_STATE must be a normalized absolute path."
  case "${state_root}" in
    /|/var|/var/lib|/var/lib/oneinstack) die "ONEINSTACK_COMPONENT_STATE is too broad: ${state_root}" ;;
  esac
}

map_host_arch() {
  case "$(uname -m)" in
    x86_64) host_arch="amd64" ;;
    aarch64|arm64) host_arch="arm64" ;;
    *) die "Docker Engine supports amd64 and arm64 hosts only." ;;
  esac
  case "${host_arch}" in
    amd64)
      artifact_sha256="cc21815cf1e2efed867dc9c8b96b46ffed8ea176ffab32b0aacb54726ded8f25"
      artifact_url="https://download.docker.com/linux/static/stable/x86_64/${artifact_name}"
      ;;
    arm64)
      artifact_sha256="1462a696be6029bd478d7d60d7f3c31cdd15affd1178a4a278aaf4a1d1b7f8b5"
      artifact_url="https://download.docker.com/linux/static/stable/aarch64/${artifact_name}"
      ;;
  esac
}

select_package_manager() {
  case "${system_id}" in
    ubuntu|debian) package_manager="apt" ;;
    centos)
      if [[ "${system_version%%.*}" == "7" ]]; then package_manager="yum"; else package_manager="dnf"; fi
      ;;
    rhel|rocky|almalinux|ol|fedora|amzn) package_manager="dnf" ;;
    sles|opensuse-leap|opensuse-tumbleweed) package_manager="zypper" ;;
    *) die "No supported package manager for ${system_id}." ;;
  esac
  require_command "${package_manager}"
}

check_host() {
  [[ -r /etc/os-release ]] || die "/etc/os-release is unavailable."
  # shellcheck disable=SC1091
  source /etc/os-release
  system_id="${ID:-}"
  system_version="${VERSION_ID:-}"
  case "${system_id}:${system_version}" in
    ubuntu:22.04|ubuntu:24.04|ubuntu:26.04|debian:11|debian:12|debian:13) ;;
    rhel:8*|rhel:9*|rhel:10*|rocky:8*|rocky:9*|rocky:10*|almalinux:8*|almalinux:9*|almalinux:10*|ol:8*|ol:9*|ol:10*) ;;
    centos:7*|centos:8*|centos:9*|centos:10*) ;;
    fedora:*) ;;
    amzn:2023) ;;
    sles:*|opensuse-leap:*|opensuse-tumbleweed:*) ;;
    *) die "Unsupported Linux release: ${system_id:-unknown} ${system_version:-unknown}" ;;
  esac
  map_host_arch
  select_package_manager
}

check_kernel_and_cgroups() {
  local kernel_major kernel_minor
  kernel_major="$(uname -r | cut -d. -f1)"
  kernel_minor="$(uname -r | cut -d. -f2)"
  [[ "${kernel_major}" =~ ^[0-9]+$ && "${kernel_minor}" =~ ^[0-9]+$ ]] || die "Cannot determine Linux kernel version."
  (( kernel_major > 3 || (kernel_major == 3 && kernel_minor >= 10) )) ||
    die "Linux kernel ${kernel_major}.${kernel_minor} is older than the required 3.10."
  [[ -d /sys/fs/cgroup ]] || die "The cgroup hierarchy is unavailable."
  [[ -e /sys/fs/cgroup/cgroup.controllers || -d /sys/fs/cgroup/systemd || -d /sys/fs/cgroup/cpu ]] ||
    die "The cgroup hierarchy is not mounted in a usable form."
}

check_systemd_base() {
  require_command systemctl
  [[ -d /run/systemd/system ]] || die "systemd is not the active init system."
}

check_systemd_and_networking() {
  check_systemd_base
  require_command iptables
  require_command ip6tables
  iptables --version >/dev/null 2>&1 || die "iptables is unavailable."
  ip6tables --version >/dev/null 2>&1 || die "ip6tables is unavailable."
  [[ ! -e "${docker_socket}" || -S "${docker_socket}" ]] ||
    die "${docker_socket} exists but is not a Unix socket."
  install -d -m 0710 -- /var/lib/docker
  [[ -w /var/lib/docker ]] || die "Docker data directory cannot be written safely."
  # SELinux/AppArmor state is observed only. This component never disables either MAC.
  if command -v getenforce >/dev/null 2>&1; then
    emit_progress 15 security.selinux.detected "SELinux policy is left unchanged"
  fi
  if command -v aa-status >/dev/null 2>&1; then
    emit_progress 15 security.apparmor.detected "AppArmor policy is left unchanged"
  fi
}

check_host_prerequisites() {
  check_kernel_and_cgroups
  check_systemd_base
}

docker_version() { docker version --format '{{.Server.Version}}' 2>/dev/null; }
docker_client_version() { docker version --format '{{.Client.Version}}' 2>/dev/null; }
docker_version_pair() { docker version --format '{{.Client.Version}}|{{.Server.Version}}' 2>/dev/null; }

required_host_commands() {
  printf '%s\n' curl tar gzip sha256sum systemctl iptables ip6tables
}

systemd_property() {
  local unit="$1" property="$2" value
  value="$(systemctl show "${unit}" --property="${property}" 2>/dev/null |
    sed -n "s/^${property}=//p")"
  printf '%s' "${value}"
}

stop_and_disable_unit() {
  local unit="$1"
  if ! systemctl stop "${unit}" 2>/dev/null; then
    if systemctl is-active --quiet "${unit}" 2>/dev/null; then
      return 1
    fi
  fi
  if ! systemctl disable "${unit}" 2>/dev/null; then
    if systemctl is-enabled --quiet "${unit}" 2>/dev/null; then
      return 1
    fi
  fi
  return 0
}

stop_unit() {
  local unit="$1"
  if systemctl stop "${unit}" 2>/dev/null; then
    return 0
  fi
  systemctl is-active --quiet "${unit}" 2>/dev/null && return 1
  return 0
}

ensure_service_started() {
  local unit="$1" label="$2" result exit_status
  if systemctl restart "${unit}" 2>/dev/null || systemctl start "${unit}" 2>/dev/null; then
    return 0
  fi
  result="$(systemd_property "${unit}" Result)"
  exit_status="$(systemd_property "${unit}" ExecMainStatus)"
  die "${label} failed to start (systemd result=${result:-unknown}, exit-status=${exit_status:-unknown}); inspect journalctl -u ${unit}."
}

install_host_dependencies_online() {
  case "${package_manager}" in
    apt)
      emit_progress 25 dependency.metadata.refreshing "Refreshing APT metadata for host prerequisites"
      apt-get update -qq
      apt-get install -y --no-install-recommends ca-certificates curl tar gzip iptables iproute2 procps
      ;;
    dnf)
      emit_progress 25 dependency.metadata.refreshing "Refreshing DNF metadata for host prerequisites"
      dnf makecache --refresh -y >/dev/null
      dnf install -y ca-certificates curl tar gzip xz iptables iproute procps-ng
      ;;
    yum)
      emit_progress 25 dependency.metadata.refreshing "Refreshing YUM metadata for host prerequisites"
      yum makecache -y >/dev/null
      yum install -y ca-certificates curl tar gzip xz iptables iproute procps-ng
      ;;
    zypper)
      emit_progress 25 dependency.metadata.refreshing "Refreshing Zypper metadata for host prerequisites"
      zypper --non-interactive refresh
      zypper --non-interactive install --no-recommends ca-certificates curl tar gzip xz iptables iproute2 procps
      ;;
  esac
}

offline_package_dir() {
  [[ -n "${offline_package_path}" ]] || die "Offline installation requires ONEINSTACK_OFFLINE_PACKAGE_PATH."
  printf '%s/packages/%s/%s/%s\n' "${offline_package_path}" "${system_id}" "${system_version}" "${host_arch}"
}

install_host_dependencies_offline() {
  local package_dir packages=() package
  package_dir="$(offline_package_dir)"
  [[ -d "${package_dir}" ]] || die "Offline dependency directory is missing: ${package_dir}"
  mapfile -t packages < <(find "${package_dir}" -maxdepth 1 -type f \( -name '*.deb' -o -name '*.rpm' \) -print | sort)
  if ((${#packages[@]} == 0)); then
    return 0
  fi
  emit_progress 25 dependency.offline.installing "Installing host prerequisites from the offline bundle"
  case "${package_manager}" in
    apt)
      dpkg -i "${packages[@]}" || apt-get -y --no-download -f install
      ;;
    dnf|yum)
      "${package_manager}" --disablerepo='*' --cacheonly install -y "${packages[@]}"
      ;;
    zypper)
      zypper --non-interactive --no-refresh --no-gpg-checks install --allow-unsigned-rpm "${packages[@]}"
      ;;
  esac
  for package in "${packages[@]}"; do [[ -f "${package}" ]] || die "Offline package disappeared: ${package}"; done
}

install_host_dependencies() {
  if [[ "${install_mode}" == "offline" ]]; then
    install_host_dependencies_offline
  else
    install_host_dependencies_online
  fi
  local command
  while IFS= read -r command; do
    require_command "${command}"
  done < <(required_host_commands)
}

artifact_checksum() { sha256sum -- "$1" | awk '{print $1}'; }

resolve_artifact() {
  local local_artifact temp_artifact actual
  if [[ "${install_mode}" == "offline" ]]; then
    local_artifact="${offline_package_path}/artifacts/${host_arch}/${artifact_name}"
    [[ -f "${local_artifact}" ]] || die "Offline Docker Engine artifact is missing: ${local_artifact}"
    artifact_path="${local_artifact}"
  else
    local_artifact="${component_root}/artifacts/${host_arch}/${artifact_name}"
    if [[ -f "${local_artifact}" ]]; then
      artifact_path="${local_artifact}"
    else
      temp_artifact="$(mktemp --tmpdir "oneinstack-docker.XXXXXX.tgz")"
      emit_progress 35 artifact.downloading "Downloading the pinned Docker Engine ${software_version} artifact"
      curl --proto '=https' --tlsv1.2 --fail --location --retry 3 --connect-timeout 20 \
        -o "${temp_artifact}" "${artifact_url}"
      artifact_path="${temp_artifact}"
      artifact_temp_path="${temp_artifact}"
    fi
  fi
  actual="$(artifact_checksum "${artifact_path}")"
  [[ "${actual}" == "${artifact_sha256}" ]] ||
    die "Docker Engine artifact SHA-256 mismatch: expected ${artifact_sha256}, got ${actual}"
  emit_progress 45 artifact.verified "Pinned Docker Engine artifact SHA-256 verified"
}

cleanup_artifact() {
  [[ -z "${artifact_temp_path}" ]] || rm -f -- "${artifact_temp_path}"
  artifact_temp_path=""
}

snapshot_path() {
  local root="$1" key="$2" source="$3"
  install -d -m 0700 -- "${root}/paths" "${root}/present"
  if [[ -e "${source}" || -L "${source}" ]]; then
    cp -a -- "${source}" "${root}/paths/${key}"
    : >"${root}/present/${key}"
  fi
}

snapshot_existing() {
  rm -rf -- "${migration_dir}"
  install -d -m 0700 -- "${migration_dir}"
  printf '%s\n' "${host_arch}" >"${migration_dir}/host-arch"
  printf '%s\n' "${system_id}" >"${migration_dir}/system-id"
  command -v docker >/dev/null 2>&1 && : >"${migration_dir}/external-docker-detected"
  systemctl is-active --quiet docker.service 2>/dev/null && : >"${migration_dir}/was-active"
  systemctl is-enabled --quiet docker.service 2>/dev/null && : >"${migration_dir}/was-enabled"
  systemctl is-active --quiet containerd.service 2>/dev/null && : >"${migration_dir}/containerd-was-active"
  systemctl is-enabled --quiet containerd.service 2>/dev/null && : >"${migration_dir}/containerd-was-enabled"
  snapshot_path "${migration_dir}" docker /usr/local/bin/docker
  snapshot_path "${migration_dir}" dockerd /usr/local/bin/dockerd
  snapshot_path "${migration_dir}" containerd /usr/local/bin/containerd
  snapshot_path "${migration_dir}" containerd-shim-runc-v2 /usr/local/bin/containerd-shim-runc-v2
  snapshot_path "${migration_dir}" ctr /usr/local/bin/ctr
  snapshot_path "${migration_dir}" runc /usr/local/bin/runc
  snapshot_path "${migration_dir}" docker-init /usr/local/bin/docker-init
  snapshot_path "${migration_dir}" docker-proxy /usr/local/bin/docker-proxy
  snapshot_path "${migration_dir}" containerd-service /etc/systemd/system/containerd.service
  snapshot_path "${migration_dir}" docker-service /etc/systemd/system/docker.service
  snapshot_path "${migration_dir}" docker-socket /etc/systemd/system/docker.socket
  [[ -d "${docker_config_dir}" ]] && cp -a -- "${docker_config_dir}" "${migration_dir}/config"
  local data_root="/var/lib/docker"
  if command -v docker >/dev/null 2>&1; then
    if ! data_root="$(docker info --format '{{.DockerRootDir}}' 2>/dev/null)"; then
      data_root="/var/lib/docker"
    fi
  fi
  [[ "${data_root}" == /* && "$(realpath -m -- "${data_root}")" == "${data_root}" ]] ||
    die "Docker data root cannot be identified safely."
  case "${data_root}" in /|/var|/var/lib|/data) die "Docker data root is too broad: ${data_root}" ;; esac
  printf '%s\n' "${data_root}" >"${migration_dir}/data-root"
  [[ -d "${data_root}" ]] && stat -c '%u %g %a' "${data_root}" >"${migration_dir}/data-root-permissions"
  systemctl stop docker.socket docker.service containerd.service 2>/dev/null || true
  emit_progress 20 migration.snapshot.created "Docker binary, service, and data-root snapshot created"
}

remove_managed_links() {
  local binary target
  for binary in docker dockerd containerd containerd-shim-runc-v2 ctr runc docker-init docker-proxy; do
    target="/usr/local/bin/${binary}"
    if [[ -L "${target}" && "$(readlink -- "${target}")" == "${managed_root}"/* ]]; then
      rm -f -- "${target}"
    fi
  done
}

restore_path() {
  local root="$1" key="$2" target="$3"
  rm -rf -- "${target}"
  if [[ -f "${root}/present/${key}" ]]; then
    install -d -m 0755 -- "$(dirname -- "${target}")"
    cp -a -- "${root}/paths/${key}" "${target}"
  fi
}

restore_snapshot() {
  local root="$1"
  stop_and_disable_unit docker.socket 2>/dev/null || true
  stop_and_disable_unit docker.service 2>/dev/null || true
  stop_and_disable_unit containerd.service 2>/dev/null || true
  remove_managed_links
  restore_path "${root}" docker /usr/local/bin/docker
  restore_path "${root}" dockerd /usr/local/bin/dockerd
  restore_path "${root}" containerd /usr/local/bin/containerd
  restore_path "${root}" containerd-shim-runc-v2 /usr/local/bin/containerd-shim-runc-v2
  restore_path "${root}" ctr /usr/local/bin/ctr
  restore_path "${root}" runc /usr/local/bin/runc
  restore_path "${root}" docker-init /usr/local/bin/docker-init
  restore_path "${root}" docker-proxy /usr/local/bin/docker-proxy
  restore_path "${root}" containerd-service /etc/systemd/system/containerd.service
  restore_path "${root}" docker-service /etc/systemd/system/docker.service
  restore_path "${root}" docker-socket /etc/systemd/system/docker.socket
  if [[ -d "${root}/config" ]]; then
    rm -rf -- "${docker_config_dir}"
    cp -a -- "${root}/config" "${docker_config_dir}"
  fi
  systemctl daemon-reload
  if [[ -f "${root}/containerd-was-enabled" ]]; then systemctl enable containerd.service 2>/dev/null || true; fi
  if [[ -f "${root}/was-enabled" ]]; then systemctl enable docker.service 2>/dev/null || true; fi
  if [[ -f "${root}/containerd-was-active" ]]; then systemctl start containerd.service 2>/dev/null || true; fi
  if [[ -f "${root}/was-active" ]]; then systemctl start docker.service 2>/dev/null || true; fi
}

write_unit_files() {
  install -d -m 0755 -- /etc/systemd/system
  cat >"/etc/systemd/system/containerd.service" <<UNIT
[Unit]
Description=containerd container runtime
Documentation=https://containerd.io
After=network.target local-fs.target
Before=docker.service

[Service]
Type=notify
ExecStart=${version_bin_dir}/containerd
Restart=always
RestartSec=2
LimitNOFILE=infinity
LimitNPROC=infinity
LimitCORE=infinity
TasksMax=infinity
Delegate=yes
KillMode=process

[Install]
WantedBy=multi-user.target
UNIT
  chmod 0644 /etc/systemd/system/containerd.service
  cat >"/etc/systemd/system/docker.socket" <<'UNIT'
[Unit]
Description=Docker Socket for the API

[Socket]
ListenStream=/run/docker.sock
SocketMode=0660
SocketUser=root
SocketGroup=docker

[Install]
WantedBy=sockets.target
UNIT
  cat >"/etc/systemd/system/docker.service" <<UNIT
[Unit]
Description=Docker Application Container Engine
Documentation=https://docs.docker.com
After=network-online.target docker.socket containerd.service
Wants=network-online.target
Requires=docker.socket containerd.service

[Service]
Type=notify
ExecStart=${version_bin_dir}/dockerd --host=fd:// --containerd=${containerd_socket} --group=docker
ExecReload=/bin/kill -s HUP \$MAINPID
TimeoutStartSec=0
Restart=on-failure
RestartSec=2
LimitNOFILE=infinity
LimitNPROC=infinity
LimitCORE=infinity
TasksMax=infinity
Delegate=yes
KillMode=process

[Install]
WantedBy=multi-user.target
UNIT
  chmod 0644 /etc/systemd/system/docker.service /etc/systemd/system/docker.socket
}

install_versioned_binaries() {
  rm -rf -- "${version_root}"
  install -d -m 0755 -- "${version_root}" "${version_bin_dir}"
  # Do not use grep -q here: with pipefail it can close the pipe early and
  # turn tar's SIGPIPE into a false negative for a valid archive.
  tar -tzf "${artifact_path}" | grep -E '^docker/(docker|dockerd)$' >/dev/null ||
    die "Docker Engine artifact does not contain the expected static binaries."
  tar -xzf "${artifact_path}" --strip-components=1 -C "${version_root}" docker
  for binary in docker dockerd containerd containerd-shim-runc-v2 ctr runc docker-init docker-proxy; do
    [[ -x "${version_root}/${binary}" ]] || continue
    mv -- "${version_root}/${binary}" "${version_bin_dir}/${binary}"
  done
  [[ -x "${version_bin_dir}/docker" && -x "${version_bin_dir}/dockerd" ]] ||
    die "Docker Engine artifact extraction is incomplete."
  rmdir "${version_root}" 2>/dev/null || true
  local binary
  for binary in docker dockerd containerd containerd-shim-runc-v2 ctr runc docker-init docker-proxy; do
    [[ -x "${version_bin_dir}/${binary}" ]] || continue
    ln -sfn -- "${version_bin_dir}/${binary}" "/usr/local/bin/${binary}"
  done
  hash -r 2>/dev/null || true
}

normalize_runtime_permissions() {
  getent group docker >/dev/null || groupadd --system docker
  [[ -S "${docker_socket}" ]] || die "Docker daemon socket was not created."
  chown root:docker "${docker_socket}"
  chmod 0660 "${docker_socket}"
  local data_root
  data_root="$(docker info --format '{{.DockerRootDir}}' 2>/dev/null || true)"
  [[ "${data_root}" == /* && "$(realpath -m -- "${data_root}")" == "${data_root}" ]] ||
    die "Docker data root cannot be identified safely for permission validation."
  case "${data_root}" in /|/var|/var/lib|/data) die "Docker data root is too broad: ${data_root}" ;; esac
  emit_progress 85 permissions.runtime.checked "Docker socket and data-root permissions verified without changing user data"
}

write_state() {
  local data_root="/var/lib/docker" runtime_pair
  runtime_pair="$(docker_version_pair)" || die "Docker runtime version is unavailable."
  [[ ! -r "${migration_dir}/data-root" ]] || read -r data_root <"${migration_dir}/data-root"
  install -d -m 0750 -- "${state_dir}"
  printf '{"component":"%s","componentVersion":"%s","softwareVersion":"%s","clientVersion":"%s","serverVersion":"%s","service":"%s","ownership":"managed","dataRoot":"%s","artifactSHA256":"%s","installMode":"%s"}\n' \
    "${component_id}" "${component_version}" "${software_version}" \
    "${runtime_pair%%|*}" "${runtime_pair##*|}" "${service_name}" "${data_root}" \
    "${artifact_sha256}" "${install_mode}" >"${state_dir}/installed.json"
  chmod 0640 "${state_dir}/installed.json"
}

commit_migration() {
  install -d -m 0700 -- "${state_dir}"
  rm -rf -- "${state_dir}/previous"
  if [[ -d "${migration_dir}" ]]; then
    mv -- "${migration_dir}" "${state_dir}/previous"
  fi
}

restore_existing() {
  restore_snapshot "${migration_dir}"
  if [[ -r "${migration_dir}/data-root" && -r "${migration_dir}/data-root-permissions" ]]; then
    local data_root owner_id group_id mode
    read -r data_root <"${migration_dir}/data-root"
    read -r owner_id group_id mode <"${migration_dir}/data-root-permissions"
    if [[ -d "${data_root}" && "${owner_id}" =~ ^[0-9]+$ && "${group_id}" =~ ^[0-9]+$ && "${mode}" =~ ^[0-7]{3,4}$ ]]; then
      chown "${owner_id}:${group_id}" "${data_root}"
      chmod "${mode}" "${data_root}"
    fi
  fi
  emit_progress 100 rollback.service.restored "Previous Docker service, binaries, configuration, and state restored"
}

remove_state() {
  rm -f -- "${state_dir}/installed.json"
  rm -rf -- "${state_dir}/migration" "${state_dir}/previous"
  rmdir "${state_dir}" 2>/dev/null || true
}
