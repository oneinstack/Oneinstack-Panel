#!/usr/bin/env bash
set -Eeuo pipefail
umask 027

component_id="nodejs"
component_name="Node.js"
package_version="1.0.8"
software_version="${SOFTWARE_VERSION:-22.12.0}"
install_dir="${INSTALL_DIR:-/usr/local/node}"
state_root="${ONEINSTACK_COMPONENT_STATE:-/var/lib/oneinstack/components}"
state_dir="${state_root}/${component_id}"
state_file="${state_dir}/installed.json"
install_mode="${ONEINSTACK_INSTALL_MODE:-center}"
offline_package_path="${ONEINSTACK_OFFLINE_PACKAGE_PATH:-}"
takeover_unmanaged_conflicts="${TAKEOVER_UNMANAGED_CONFLICTS:-false}"

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"
component_root="$(cd -- "${script_dir}/.." && pwd -P)"
package_root="${component_root}"
artifact_root="${package_root}/artifacts"
profile_file="/etc/profile.d/nodejs.sh"
link_dir="/usr/local/bin"
architecture=""
system_id=""
system_version=""
glibc_version=""
route=""
artifact=""
artifact_name=""
artifact_url=""
artifact_sha256=""
artifact_temp_path=""
transaction_dir=""
backup_dir=""
conflict_backup_dir=""
takeover_install_backup_dir=""
profile_backup=""
stage_dir=""
build_root=""
transaction_committed=false

die() { printf 'ERROR: %s\n' "$*" >&2; exit 1; }

emit_progress() {
  local percent="$1" code="$2" message="$3" fd="${ONEINSTACK_PROGRESS_FD:-}"
  [[ "${fd}" =~ ^[0-9]+$ ]] || return 0
  message="${message//\\/\\\\}"
  message="${message//\"/\\\"}"
  message="${message//$'\n'/ }"
  {
    printf '{"type":"progress","percent":%s,"code":"%s","message":"%s"}\n' \
      "${percent}" "${code}" "${message}" >&"${fd}"
  } 2>/dev/null || true
}

require_root() { [[ "$(id -u)" -eq 0 ]] || die "This action must run as root."; }
require_command() { command -v "$1" >/dev/null 2>&1 || die "Required command not found: $1"; }

version_ge() {
  local actual="$1" required="$2"
  [[ "$(printf '%s\n%s\n' "${actual}" "${required}" | sort -V | head -n1)" == "${required}" ]]
}

