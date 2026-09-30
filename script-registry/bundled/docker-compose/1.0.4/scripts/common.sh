#!/usr/bin/env bash
set -Eeuo pipefail
umask 027

component_id="docker-compose"
software_version="${SOFTWARE_VERSION:-5.5.1}"
component_version="1.0.4"
state_root="${ONEINSTACK_COMPONENT_STATE:-/var/lib/oneinstack/components}"
state_dir="${state_root}/${component_id}"
migration_dir="${state_dir}/migration"
component_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
managed_root="/usr/local/lib/oneinstack/docker-compose"
version_root="${managed_root}/${software_version}"
plugin_dir="/usr/local/lib/docker/cli-plugins"
plugin_path="${plugin_dir}/docker-compose"

host_arch=""
system_id=""
system_version=""
package_manager=""
install_mode="${ONEINSTACK_INSTALL_MODE:-center}"
offline_package_path="${ONEINSTACK_OFFLINE_PACKAGE_PATH:-}"
artifact_name="docker-compose-linux-x86_64"
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
  [[ "${software_version}" == "5.5.1" ]] || die "Unsupported Docker Compose software version: ${software_version}"
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
    x86_64) host_arch="amd64"; artifact_name="docker-compose-linux-x86_64" ;;
    aarch64|arm64) host_arch="arm64"; artifact_name="docker-compose-linux-aarch64" ;;
    *) die "Docker Compose supports amd64 and arm64 hosts only." ;;
  esac
  case "${host_arch}" in
    amd64)
      artifact_sha256="db1889184726840f75c4f9c001048430d4f25b3be3cb084d3ddd762bc0aed576"
      artifact_url="https://github.com/docker/compose/releases/download/v${software_version}/${artifact_name}"
      ;;
    arm64)
      artifact_sha256="732e3a84c1a0f67256ce80bc2598a24546b10ca05f9faa97efceb1171ece2ef7"
      artifact_url="https://github.com/docker/compose/releases/download/v${software_version}/${artifact_name}"
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

docker_version() { docker version --format '{{.Server.Version}}' 2>/dev/null; }
compose_version() { docker compose version --short 2>/dev/null; }

check_engine() {
  require_command docker
  local engine_version
  engine_version="$(docker_version)" || die "Docker Engine is not responding; install Docker Engine before Docker Compose."
  [[ "${engine_version}" == "29.8.0" ]] || die "Docker Engine 29.8.0 is required; detected ${engine_version}."
}

offline_package_dir() {
  [[ -n "${offline_package_path}" ]] || die "Offline installation requires ONEINSTACK_OFFLINE_PACKAGE_PATH."
  printf '%s/packages/%s/%s/%s\n' "${offline_package_path}" "${system_id}" "${system_version}" "${host_arch}"
}

install_host_dependencies_online() {
  case "${package_manager}" in
    apt)
      apt-get update -qq
      apt-get install -y --no-install-recommends ca-certificates curl
      ;;
    dnf) dnf makecache --refresh -y >/dev/null; dnf install -y ca-certificates curl ;;
    yum) yum makecache -y >/dev/null; yum install -y ca-certificates curl ;;
    zypper)
      zypper --non-interactive refresh
      zypper --non-interactive install --no-recommends ca-certificates curl
      ;;
  esac
}

install_host_dependencies_offline() {
  local package_dir packages=()
  package_dir="$(offline_package_dir)"
  [[ -d "${package_dir}" ]] || die "Offline dependency directory is missing: ${package_dir}"
  mapfile -t packages < <(find "${package_dir}" -maxdepth 1 -type f \( -name '*.deb' -o -name '*.rpm' \) -print | sort)
  ((${#packages[@]} == 0)) && return 0
  emit_progress 25 dependency.offline.installing "Installing Compose prerequisites from the offline bundle"
  case "${package_manager}" in
    apt) dpkg -i "${packages[@]}" || apt-get -y --no-download -f install ;;
    dnf|yum) "${package_manager}" --disablerepo='*' --cacheonly install -y "${packages[@]}" ;;
    zypper) zypper --non-interactive --no-refresh --no-gpg-checks install --allow-unsigned-rpm "${packages[@]}" ;;
  esac
}

install_host_dependencies() {
  if [[ "${install_mode}" == "offline" ]]; then install_host_dependencies_offline; else install_host_dependencies_online; fi
  require_command curl
  require_command sha256sum
}