validate_managed_path() {
  local value="$1" label="$2"
  [[ -n "${value}" && "${value}" == /* && "$(realpath -m -- "${value}")" == "${value}" ]] ||
    die "${label} must be a normalized absolute path."
  case "${value}" in
    *'"'*|*\\*) die "${label} contains unsupported JSON path characters." ;;
  esac
  case "${value}" in
    /|/bin|/sbin|/usr|/usr/bin|/usr/sbin|/usr/local|/usr/local/bin|/etc|/var|/data|/home|/root)
      die "${label} is too broad: ${value}" ;;
  esac
}

option_for_version() {
  case "${software_version}" in
    22.12.0|16.20.2) ;;
    *) die "Unsupported ${component_name} software version: ${software_version}" ;;
  esac
}

validate_conflict_policy() {
  case "${takeover_unmanaged_conflicts}" in
    true|false) ;;
    *) die "TAKEOVER_UNMANAGED_CONFLICTS must be true or false." ;;
  esac
}

state_value() {
  local key="$1"
  [[ -f "${state_file}" ]] || return 0
  sed -n "s/.*\"${key}\":\"\([^\"]*\)\".*/\1/p" "${state_file}" | head -n1
}

load_managed_install_dir() {
  local stored
  stored="$(state_value installDir || true)"
  [[ -n "${stored}" ]] || return 0
  if [[ -n "${INSTALL_DIR+x}" && "${INSTALL_DIR}" != "${stored}" ]]; then
    die "INSTALL_DIR differs from the managed installation directory; uninstall the existing runtime before changing it."
  fi
  install_dir="${stored}"
}

validate_inputs() {
  option_for_version
  validate_conflict_policy
  validate_managed_path "${state_root}" ONEINSTACK_COMPONENT_STATE
  validate_managed_path "${install_dir}" INSTALL_DIR
  case "${install_mode}" in
    center) package_root="${component_root}" ;;
    offline)
      [[ -n "${offline_package_path}" && "${offline_package_path}" == /* &&
        "$(realpath -m -- "${offline_package_path}")" == "${offline_package_path}" ]] ||
        die "ONEINSTACK_OFFLINE_PACKAGE_PATH must be a normalized absolute path."
      [[ -d "${offline_package_path}" ]] || die "Offline component bundle does not exist."
      package_root="${offline_package_path}"
      [[ -f "${package_root}/manifest.yaml" ]] || die "Offline bundle is missing manifest.yaml."
      [[ -f "${package_root}/files.sha256" ]] || die "Offline bundle is missing files.sha256."
      grep -Eq '^[[:space:]]+id:[[:space:]]+nodejs[[:space:]]*$' "${package_root}/manifest.yaml" ||
        die "Offline bundle manifest does not describe nodejs."
      grep -Eq '^[[:space:]]+version:[[:space:]]+1\.0\.8[[:space:]]*$' "${package_root}/manifest.yaml" ||
        die "Offline bundle package version does not match."
      (cd -- "${package_root}" && sha256sum --check --strict files.sha256 >/dev/null) ||
        die "Offline bundle checksum verification failed."
      ;;
    *) die "ONEINSTACK_INSTALL_MODE must be center or offline." ;;
  esac
  artifact_root="${package_root}/artifacts"
}

detect_host() {
  local required_glibc
  [[ -r /etc/os-release ]] || die "/etc/os-release is unavailable."
  # shellcheck disable=SC1091
  source /etc/os-release
  system_id="${ID:-}"
  system_version="${VERSION_ID:-}"
  case "${system_id}:${system_version}" in
    ubuntu:22.04|ubuntu:24.04|ubuntu:26.04|debian:11|debian:12|debian:13|\
    rhel:8*|rhel:9*|rhel:10*|rocky:8*|rocky:9*|rocky:10*|\
    almalinux:8*|almalinux:9*|almalinux:10*|ol:8*|ol:9*|ol:10*|\
    centos:7*|centos:8*|centos:9*|centos:10*|fedora:*|amzn:2023|\
    sles:*|opensuse-leap:*|opensuse-tumbleweed:*|opensuse:*) ;;
    *) die "Unsupported Linux release: ${system_id:-unknown} ${system_version:-unknown}" ;;
  esac
  case "$(uname -m)" in
    x86_64) architecture="amd64" ;;
    aarch64|arm64) architecture="arm64" ;;
    *) die "Unsupported host architecture: $(uname -m)" ;;
  esac
  glibc_version="$(getconf GNU_LIBC_VERSION 2>/dev/null | awk '{print $2}' || true)"
  [[ "${glibc_version}" =~ ^[0-9]+\.[0-9]+$ ]] || die "GNU libc version could not be detected."
  if [[ "${software_version}" == "22.12.0" && "${system_id}:${system_version}" == centos:7* ]]; then
    route="source"
    [[ "${glibc_version}" == "2.17" ]] || die "CentOS 7 source route requires glibc 2.17."
  else
    route="binary"
    required_glibc="2.28"
    [[ "${software_version}" == "16.20.2" ]] && required_glibc="2.17"
    version_ge "${glibc_version}" "${required_glibc}" ||
      die "Node.js ${software_version} binary artifacts require glibc ${required_glibc} or newer."
  fi
}

package_manager() {
  case "${system_id}:${system_version}" in
    ubuntu:*|debian:*) printf '%s\n' apt ;;
    centos:7*) printf '%s\n' yum ;;
    rhel:*|rocky:*|almalinux:*|ol:*|centos:*|fedora:*|amzn:*)
      if command -v dnf >/dev/null 2>&1; then printf '%s\n' dnf; else printf '%s\n' yum; fi ;;
    sles:*|opensuse-leap:*|opensuse-tumbleweed:*|opensuse:*) printf '%s\n' zypper ;;
    *) die "No package manager mapping for ${system_id} ${system_version}." ;;
  esac
}

select_artifact() {
  case "${software_version}:${route}:${architecture}" in
    16.20.2:binary:amd64)
      artifact_name="node-v16.20.2-linux-x64.tar.xz"
      artifact_url="https://nodejs.org/dist/v16.20.2/node-v16.20.2-linux-x64.tar.xz"
      artifact_sha256="874463523f26ed528634580247f403d200ba17a31adf2de98a7b124c6eb33d87" ;;
    16.20.2:binary:arm64)
      artifact_name="node-v16.20.2-linux-arm64.tar.xz"
      artifact_url="https://nodejs.org/dist/v16.20.2/node-v16.20.2-linux-arm64.tar.xz"
      artifact_sha256="e88d86154d1ce53dc52fd74d79d4bfdf0b05f58c0bb2639adfa36e9378b770c4" ;;
    22.12.0:binary:amd64)
      artifact_name="node-v22.12.0-linux-x64.tar.xz"
      artifact_url="https://nodejs.org/dist/v22.12.0/node-v22.12.0-linux-x64.tar.xz"
      artifact_sha256="22982235e1b71fa8850f82edd09cdae7e3f32df1764a9ec298c72d25ef2c164f" ;;
    22.12.0:binary:arm64)
      artifact_name="node-v22.12.0-linux-arm64.tar.xz"
      artifact_url="https://nodejs.org/dist/v22.12.0/node-v22.12.0-linux-arm64.tar.xz"
      artifact_sha256="8cfd5a8b9afae5a2e0bd86b0148ca31d2589c0ea669c2d0b11c132e35d90ed68" ;;
    22.12.0:source:amd64|22.12.0:source:arm64)
      artifact_name="node-v22.12.0.tar.xz"
      artifact_url="https://nodejs.org/dist/v22.12.0/node-v22.12.0.tar.xz"
      artifact_sha256="fe1bc4be004dc12721ea2cb671b08a21de01c6976960ef8a1248798589679e16" ;;
    *) die "No Node.js artifact mapping for ${route} ${architecture}." ;;
  esac
}

offline_artifact_path() {
  printf '%s\n' "${artifact_root}/${architecture}/${artifact_name}"
}

verify_artifact() {
  [[ -f "${artifact}" ]] || die "Node.js artifact is missing: ${artifact}"
  printf '%s  %s\n' "${artifact_sha256}" "${artifact}" | sha256sum --check --status ||
    die "Node.js artifact checksum mismatch."
}

resolve_artifact() {
  if [[ "${install_mode}" == offline ]]; then
    artifact="$(offline_artifact_path)"
  else
    [[ -n "${transaction_dir}" && -d "${transaction_dir}" ]] || die "Node.js artifact download transaction is not ready."
    require_command curl
    artifact="${transaction_dir}/${artifact_name}"
    emit_progress 25 artifact_download "Downloading the pinned Node.js ${software_version} artifact"
    curl --proto '=https' --tlsv1.2 --fail --location --retry 3 --connect-timeout 20 \
      --output "${artifact}" "${artifact_url}"
    artifact_temp_path="${artifact}"
  fi
  verify_artifact
  emit_progress 30 artifact_verified "Pinned Node.js artifact SHA-256 verified"
}

check_entrypoint_conflicts() {
  local name link target
  for name in node npm npx corepack nodejs; do
    link="${link_dir}/${name}"
    [[ -e "${link}" || -L "${link}" ]] || continue
    is_managed_entrypoint "${name}" && continue
    target=""
    [[ -L "${link}" ]] && target="$(readlink -- "${link}")"
    [[ "${takeover_unmanaged_conflicts}" == true ]] ||
      die "Refusing to replace unmanaged ${link}${target:+ (target: ${target})}. Set TAKEOVER_UNMANAGED_CONFLICTS=true only after approving a backup."
  done
  if [[ -e "${profile_file}" || -L "${profile_file}" ]]; then
    if ! is_managed_profile; then
      [[ "${takeover_unmanaged_conflicts}" == true ]] ||
        die "Refusing to replace unmanaged ${profile_file}. Set TAKEOVER_UNMANAGED_CONFLICTS=true only after approving a backup."
    fi
  fi
}

is_managed_entrypoint() {
  local name="$1" link="${link_dir}/$1" target=""
  [[ -f "${state_file}" ]] || return 1
  [[ -L "${link}" ]] || return 1
  target="$(readlink -- "${link}")"
  [[ "${target}" == "${install_dir}/bin/${name}" ]]
}

is_managed_profile() {
  [[ -f "${state_file}" ]] && has_managed_profile
}

has_managed_profile() {
  [[ -f "${profile_file}" && ! -L "${profile_file}" ]] &&
    grep -Fq '# OneinStack managed Node.js PATH' "${profile_file}"
}

backup_conflict_path() {
  local source_path="$1" backup_name="$2" backup_path
  [[ -e "${source_path}" || -L "${source_path}" ]] || return 0
  [[ -n "${conflict_backup_dir}" ]] || die "Node.js conflict backup transaction is not ready."
  backup_path="${conflict_backup_dir}/${backup_name}"
  [[ ! -e "${backup_path}" && ! -L "${backup_path}" ]] ||
    die "Node.js conflict backup already exists: ${backup_path}"
  cp -a -- "${source_path}" "${backup_path}"
}

backup_unmanaged_conflicts() {
  local name link
  [[ "${takeover_unmanaged_conflicts}" == true ]] || return 0
  conflict_backup_dir="$(mktemp -d "${state_dir}/.conflict-backup.XXXXXX")"
  for name in node npm npx corepack nodejs; do
    link="${link_dir}/${name}"
    if [[ -e "${link}" || -L "${link}" ]] && ! is_managed_entrypoint "${name}"; then
      backup_conflict_path "${link}" "${name}"
    fi
  done
  if [[ -e "${profile_file}" || -L "${profile_file}" ]] && ! is_managed_profile; then
    backup_conflict_path "${profile_file}" nodejs.sh
  fi
  if [[ -z "$(find "${conflict_backup_dir}" -mindepth 1 -maxdepth 1 -print -quit)" ]]; then
    rmdir -- "${conflict_backup_dir}"
    conflict_backup_dir=""
  fi
}

backup_unmanaged_install_dir() {
  local parent backup_path
  [[ "${takeover_unmanaged_conflicts}" == true ]] ||
    die "Refusing to replace an unmanaged Node.js installation."
  [[ -e "${install_dir}" || -L "${install_dir}" ]] || return 0
  [[ ! -f "${state_file}" ]] || die "Managed Node.js state already exists for ${install_dir}."

  parent="$(dirname -- "${install_dir}")"
  takeover_install_backup_dir="$(mktemp -d "${parent}/.nodejs-conflict-backup.XXXXXX")"
  backup_path="${takeover_install_backup_dir}/runtime"
  if ! mv -- "${install_dir}" "${backup_path}"; then
    rmdir -- "${takeover_install_backup_dir}" 2>/dev/null || true
    takeover_install_backup_dir=""
    die "Failed to back up the unmanaged Node.js installation before takeover."
  fi
  printf 'Node.js unmanaged installation backup: %s\n' "${takeover_install_backup_dir}"
  emit_progress 18 takeover_backup_created "Unmanaged Node.js installation backed up before takeover"
}

load_persisted_conflict_backup() {
  local stored
  stored="$(state_value conflictBackupDir || true)"
  [[ -n "${stored}" ]] || return 0
  case "${stored}" in
    "${state_dir}"/.conflict-backup.*) ;;
    *) die "Managed Node.js conflict backup path is invalid." ;;
  esac
  [[ -d "${stored}" ]] || die "Managed Node.js conflict backup is missing: ${stored}"
  conflict_backup_dir="${stored}"
}

load_persisted_takeover_install_backup() {
  local stored parent stored_parent stored_name
  stored="$(state_value takeoverInstallBackupDir || true)"
  [[ -n "${stored}" ]] || return 0
  parent="$(dirname -- "${install_dir}")"
  stored_parent="$(dirname -- "${stored}")"
  stored_name="$(basename -- "${stored}")"
  [[ "${stored}" == /* && "$(realpath -m -- "${stored}")" == "${stored}" && "${stored_parent}" == "${parent}" ]] ||
    die "Managed Node.js takeover backup path is invalid."
  case "${stored_name}" in
    .nodejs-conflict-backup.*) ;;
    *) die "Managed Node.js takeover backup path is invalid." ;;
  esac
  [[ -d "${stored}" ]] || die "Managed Node.js takeover backup is missing: ${stored}"
  [[ -e "${stored}/runtime" || -L "${stored}/runtime" ]] ||
    die "Managed Node.js takeover backup does not contain the original installation."
  takeover_install_backup_dir="${stored}"
}

restore_takeover_install_backup() {
  local backup_path
  [[ -n "${takeover_install_backup_dir}" ]] || return 0
  backup_path="${takeover_install_backup_dir}/runtime"
  [[ -e "${backup_path}" || -L "${backup_path}" ]] || {
    printf 'ERROR: Node.js takeover backup is incomplete: %s\n' "${takeover_install_backup_dir}" >&2
    return 1
  }
  if [[ -e "${install_dir}" || -L "${install_dir}" ]]; then
    rm -rf -- "${install_dir}" || return 1
  fi
  mv -- "${backup_path}" "${install_dir}" || return 1
  rmdir -- "${takeover_install_backup_dir}" 2>/dev/null || true
  takeover_install_backup_dir=""
}

restore_conflict_backups() {
  local name link backup_path restore_failed=false
  [[ -n "${conflict_backup_dir}" && -d "${conflict_backup_dir}" ]] || return 0
  for name in node npm npx corepack nodejs; do
    link="${link_dir}/${name}"
    backup_path="${conflict_backup_dir}/${name}"
    [[ -e "${backup_path}" || -L "${backup_path}" ]] || continue
    if [[ -e "${link}" || -L "${link}" ]]; then
      is_managed_entrypoint "${name}" || {
        printf 'WARNING: preserving changed %s; skipped conflict backup restore.\n' "${link}" >&2
        restore_failed=true
        continue
      }
      rm -f -- "${link}" || restore_failed=true
    fi
    install -d -m 0755 -- "${link_dir}" || restore_failed=true
    cp -a -- "${backup_path}" "${link}" || restore_failed=true
  done
  backup_path="${conflict_backup_dir}/nodejs.sh"
  if [[ -e "${backup_path}" || -L "${backup_path}" ]]; then
    if [[ -e "${profile_file}" || -L "${profile_file}" ]]; then
      if is_managed_profile; then
        rm -f -- "${profile_file}" || restore_failed=true
      else
        printf 'WARNING: preserving changed %s; skipped conflict backup restore.\n' "${profile_file}" >&2
        restore_failed=true
      fi
    fi
    if [[ ! -e "${profile_file}" && ! -L "${profile_file}" ]]; then
      install -d -m 0755 -- "$(dirname -- "${profile_file}")" || restore_failed=true
      cp -a -- "${backup_path}" "${profile_file}" || restore_failed=true
    fi
  fi
  [[ "${restore_failed}" == false ]]
}

check_build_resources() {
  local memory_kib available_kib
  memory_kib="$(awk '/^MemTotal:/ {print $2; exit}' /proc/meminfo 2>/dev/null || true)"
  [[ "${memory_kib}" =~ ^[0-9]+$ && "${memory_kib}" -ge 8388608 ]] ||
    die "CentOS 7 source build requires at least 8 GiB of RAM."
  available_kib="$(df -Pk -- "$(dirname -- "${install_dir}")" | awk 'NR==2 {print $4}')"
  [[ "${available_kib}" =~ ^[0-9]+$ && "${available_kib}" -ge 8388604 ]] ||
    die "At least 8 GiB of free space is required for the Node.js source build."
}

offline_dependency_dir() {
  printf '%s\n' "${package_root}/packages/${system_id}/${system_version}/${architecture}"
}

install_local_dependencies() {
  local manager="$1" dependency_dir="$2"
  shopt -s nullglob
  local packages=("${dependency_dir}"/*.deb "${dependency_dir}"/*.rpm)
  ((${#packages[@]} > 0)) || die "Offline dependency bundle is empty for ${system_id} ${system_version} ${architecture}."
  case "${manager}" in
    apt) DEBIAN_FRONTEND=noninteractive apt-get install -y --no-download "${packages[@]}" ;;
    dnf) dnf --disablerepo='*' --setopt=install_weak_deps=False install -y "${packages[@]}" ;;
    yum) yum --disablerepo='*' localinstall -y "${packages[@]}" ;;
    zypper) zypper --non-interactive --no-refresh install --no-recommends "${packages[@]}" ;;
    *) die "Unsupported package manager: ${manager}" ;;
  esac
}

install_build_dependencies() {
  local manager dependency_dir
  manager="$(package_manager)"
  dependency_dir="$(offline_dependency_dir)"
  if [[ "${install_mode}" == offline ]]; then
    [[ -d "${dependency_dir}" ]] || die "Offline dependency bundle is missing for ${system_id} ${system_version} ${architecture}."
    install_local_dependencies "${manager}" "${dependency_dir}"
    return 0
  fi
  if [[ -d "${dependency_dir}" ]]; then
    install_local_dependencies "${manager}" "${dependency_dir}"
    return 0
  fi
  case "${manager}" in
    apt)
      apt-get update
      DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends ca-certificates curl tar xz-utils make gcc g++ python3 ;;
    dnf)
      dnf install -y ca-certificates curl tar xz make gcc gcc-c++ python3 ;;
    yum)
      yum install -y ca-certificates curl tar xz make gcc gcc-c++ python3 ;;
    zypper)
      zypper --non-interactive refresh
      zypper --non-interactive install --no-recommends ca-certificates curl tar xz make gcc gcc-c++ python3 ;;
  esac
}

install_runtime_dependencies() {
  local manager dependency_dir
  manager="$(package_manager)"
  dependency_dir="$(offline_dependency_dir)"
  if [[ "${install_mode}" == offline ]]; then
    [[ -d "${dependency_dir}" ]] || return 0
    shopt -s nullglob
    local packages=("${dependency_dir}"/*.deb "${dependency_dir}"/*.rpm)
    ((${#packages[@]} == 0)) && return 0
    install_local_dependencies "${manager}" "${dependency_dir}"
    return 0
  fi
  case "${manager}" in
    apt)
      apt-get update
      DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends ca-certificates curl tar xz-utils ;;
    dnf)
      dnf install -y ca-certificates curl tar xz ;;
    yum)
      yum install -y ca-certificates curl tar xz ;;
    zypper)
      zypper --non-interactive refresh
      zypper --non-interactive install --no-recommends ca-certificates curl tar xz ;;
  esac
}

select_compiler() {
  local candidate version
  gcc_bin=""
  gxx_bin=""
  for candidate in \
    /opt/rh/devtoolset-10/root/usr/bin/gcc \
    /opt/rh/gcc-toolset-10/root/usr/bin/gcc \
    "$(command -v gcc 2>/dev/null || true)"; do
    [[ -x "${candidate}" ]] || continue
    version="$("${candidate}" -dumpfullversion -dumpversion 2>/dev/null || true)"
    version_ge "${version}" 10.1 || continue
    gcc_bin="${candidate}"
    break
  done
  [[ -n "${gcc_bin}" ]] || die "CentOS 7 source build requires GCC 10.1 or newer."
  for candidate in \
    "$(dirname -- "${gcc_bin}")/g++" \
    /opt/rh/devtoolset-10/root/usr/bin/g++ \
    /opt/rh/gcc-toolset-10/root/usr/bin/g++; do
    [[ -x "${candidate}" ]] || continue
    gxx_bin="${candidate}"
    break
  done
  [[ -n "${gxx_bin}" ]] || die "CentOS 7 source build requires a matching g++ compiler."
  local gcc_dir gxx_dir
  gcc_dir="$(dirname -- "${gcc_bin}")"
  gxx_dir="$(dirname -- "${gxx_bin}")"
  export CC="${gcc_bin}" CXX="${gxx_bin}"
  export PATH="${gcc_dir}:${gxx_dir}:${PATH}"
}

verify_runtime_dir() {
  local runtime_dir="$1"
  local actual npm_version
  [[ -x "${runtime_dir}/bin/node" && -x "${runtime_dir}/bin/npm" && -x "${runtime_dir}/bin/npx" ]] || return 1
  actual="$("${runtime_dir}/bin/node" --version 2>/dev/null || true)"
  [[ "${actual}" == "v${software_version}" ]] || return 1
  npm_version="$(PATH="${runtime_dir}/bin:${PATH:-}" "${runtime_dir}/bin/npm" --version 2>/dev/null || true)"
  [[ "${npm_version}" =~ ^[0-9]+\.[0-9]+\.[0-9]+ ]] || return 1
}

verify_installed() { verify_runtime_dir "${install_dir}"; }

prepare_transaction() {
  install -d -m 0700 -- "${state_dir}"
  transaction_dir="$(mktemp -d "${state_dir}/.transaction.XXXXXX")"
  check_entrypoint_conflicts
  backup_unmanaged_conflicts
  if [[ -e "${profile_file}" ]]; then
    if is_managed_profile; then
      profile_backup="${transaction_dir}/nodejs.sh"
      cp -a -- "${profile_file}" "${profile_backup}"
    fi
  fi
  if [[ -e "${install_dir}" || -L "${install_dir}" ]]; then
    if [[ -f "${state_file}" ]]; then
      backup_dir="$(dirname -- "${install_dir}")/.nodejs-backup.$$"
      [[ ! -e "${backup_dir}" ]] || die "Node.js installation backup path already exists."
      mv -- "${install_dir}" "${backup_dir}"
    else
      backup_unmanaged_install_dir
    fi
  fi
}

restore_profile() {
  if [[ -n "${profile_backup}" && -f "${profile_backup}" ]]; then
    install -m 0644 -- "${profile_backup}" "${profile_file}"
  elif has_managed_profile; then
    rm -f -- "${profile_file}"
  fi
}

remove_entrypoints() {
  local name link target expected
  for name in node npm npx corepack nodejs; do
    link="${link_dir}/${name}"
    [[ -L "${link}" ]] || continue
    target="$(readlink -- "${link}")"
    expected="${install_dir}/bin/${name}"
    [[ "${target}" == "${expected}" ]] && rm -f -- "${link}"
  done
}

configure_entrypoints() {
  local name link
  install -d -m 0755 -- "${link_dir}"
  for name in node npm npx corepack nodejs; do
    [[ -x "${install_dir}/bin/${name}" ]] || continue
    link="${link_dir}/${name}"
    if [[ -e "${link}" || -L "${link}" ]]; then
      if [[ -L "${link}" && "$(readlink -- "${link}")" == "${install_dir}/bin/${name}" ]]; then
        continue
      fi
      [[ "${takeover_unmanaged_conflicts}" == true ]] ||
        die "Refusing to replace unmanaged ${link}."
      rm -f -- "${link}"
      ln -s -- "${install_dir}/bin/${name}" "${link}"
    else
      ln -s -- "${install_dir}/bin/${name}" "${link}"
    fi
  done
  local temporary_profile
  temporary_profile="${profile_file}.tmp.$$"
  printf '%s\n' '# OneinStack managed Node.js PATH' "export PATH=\"${install_dir}/bin:\${PATH}\"" >"${temporary_profile}"
  chmod 0644 -- "${temporary_profile}"
  mv -f -- "${temporary_profile}" "${profile_file}"
}

rollback_transaction() {
  local rollback_failed=false
  [[ "${transaction_committed}" == true ]] && return 0
  printf 'rollback started: restoring the previous Node.js installation state\n' >&2
  remove_entrypoints || rollback_failed=true
  if [[ -n "${stage_dir}" && -e "${stage_dir}" ]] && ! rm -rf -- "${stage_dir}"; then
    rollback_failed=true
  fi
  if [[ -n "${build_root}" && -e "${build_root}" ]] && ! rm -rf -- "${build_root}"; then
    rollback_failed=true
  fi
  if [[ -n "${backup_dir}" && -e "${backup_dir}" ]]; then
    if [[ -e "${install_dir}" || -L "${install_dir}" ]] && ! rm -rf -- "${install_dir}"; then
      rollback_failed=true
    elif ! mv -- "${backup_dir}" "${install_dir}"; then
      rollback_failed=true
    elif ! configure_entrypoints; then
      rollback_failed=true
    fi
    if [[ -n "${profile_backup}" || -e "${profile_file}" ]]; then
      restore_profile || rollback_failed=true
    fi
    restore_conflict_backups || rollback_failed=true
  else
    if [[ -n "${profile_backup}" || -e "${profile_file}" ]]; then
      restore_profile || rollback_failed=true
    fi
    restore_conflict_backups || rollback_failed=true
    if [[ "${rollback_failed}" == false ]]; then
      if [[ -n "${takeover_install_backup_dir}" ]]; then
        restore_takeover_install_backup || rollback_failed=true
      elif [[ -e "${install_dir}" || -L "${install_dir}" ]]; then
        rm -rf -- "${install_dir}" || rollback_failed=true
      fi
    fi
  fi
  if [[ "${rollback_failed}" == true ]]; then
    printf 'rollback failed: Node.js recovery artifacts were preserved for manual recovery\n' >&2
    return 1
  fi
  printf 'rollback succeeded: the previous Node.js installation state was restored\n' >&2
}

cleanup_transaction() {
  local status="$?" rollback_failed=false
  if [[ "${status}" -ne 0 ]]; then
    rollback_transaction || rollback_failed=true
    if [[ "${rollback_failed}" == false ]]; then
      [[ -z "${conflict_backup_dir}" || ! -e "${conflict_backup_dir}" ]] ||
        rm -rf -- "${conflict_backup_dir}"
    fi
  fi
  if [[ "${rollback_failed}" == false ]]; then
    [[ -z "${transaction_dir}" || ! -d "${transaction_dir}" ]] || rm -rf -- "${transaction_dir}"
    [[ -z "${backup_dir}" || ! -e "${backup_dir}" ]] || rm -rf -- "${backup_dir}"
    [[ -z "${build_root}" || ! -e "${build_root}" ]] || rm -rf -- "${build_root}"
  fi
  [[ -z "${artifact_temp_path}" || ! -e "${artifact_temp_path}" ]] || rm -f -- "${artifact_temp_path}"
  exit "${status}"
}

write_state() {
  local temporary
  install -d -m 0700 -- "${state_dir}"
  temporary="$(mktemp "${state_dir}/.installed.json.XXXXXX")"
  cat >"${temporary}" <<EOF
{
  "component":"${component_id}",
  "packageVersion":"${package_version}",
  "softwareVersion":"${software_version}",
  "runtimeVersion":"v${software_version}",
  "installDir":"${install_dir}",
  "system":"${system_id}",
  "systemVersion":"${system_version}",
  "architecture":"${architecture}",
  "route":"${route}",
  "artifactSHA256":"${artifact_sha256}",
  "conflictBackupDir":"${conflict_backup_dir}",
  "takeoverInstallBackupDir":"${takeover_install_backup_dir}"
}
EOF
  chmod 0600 -- "${temporary}"
  mv -f -- "${temporary}" "${state_file}"
}

check_common_commands() {
  local command_name
  for command_name in awk basename cp df dirname find grep head install ln mktemp mv readlink realpath rm sed sha256sum sort tar; do
    require_command "${command_name}"
  done
}