resolve_artifact() {
  local local_artifact temp_artifact actual curl_status
  if [[ "${install_mode}" == "offline" ]]; then
    local_artifact="${offline_package_path}/artifacts/${host_arch}/${artifact_name}"
    [[ -f "${local_artifact}" ]] || die "Offline Docker Compose artifact is missing: ${local_artifact}"
    artifact_path="${local_artifact}"
  else
    local_artifact="${component_root}/artifacts/${host_arch}/${artifact_name}"
    if [[ -f "${local_artifact}" ]]; then
      artifact_path="${local_artifact}"
    else
      temp_artifact="$(mktemp --tmpdir "oneinstack-compose.XXXXXX")"
      emit_progress 40 artifact.downloading "Downloading the pinned Docker Compose ${software_version} artifact"
      if curl --silent --show-error --proto '=https' --tlsv1.2 --fail --location --retry 3 \
        --connect-timeout 20 -o "${temp_artifact}" "${artifact_url}"; then
        :
      else
        curl_status=$?
        die "Docker Compose artifact download failed (curl exit ${curl_status}): ${artifact_url}; check DNS/HTTPS access or use offline mode."
      fi
      artifact_path="${temp_artifact}"
      artifact_temp_path="${temp_artifact}"
    fi
  fi
  actual="$(sha256sum -- "${artifact_path}" | awk '{print $1}')"
  [[ "${actual}" == "${artifact_sha256}" ]] ||
    die "Docker Compose artifact SHA-256 mismatch: expected ${artifact_sha256}, got ${actual}"
  emit_progress 50 artifact.verified "Pinned Docker Compose artifact SHA-256 verified"
}

cleanup_artifact() {
  [[ -z "${artifact_temp_path}" ]] || rm -f -- "${artifact_temp_path}"
  artifact_temp_path=""
}

snapshot_path() {
  local source="$1"
  install -d -m 0700 -- "${migration_dir}"
  if [[ -e "${source}" || -L "${source}" ]]; then cp -a -- "${source}" "${migration_dir}/previous-plugin"; fi
}

snapshot_existing() {
  rm -rf -- "${migration_dir}"
  install -d -m 0700 -- "${migration_dir}"
  snapshot_path "${plugin_path}"
  [[ -f "${state_dir}/installed.json" ]] && cp -a -- "${state_dir}/installed.json" "${migration_dir}/previous-state.json"
  emit_progress 20 migration.snapshot.created "Docker Compose plugin and state snapshot created"
}

restore_previous_from_root() {
  local root="$1"
  rm -f -- "${plugin_path}"
  if [[ -f "${root}/previous-plugin" || -L "${root}/previous-plugin" ]]; then
    install -d -m 0755 -- "${plugin_dir}"
    cp -a -- "${root}/previous-plugin" "${plugin_path}"
  fi
  if [[ -f "${root}/previous-state.json" ]]; then
    install -d -m 0750 -- "${state_dir}"
    cp -a -- "${root}/previous-state.json" "${state_dir}/installed.json"
  fi
  rm -rf -- "${version_root}"
}

restore_previous() {
  restore_previous_from_root "${migration_dir}"
  emit_progress 100 rollback.completed "Previous Docker Compose plugin and state restored"
}

install_versioned_plugin() {
  rm -rf -- "${version_root}"
  install -d -m 0755 -- "${version_root}" "${plugin_dir}"
  install -m 0755 -- "${artifact_path}" "${version_root}/docker-compose"
  ln -sfn -- "${version_root}/docker-compose" "${plugin_path}"
}

write_state() {
  local runtime_version
  runtime_version="$(compose_version)" || die "Docker Compose runtime version is unavailable."
  [[ "${runtime_version}" == "${software_version}" ]] || die "Unexpected Docker Compose runtime version: ${runtime_version}"
  install -d -m 0750 -- "${state_dir}"
  printf '{"component":"%s","componentVersion":"%s","softwareVersion":"%s","runtimeVersion":"%s","pluginPath":"%s","ownership":"managed","installMode":"%s","artifactSHA256":"%s"}\n' \
    "${component_id}" "${component_version}" "${software_version}" "${runtime_version}" \
    "${plugin_path}" "${install_mode}" "${artifact_sha256}" >"${state_dir}/installed.json"
  chmod 0640 "${state_dir}/installed.json"
}

commit_migration() {
  install -d -m 0700 -- "${state_dir}"
  rm -rf -- "${state_dir}/previous"
  if [[ -d "${migration_dir}" ]]; then
    mv -- "${migration_dir}" "${state_dir}/previous"
  fi
}
remove_state() {
  rm -f -- "${state_dir}/installed.json"
  rm -rf -- "${state_dir}/migration"
  rmdir "${state_dir}" 2>/dev/null || true
}
