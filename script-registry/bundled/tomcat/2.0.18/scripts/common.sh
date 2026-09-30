#!/usr/bin/env bash
set -Eeuo pipefail

component_id="tomcat"
component_name="Apache Tomcat"
package_version="2.0.18"
software_version="${SOFTWARE_VERSION:-11.0.15}"
install_dir="/usr/local/tomcat"
data_dir="/data/tomcat"
log_dir="${data_dir}/logs"
run_user="tomcat"
run_group="tomcat"
service_name="tomcat"
service_unit="/etc/systemd/system/tomcat.service"
state_dir="${ONEINSTACK_COMPONENT_STATE:-/var/lib/oneinstack/components}/tomcat"
state_dir="${state_dir%/}"
install_parameters_file="${state_dir}/installed.env"
installed_state_file="${state_dir}/installed.json"
backup_root="${state_dir}/config-backups"
rollback_root="${state_dir}/rollback"
managed_path_acl_file="${state_dir}/managed-path-acl"
transaction_path_acl_file="${rollback_root}/transaction-path-acl"
install_config_snapshot="${rollback_root}/previous-config"
cache_root="/var/cache/oneinstack/tomcat"
offline_root="${ONEINSTACK_OFFLINE_PACKAGE_PATH:-}"
install_mode="${ONEINSTACK_INSTALL_MODE:-center}"
architecture=""
os_id=""
os_version=""
package_manager=""
jdk_version=""
java_home=""
java_library_path=""
java_ldconfig_file="/etc/ld.so.conf.d/oneinstack-tomcat.conf"
tomcat_port="${TOMCAT_PORT:-8080}"
bind_address="${TOMCAT_BIND_ADDRESS:-127.0.0.1}"
jvm_xms_mb="${TOMCAT_JVM_XMS_MB:-256}"
jvm_xmx_mb="${TOMCAT_JVM_XMX_MB:-512}"
max_threads="${TOMCAT_MAX_THREADS:-200}"
accept_count="${TOMCAT_ACCEPT_COUNT:-100}"
connection_timeout_ms="${TOMCAT_CONNECTION_TIMEOUT_MS:-20000}"
uri_encoding="${TOMCAT_URI_ENCODING:-UTF-8}"
access_log_enabled="${ONEINSTACK_CONFIG_ACCESS_LOG_ENABLED:-false}"
offline_archives_dir=""
offline_packages_dir=""
artifact_path=""
artifact_url=""
artifact_sha256=""
artifact_name=""

die() {
  printf 'TOMCAT_ERROR: %s\n' "$*" >&2
  exit 1
}

require_root() {
  [[ "$(id -u)" -eq 0 ]] || die "root privileges are required"
}

command_exists() { command -v "$1" >/dev/null 2>&1; }

systemd_available() {
  command_exists systemctl && [[ -d /run/systemd/system || "${ONEINSTACK_ALLOW_SYSTEMD_TEST:-false}" == "true" ]]
}

require_command() {
  command_exists "$1" || die "required command is missing: $1"
}

normalize_architecture() {
  local raw
  raw="$(uname -m)"
  case "${raw}" in
    x86_64|amd64) architecture="amd64" ;;
    aarch64|arm64) architecture="arm64" ;;
    *) die "HOST_PLATFORM_UNSUPPORTED: architecture ${raw} is not supported" ;;
  esac
}

detect_host() {
  [[ -r /etc/os-release ]] || die "HOST_PLATFORM_UNSUPPORTED: /etc/os-release is missing"
  # shellcheck disable=SC1091
  . /etc/os-release
  local raw_id="${ID,,}" raw_version="${VERSION_ID:-}" pretty="${PRETTY_NAME:-}"
  os_id="${raw_id}"
  os_version="${raw_version//[^0-9.]/}"
  if [[ "${raw_id}" == "centos" && "${pretty,,}" == *stream* ]]; then
    os_id="centos-stream"
  elif [[ "${raw_id}" == "opensuse-leap" || "${raw_id}" == "opensuse-tumbleweed" ]]; then
    os_id="${raw_id}"
  elif [[ "${raw_id}" == opensuse-* ]]; then
    os_id="opensuse"
  fi
  case "${os_id}" in
    rhel|rocky|almalinux|ol|centos|centos-stream|sles)
      # Manifest and Bundle identities use the supported major release. Keep
      # Rocky 9.4, RHEL 9.5, etc. compatible with the declared 9 entry.
      os_version="${os_version%%.*}"
      ;;
  esac
  case "${os_id}" in
    ubuntu|debian) package_manager="apt" ;;
    rhel|rocky|almalinux|ol|centos-stream|fedora|amzn) package_manager="dnf" ;;
    centos) package_manager="yum" ;;
    sles|opensuse-leap|opensuse-tumbleweed|opensuse) package_manager="zypper" ;;
    *) die "HOST_PLATFORM_UNSUPPORTED: system ${os_id} ${os_version} is not supported" ;;
  esac
  [[ -n "${os_version}" ]] || die "HOST_PLATFORM_UNSUPPORTED: system version is empty"
}

host_version_supported() {
  case "${os_id}:${os_version}" in
    ubuntu:22.04|ubuntu:24.04|ubuntu:26.04|debian:11|debian:12|debian:13|\
    rhel:8|rhel:9|rhel:10|rocky:8|rocky:9|rocky:10|almalinux:8|almalinux:9|almalinux:10|\
    ol:8|ol:9|ol:10|centos:7|centos-stream:8|centos-stream:9|centos-stream:10|\
    amzn:2023|sles:15|sles:16) return 0 ;;
    fedora:*|opensuse-leap:*|opensuse-tumbleweed:*|opensuse:*) return 0 ;;
    *) return 1 ;;
  esac
}

validate_host() {
  normalize_architecture
  detect_host
  host_version_supported || die "HOST_PLATFORM_UNSUPPORTED: ${os_id} ${os_version} ${architecture}"
  systemd_available || die "HOST_PLATFORM_UNSUPPORTED: systemd is required for managed Tomcat"
}

jdk_version_for_tomcat() {
  case "${software_version}" in
    7.*|8.5.*) jdk_version="8" ;;
    9.*|10.1.*|11.*) jdk_version="17" ;;
    *) die "HOST_PLATFORM_UNSUPPORTED: Tomcat version ${software_version} is not declared" ;;
  esac
  java_home="/usr/lib/jvm/oneinstack-tomcat-jdk-${jdk_version}"
}

resolve_java_library_path() {
  local directory joined=""
  [[ -d "${java_home}" ]] || die "TOMCAT_ARTIFACT_MISSING: JDK home is missing: ${java_home}"
  while IFS= read -r directory; do
    [[ -n "${directory}" ]] || continue
    if [[ -n "${joined}" ]]; then joined+=":"; fi
    joined+="${directory}"
  done < <(find "${java_home}" -type f -name libjli.so -exec dirname {} \; 2>/dev/null | sort -u)
  [[ -n "${joined}" ]] || die "TOMCAT_ARTIFACT_MISSING: libjli.so is missing from ${java_home}"
  java_library_path="${joined}"
  export LD_LIBRARY_PATH="${java_library_path}"
}

refresh_java_ldconfig() {
  local ldconfig_command candidate
  local -a library_directories=()
  ldconfig_command="$(command -v ldconfig || true)"
  [[ -n "${ldconfig_command}" ]] || die "required command is missing: ldconfig"
  IFS=: read -r -a library_directories <<<"${java_library_path}"
  install -d -m 0755 "$(dirname "${java_ldconfig_file}")"
  candidate="$(mktemp "${java_ldconfig_file}.XXXXXX")"
  printf '%s\n' "${library_directories[@]}" >"${candidate}"
  chmod 0644 "${candidate}"
  mv -f -- "${candidate}" "${java_ldconfig_file}"
  normalize_selinux_contexts "${java_ldconfig_file}"
  "${ldconfig_command}"
}

normalize_java_home_permissions() {
  [[ -d "${java_home}" ]] || die "TOMCAT_ARTIFACT_MISSING: JDK home is missing: ${java_home}"
  chown -R root:root "${java_home}"
  find "${java_home}" -type d -exec chmod u+rwx,go+rx {} +
  find "${java_home}" -type f -exec chmod u+rw,go+r {} +
  for directory in "${java_home}/bin" "${java_home}/jre/bin"; do
    [[ -d "${directory}" ]] || continue
    find "${directory}" -type f -exec chmod a+x {} +
  done
  find "${java_home}" -type f \( -name jspawnhelper -o -name jexec \) -exec chmod a+x {} +
}

validate_java_runtime_user_access() {
  local output
  require_command runuser
  resolve_java_library_path
  if ! output="$(runuser -u "${run_user}" -- env -i \
    PATH=/usr/bin:/bin \
    JAVA_HOME="${java_home}" \
    LD_LIBRARY_PATH="${java_library_path}" \
    "${java_home}/bin/java" -version 2>&1)"; then
    printf '%s\n' "${output}" >&2
    die "JDK_RUNTIME_ACCESS_DENIED: ${run_user} cannot execute ${java_home}/bin/java"
  fi
}

selinux_runtime_enabled() {
  local selinux_state
  command_exists getenforce || return 1
  selinux_state="$(getenforce 2>/dev/null || true)"
  [[ "${selinux_state}" == Enforcing || "${selinux_state}" == Permissive ]]
}

normalize_selinux_contexts() {
  local path
  selinux_runtime_enabled || return 0
  require_command restorecon
  for path in "$@"; do
    [[ -e "${path}" || -L "${path}" ]] || continue
    restorecon -RF -- "${path}" || die "SELINUX_CONTEXT_RESTORE_FAILED: ${path}"
  done
}

set_tomcat_artifact() {
  local family="${software_version%%.*}"
  artifact_name="tomcat-${software_version}-${architecture}.tar.gz"
  artifact_url="https://archive.apache.org/dist/tomcat/tomcat-${family}/v${software_version}/bin/apache-tomcat-${software_version}.tar.gz"
  case "${software_version}" in
    7.0.109) artifact_sha256="ebfeb051e6da24bce583a4105439bfdafefdc7c5bdd642db2ab07e056211cb31" ;;
    8.5.96) artifact_sha256="0307cfb85e58a2ff3d033d464bdc01f03fd5452618b17baf64368b1e6b02e886" ;;
    9.0.113) artifact_sha256="790db2b8092b7954dec2afc6af71a7bbb6c67998198516dd6a9f865661b5d2a7" ;;
    10.1.50) artifact_sha256="f74f9f1a7ac2cf6eeede2c50f45088d9c3e55f77d5777f9f7033ed3d43ef529c" ;;
    11.0.15) artifact_sha256="c515a0edb273846b4d7926fa8175aaa46905f45d5e2af588e01783e35a89a69c" ;;
    *) die "TOMCAT_ARTIFACT_MISSING: Tomcat ${software_version}" ;;
  esac
}

set_jdk_artifact() {
  case "${jdk_version}:${architecture}" in
    8:amd64)
      artifact_name="jdk8-amd64.tar.gz"
      artifact_url="https://github.com/adoptium/temurin8-binaries/releases/download/jdk8u504-b01/OpenJDK8U-jdk_x64_linux_hotspot_8u504b01.tar.gz"
      artifact_sha256="9c70e102f527ac674ac2fe9c7d47b9a04e2d19842ba5ab8e9b33f368bbadfaea" ;;
    8:arm64)
      artifact_name="jdk8-arm64.tar.gz"
      artifact_url="https://github.com/adoptium/temurin8-binaries/releases/download/jdk8u504-b01/OpenJDK8U-jdk_aarch64_linux_hotspot_8u504b01.tar.gz"
      artifact_sha256="57b7ed8af9d48542bb49ff7894448040b17bea0a48b41677d11ecaec6129768d" ;;
    17:amd64)
      artifact_name="jdk17-amd64.tar.gz"
      artifact_url="https://github.com/adoptium/temurin17-binaries/releases/download/jdk-17.0.17%2B10/OpenJDK17U-jdk_x64_linux_hotspot_17.0.17_10.tar.gz"
      artifact_sha256="992f96e7995075ac7636bb1a8de52b0c61d71ed3137fafc979ab96b4ab78dd75" ;;
    17:arm64)
      artifact_name="jdk17-arm64.tar.gz"
      artifact_url="https://github.com/adoptium/temurin17-binaries/releases/download/jdk-17.0.17%2B10/OpenJDK17U-jdk_aarch64_linux_hotspot_17.0.17_10.tar.gz"
      artifact_sha256="dc29ca6d35beb4419b4b00419b8a3dfbf5ae551e1ae2b046b516d9a579d04533" ;;
    *) die "TOMCAT_ARTIFACT_MISSING: Temurin JDK ${jdk_version}/${architecture}" ;;
  esac
}

validate_scalar_inputs() {
  [[ "${software_version}" =~ ^(7\.0\.109|8\.5\.96|9\.0\.113|10\.1\.50|11\.0\.15)$ ]] ||
    die "TOMCAT_ARTIFACT_MISSING: unsupported Tomcat version ${software_version}"
  [[ "${tomcat_port}" =~ ^[0-9]+$ && "${tomcat_port}" -ge 1 && "${tomcat_port}" -le 65535 ]] || die "invalid TOMCAT_PORT"
  [[ "${jvm_xms_mb}" =~ ^[0-9]+$ && "${jvm_xms_mb}" -ge 16 && "${jvm_xms_mb}" -le 1048576 ]] || die "invalid TOMCAT_JVM_XMS_MB"
  [[ "${jvm_xmx_mb}" =~ ^[0-9]+$ && "${jvm_xmx_mb}" -ge "${jvm_xms_mb}" && "${jvm_xmx_mb}" -le 1048576 ]] || die "invalid TOMCAT_JVM_XMX_MB"
  [[ "${max_threads}" =~ ^[0-9]+$ && "${max_threads}" -ge 1 && "${max_threads}" -le 65535 ]] || die "invalid TOMCAT_MAX_THREADS"
  [[ "${accept_count}" =~ ^[0-9]+$ && "${accept_count}" -ge 0 && "${accept_count}" -le 65535 ]] || die "invalid TOMCAT_ACCEPT_COUNT"
  [[ "${connection_timeout_ms}" =~ ^[0-9]+$ && "${connection_timeout_ms}" -ge 100 && "${connection_timeout_ms}" -le 3600000 ]] || die "invalid TOMCAT_CONNECTION_TIMEOUT_MS"
  [[ "${uri_encoding}" =~ ^[A-Za-z0-9._-]{1,32}$ ]] || die "invalid TOMCAT_URI_ENCODING"
  [[ "${bind_address}" != *$'\n'* && "${bind_address}" != *$'\r'* && "${bind_address}" != *'<'* && "${bind_address}" != *'>'* && "${bind_address}" != *'"'* && "${bind_address}" != *'&'* ]] || die "invalid TOMCAT_BIND_ADDRESS"
  [[ "${bind_address}" =~ ^[A-Za-z0-9:.%_-]+$ ]] || die "invalid TOMCAT_BIND_ADDRESS"
  [[ "${access_log_enabled}" == true || "${access_log_enabled}" == false ]] || die "invalid ONEINSTACK_CONFIG_ACCESS_LOG_ENABLED"
  [[ "${install_mode}" == center || "${install_mode}" == offline ]] || die "invalid ONEINSTACK_INSTALL_MODE"
  jdk_version_for_tomcat
  set_tomcat_artifact
}

offline_bundle_info_value() {
  local key="$1"
  awk -F= -v wanted="${key}" '$1 == wanted {print substr($0, index($0,"=")+1); exit}' "${offline_root}/bundle-info"
}

validate_offline_bundle() {
  local expected_files actual_files package package_pattern
  [[ "${install_mode}" == offline ]] || return 0
  [[ -n "${offline_root}" && "${offline_root}" == /* ]] || die "OFFLINE_BUNDLE_INVALID: bundle path must be absolute"
  [[ -d "${offline_root}" && "$(cd "${offline_root}" && pwd -P)" == "${offline_root}" ]] || die "OFFLINE_BUNDLE_INVALID: bundle path is not canonical"
  [[ -f "${offline_root}/manifest.yaml" && -f "${offline_root}/bundle-info" && -f "${offline_root}/files.sha256" ]] || die "OFFLINE_BUNDLE_INVALID: required bundle files are missing"
  grep -Fqx '    id: tomcat' "${offline_root}/manifest.yaml" || die "OFFLINE_BUNDLE_IDENTITY_MISMATCH: manifest component"
  grep -Fqx "    version: ${package_version}" "${offline_root}/manifest.yaml" || die "OFFLINE_BUNDLE_IDENTITY_MISMATCH: manifest package version"
  grep -Eq "^    softwareVersions:.*${software_version}" "${offline_root}/manifest.yaml" || die "OFFLINE_BUNDLE_IDENTITY_MISMATCH: manifest software version"
  grep -Eq '^    architectures: \[amd64, arm64\]$' "${offline_root}/manifest.yaml" || die "OFFLINE_BUNDLE_IDENTITY_MISMATCH: manifest architecture matrix"
  [[ -z "${ONEINSTACK_OFFLINE_BUNDLE_ID:-}" || "${ONEINSTACK_OFFLINE_BUNDLE_ID}" =~ ^[0-9a-fA-F]{64}$ ]] || die "OFFLINE_BUNDLE_IDENTITY_MISMATCH: invalid bundle id"
  [[ -z "${ONEINSTACK_OFFLINE_BUNDLE_DIGEST:-}" || "${ONEINSTACK_OFFLINE_BUNDLE_DIGEST}" =~ ^[0-9a-fA-F]{64}$ ]] || die "OFFLINE_BUNDLE_IDENTITY_MISMATCH: invalid bundle digest"
  if [[ -n "${ONEINSTACK_OFFLINE_BUNDLE_ID:-}" && -n "${ONEINSTACK_OFFLINE_BUNDLE_DIGEST:-}" && "${ONEINSTACK_OFFLINE_BUNDLE_ID,,}" != "${ONEINSTACK_OFFLINE_BUNDLE_DIGEST,,}" ]]; then
    die "OFFLINE_BUNDLE_IDENTITY_MISMATCH: bundle id and digest differ"
  fi
  [[ "$(offline_bundle_info_value component)" == tomcat && "$(offline_bundle_info_value packageVersion)" == "${package_version}" ]] || die "OFFLINE_BUNDLE_IDENTITY_MISMATCH: component package identity"
  [[ "$(offline_bundle_info_value softwareVersion)" == "${software_version}" && "$(offline_bundle_info_value osId)" == "${os_id}" && "$(offline_bundle_info_value osVersion)" == "${os_version}" && "$(offline_bundle_info_value architecture)" == "${architecture}" ]] || die "OFFLINE_BUNDLE_IDENTITY_MISMATCH: target identity"
  [[ "$(offline_bundle_info_value jdkVersion)" == "${jdk_version}" ]] || die "OFFLINE_BUNDLE_IDENTITY_MISMATCH: JDK mapping"
  while IFS= read -r path; do
    [[ -z "${path}" ]] && continue
    [[ -L "${path}" || ! -f "${path}" ]] && die "OFFLINE_BUNDLE_INVALID: symlink or special file ${path}"
  done < <(find "${offline_root}" -mindepth 1 \( -type l -o -not -type f -a -not -type d \) -print)
  (cd "${offline_root}" && sha256sum -c files.sha256 >/dev/null) || die "OFFLINE_BUNDLE_CHECKSUM_MISMATCH: files.sha256"
  expected_files="$(awk '{print $2}' "${offline_root}/files.sha256" | sort)"
  actual_files="$(cd "${offline_root}" && find . -type f ! -name files.sha256 -print | sed 's#^\./##' | sort)"
  [[ "${expected_files}" == "${actual_files}" ]] || die "OFFLINE_BUNDLE_INVALID: checksum list does not exactly cover bundle files"
  offline_archives_dir="${offline_root}/artifacts/${architecture}"
  offline_packages_dir="${offline_root}/packages/${os_id}/${os_version}/${architecture}"
  [[ -d "${offline_archives_dir}" ]] || die "TOMCAT_ARTIFACT_MISSING: offline artifacts/${architecture}"
  [[ -d "${offline_packages_dir}" ]] || die "OFFLINE_DEPENDENCY_MISSING: package directory"
  find "${offline_packages_dir}" -maxdepth 1 -type f \( -name '*.deb' -o -name '*.rpm' \) -print -quit | grep -q . || die "OFFLINE_DEPENDENCY_MISSING: no matching package files"
  while IFS= read -r package; do
    if [[ "${package_manager}" == apt ]]; then package_pattern="${package}_*.deb"; else package_pattern="${package}-[0-9]*.rpm"; fi
    find "${offline_packages_dir}" -maxdepth 1 -type f -name "${package_pattern}" -print -quit | grep -q . || die "OFFLINE_DEPENDENCY_MISSING: ${package} package"
  done < <(dependency_names)
  [[ -f "${offline_archives_dir}/tomcat-${software_version}-${architecture}.tar.gz" && -f "${offline_archives_dir}/jdk${jdk_version}-${architecture}.tar.gz" ]] || die "TOMCAT_ARTIFACT_MISSING: offline Tomcat/JDK archive"
}

validate_inputs() {
  validate_host
  validate_scalar_inputs
  validate_offline_bundle
  require_command sha256sum
  require_command tar
  require_command awk
  require_command sed
  require_command install
  require_command find
  require_command systemctl
  if [[ "${install_mode}" == center ]]; then
    require_command curl
  fi
}

dependency_names() {
  case "${package_manager}" in
    apt) printf '%s\n' acl ca-certificates curl gzip iproute2 procps tar util-linux xz-utils ;;
    dnf|yum) printf '%s\n' acl ca-certificates curl gzip iproute procps-ng tar util-linux xz ;;
    zypper) printf '%s\n' acl ca-certificates curl gzip iproute2 procps tar util-linux xz ;;
  esac
  if selinux_runtime_enabled; then
    printf '%s\n' policycoreutils
  fi
  return 0
}

install_dependencies() {
  if [[ "${install_mode}" == center ]]; then
    case "${package_manager}" in
      apt)
        apt-get update
        mapfile -t packages < <(dependency_names)
        DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends "${packages[@]}" ;;
      dnf)
        mapfile -t packages < <(dependency_names)
        dnf install -y --setopt=install_weak_deps=False "${packages[@]}" ;;
      yum)
        mapfile -t packages < <(dependency_names)
        yum install -y "${packages[@]}" ;;
      zypper)
        mapfile -t packages < <(dependency_names)
        zypper --non-interactive install --no-recommends "${packages[@]}" ;;
    esac
    return 0
  fi
  local package
  case "${package_manager}" in
    apt)
      mapfile -t packages < <(find "${offline_packages_dir}" -maxdepth 1 -type f -name '*.deb' -print | sort)
      ((${#packages[@]} > 0)) || die "OFFLINE_DEPENDENCY_MISSING: no .deb packages"
      dpkg --install "${packages[@]}" || true
      apt-get -o Dir::Etc::sourcelist=- -o Dir::Etc::sourceparts=- -o Acquire::Retries=0 --no-download --fix-broken install -y ;;
    dnf)
      mapfile -t packages < <(find "${offline_packages_dir}" -maxdepth 1 -type f -name '*.rpm' -print | sort)
      ((${#packages[@]} > 0)) || die "OFFLINE_DEPENDENCY_MISSING: no .rpm packages"
      dnf --disablerepo='*' --cacheonly --setopt=install_weak_deps=False install -y "${packages[@]}" ;;
    yum)
      mapfile -t packages < <(find "${offline_packages_dir}" -maxdepth 1 -type f -name '*.rpm' -print | sort)
      ((${#packages[@]} > 0)) || die "OFFLINE_DEPENDENCY_MISSING: no .rpm packages"
      yum --disablerepo='*' --cacheonly install -y "${packages[@]}" ;;
    zypper)
      mapfile -t packages < <(find "${offline_packages_dir}" -maxdepth 1 -type f -name '*.rpm' -print | sort)
      ((${#packages[@]} > 0)) || die "OFFLINE_DEPENDENCY_MISSING: no .rpm packages"
      zypper --no-refresh --non-interactive install --allow-unsigned-rpm "${packages[@]}" ;;
    *) die "OFFLINE_DEPENDENCY_MISSING: unknown package manager" ;;
  esac
}

verify_artifact() {
  local path="$1" expected="$2"
  [[ -f "${path}" ]] || die "TOMCAT_ARTIFACT_MISSING: ${path}"
  local actual
  actual="$(sha256sum "${path}" | awk '{print $1}')"
  [[ "${actual}" == "${expected}" ]] || die "OFFLINE_BUNDLE_CHECKSUM_MISMATCH: ${path}"
}

download_artifact() {
  local destination="$1" url="$2"
  local -a options=(--fail --location --retry 3 --retry-delay 2 --proto '=https')
  # CentOS 7 ships curl 7.29.0; --tlsv1.2 was added in curl 7.34.0.
  # Its NSS backend still negotiates the TLS version required by the HTTPS
  # origin. Newer curl versions keep the explicit TLS 1.2 minimum.
  if curl --tlsv1.2 --version >/dev/null 2>&1; then
    options+=(--tlsv1.2)
  fi
  if ! curl "${options[@]}" -o "${destination}" "${url}"; then
    rm -f -- "${destination}"
    die "TOMCAT_ARTIFACT_DOWNLOAD_FAILED: ${artifact_name}"
  fi
}

obtain_artifact() {
  local runtime="$1"
  if [[ "${runtime}" == tomcat ]]; then set_tomcat_artifact; else set_jdk_artifact; fi
  if [[ "${install_mode}" == offline ]]; then
    artifact_path="${offline_archives_dir}/${artifact_name}"
  else
    mkdir -p "${cache_root}/${software_version}"
    artifact_path="${cache_root}/${software_version}/${artifact_name}"
    if [[ ! -f "${artifact_path}" ]] || ! sha256sum -c <(printf '%s  %s\n' "${artifact_sha256}" "${artifact_path}") >/dev/null 2>&1; then
      local temporary="${artifact_path}.part"
      rm -f -- "${temporary}"
      download_artifact "${temporary}" "${artifact_url}"
      mv -f -- "${temporary}" "${artifact_path}"
    fi
  fi
  verify_artifact "${artifact_path}" "${artifact_sha256}"
}

safe_extract_archive() {
  local archive="$1" destination="$2"
  while IFS= read -r entry; do
    [[ "${entry}" != /* && "${entry}" != *'../'* && "${entry}" != ../* ]] || die "TOMCAT_ARTIFACT_MISSING: unsafe archive path"
  done < <(tar -tzf "${archive}")
  mkdir -p "${destination}"
  tar -xzf "${archive}" -C "${destination}" --no-same-owner --no-same-permissions
}

create_runtime_account() {
  getent group "${run_group}" >/dev/null 2>&1 || groupadd --system "${run_group}"
  id "${run_user}" >/dev/null 2>&1 || useradd --system --gid "${run_group}" --home-dir "${data_dir}" --shell /usr/sbin/nologin "${run_user}"
}

port_is_available() {
  local target_port="$1" target_address="$2"
  command_exists ss || return 0
  local line main_pid_property main_pid=""
  if systemctl is-active --quiet "${service_name}.service" 2>/dev/null; then
    main_pid_property="$(systemctl show "${service_name}.service" --property MainPID 2>/dev/null || true)"
    if [[ "${main_pid_property}" == MainPID=* && "${main_pid_property#MainPID=}" =~ ^[1-9][0-9]*$ ]]; then
      main_pid="${main_pid_property#MainPID=}"
    fi
  fi
  while IFS= read -r line; do
    [[ "${line}" == *":${target_port} "* || "${line}" == *":${target_port}"* ]] || continue
    if [[ "${line}" == *"${target_address}:${target_port}"* || "${line}" == *":${target_port} "* ]]; then
      [[ -n "${main_pid}" && "${line}" == *"pid=${main_pid},"* ]] && continue
      return 1
    fi
  done < <(ss -H -ltnp 2>/dev/null || true)
  return 0
}

ensure_managed_service_ownership() {
  local fragment_property fragment_path=""
  fragment_property="$(systemctl show "${service_name}.service" --property FragmentPath 2>/dev/null || true)"
  if [[ "${fragment_property}" == FragmentPath=* ]]; then
    fragment_path="${fragment_property#FragmentPath=}"
  fi
  if [[ -n "${fragment_path}" && "${fragment_path}" != "${service_unit}" ]]; then
    die "EXTERNAL_TOMCAT_CONFLICT: tomcat.service is owned by ${fragment_path}"
  fi
  if [[ -e "${service_unit}" || -L "${service_unit}" ]]; then
    [[ -f "${service_unit}" && ! -L "${service_unit}" ]] || die "EXTERNAL_TOMCAT_CONFLICT: ${service_unit} is not a regular managed unit"
    grep -q '^Description=OneinStack Apache Tomcat ' "${service_unit}" || die "EXTERNAL_TOMCAT_CONFLICT: tomcat.service is not recognized"
    grep -q '^Environment=CATALINA_HOME=/usr/local/tomcat$' "${service_unit}" || die "EXTERNAL_TOMCAT_CONFLICT: tomcat.service has an external CATALINA_HOME"
  elif systemctl is-active --quiet "${service_name}.service" 2>/dev/null; then
    die "EXTERNAL_TOMCAT_CONFLICT: active tomcat.service is not managed by Oneinstack"
  fi
}

validate_install_port() {
  ensure_managed_service_ownership
  port_is_available "${tomcat_port}" "${bind_address}" || die "PORT_IN_USE: ${bind_address}:${tomcat_port}"
}

write_connector_line() {
  local file="$1" port="$2" address="$3" xthreads="$4" queue="$5" timeout="$6" encoding="$7"
  local candidate
  candidate="$(mktemp "${file}.candidate.XXXXXX")"
  awk -v port="${port}" -v address="${address}" -v threads="${xthreads}" -v queue="${queue}" -v timeout="${timeout}" -v encoding="${encoding}" '
    function managed_connector() {
      return "    <!-- ONEINSTACK MANAGED CONNECTOR -->\n" \
        "    <Connector port=\"" port "\" address=\"" address "\" protocol=\"HTTP/1.1\" connectionTimeout=\"" timeout "\" maxThreads=\"" threads "\" acceptCount=\"" queue "\" URIEncoding=\"" encoding "\" />"
    }
    /ONEINSTACK MANAGED CONNECTOR/ {
      if (!done) {
        print managed_connector()
        done=1
      }
      skip_marker_connector=1
      next
    }
    skip_marker_connector {
      if ($0 ~ /^[[:space:]]*<Connector[[:space:]]/) {
        skip_marker_connector=0
        skip_connector_tail=1
        next
      }
      skip_marker_connector=0
    }
    skip_connector_tail {
      if ($0 ~ /^[[:space:]]*[[:alnum:]_.:-]+[[:space:]]*=/ || $0 ~ /^[[:space:]]*\/>/) {
        if ($0 ~ /\/>/) skip_connector_tail=0
        next
      }
      skip_connector_tail=0
    }
    skip_connector {
      if ($0 ~ /\/>/) skip_connector=0
      next
    }
    /<Connector[[:space:]].*protocol="HTTP\/1\.1"/ {
      if (!done) {
        print managed_connector()
        done=1
      }
      if ($0 !~ /\/>/) skip_connector=1
      next
    }
    {print}
    END {if (!done) print managed_connector()}
  ' "${file}" >"${candidate}"
  mv -f -- "${candidate}" "${file}"
}

write_access_log_setting() {
  local file="$1" enabled="$2" candidate
  candidate="$(mktemp "${file}.candidate.XXXXXX")"
  awk -v enabled="${enabled}" '
    /ONEINSTACK MANAGED ACCESS LOG/ {if (enabled == "true") print "        <!-- ONEINSTACK MANAGED ACCESS LOG -->\n        <Valve className=\"org.apache.catalina.valves.AccessLogValve\" directory=\"logs\" prefix=\"localhost_access_log\" suffix=\".txt\" pattern=\"%h %l %u %t &quot;%r&quot; %s %b\" />"; next}
    /<Valve className="org\.apache\.catalina\.valves\.AccessLogValve/ {next}
    /<\/Host>/ && enabled == "true" {print "        <!-- ONEINSTACK MANAGED ACCESS LOG -->\n        <Valve className=\"org.apache.catalina.valves.AccessLogValve\" directory=\"logs\" prefix=\"localhost_access_log\" suffix=\".txt\" pattern=\"%h %l %u %t &quot;%r&quot; %s %b\" />"}
    {print}
  ' "${file}" >"${candidate}"
  mv -f -- "${candidate}" "${file}"
}

write_setenv() {
  local candidate
  resolve_java_library_path
  install -d -m 0750 "${data_dir}/bin"
  candidate="$(mktemp "${data_dir}/bin/setenv.sh.XXXXXX")"
  cat >"${candidate}" <<EOF
#!/usr/bin/env bash
# ONEINSTACK MANAGED SETENV
export JAVA_HOME="${java_home}"
export LD_LIBRARY_PATH="${java_library_path}"
export CATALINA_HOME="${install_dir}"
export CATALINA_BASE="${data_dir}"
export CATALINA_OPTS="-Xms${jvm_xms_mb}m -Xmx${jvm_xmx_mb}m"
EOF
  chmod 0750 "${candidate}"
  chown root:"${run_group}" "${candidate}"
  mv -f -- "${candidate}" "${data_dir}/bin/setenv.sh"
}

write_managed_service() {
  local candidate env_command
  resolve_java_library_path
  refresh_java_ldconfig
  env_command="$(command -v env || true)"
  [[ -n "${env_command}" ]] || die "required command is missing: env"
  candidate="$(mktemp "${service_unit}.XXXXXX")"
  cat >"${candidate}" <<EOF
[Unit]
Description=OneinStack Apache Tomcat ${software_version}
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=${run_user}
Group=${run_group}
Environment=CATALINA_HOME=${install_dir}
Environment=CATALINA_BASE=${data_dir}
Environment=JAVA_HOME=${java_home}
Environment=LD_LIBRARY_PATH=${java_library_path}
ExecStart=${env_command} LD_LIBRARY_PATH=${java_library_path} JAVA_HOME=${java_home} CATALINA_HOME=${install_dir} CATALINA_BASE=${data_dir} ${install_dir}/bin/catalina.sh run
ExecStop=${env_command} LD_LIBRARY_PATH=${java_library_path} JAVA_HOME=${java_home} CATALINA_HOME=${install_dir} CATALINA_BASE=${data_dir} ${install_dir}/bin/catalina.sh stop
Restart=on-failure
RestartSec=5
UMask=0027
LimitNOFILE=65535

[Install]
WantedBy=multi-user.target
EOF
  chmod 0644 "${candidate}"
  mv -f -- "${candidate}" "${service_unit}"
  normalize_selinux_contexts "${service_unit}"
}

tomcat_version_from_binary() {
  local output detected
  [[ -x "${install_dir}/bin/version.sh" ]] || return 1
  resolve_java_library_path
  output="$(
    JAVA_HOME="${java_home}" \
      LD_LIBRARY_PATH="${java_library_path}" \
      CATALINA_HOME="${install_dir}" \
      CATALINA_BASE="${data_dir}" \
      "${install_dir}/bin/version.sh" 2>&1
  )" || return 1
  detected="$(printf '%s\n' "${output}" | sed -nE 's/^Server version:[[:space:]]*Apache Tomcat\/([^[:space:]]+).*$/\1/p' | head -n1)"
  if [[ -z "${detected}" ]]; then
    detected="$(printf '%s\n' "${output}" | sed -nE 's/^Server number:[[:space:]]*([^[:space:]]+).*$/\1/p' | head -n1)"
    detected="${detected%.0}"
  fi
  [[ -n "${detected}" ]] || return 1
  printf '%s\n' "${detected}"
}

tomcat_ready() {
  command_exists curl || return 1
  local probe_address="${bind_address}"
  [[ "${probe_address}" == 0.0.0.0 ]] && probe_address="127.0.0.1"
  [[ "${probe_address}" == *:* && "${probe_address}" != \[*\] ]] && probe_address="[${probe_address}]"
  curl --silent --show-error --connect-timeout 1 --max-time 3 "http://${probe_address}:${tomcat_port}/" >/dev/null
}

service_active() { systemctl is-active --quiet "${service_name}.service"; }

tomcat_java_process_ready() {
  local main_pid_property main_pid java_binary process_binary command_line
  main_pid_property="$(systemctl show "${service_name}.service" --property MainPID 2>/dev/null || true)"
  [[ "${main_pid_property}" == MainPID=* ]] || return 1
  main_pid="${main_pid_property#MainPID=}"
  [[ "${main_pid}" =~ ^[1-9][0-9]*$ ]] || return 1
  java_binary="$(readlink -f -- "${java_home}/bin/java" 2>/dev/null || true)"
  process_binary="$(readlink -f -- "/proc/${main_pid}/exe" 2>/dev/null || true)"
  [[ -n "${java_binary}" && "${process_binary}" == "${java_binary}" ]] || return 1
  command_line="$(tr '\0' ' ' <"/proc/${main_pid}/cmdline" 2>/dev/null || true)"
  [[ "${command_line}" == *"-Dcatalina.base=${data_dir}"* ]]
}

wait_for_tomcat_java_process() {
  local attempt service_state
  for attempt in $(seq 1 30); do
    tomcat_java_process_ready && return 0
    service_state="$(systemctl is-active "${service_name}.service" 2>/dev/null || true)"
    [[ "${service_state}" == active || "${service_state}" == activating ]] || return 1
    sleep 1
  done
  return 1
}

wait_for_tomcat_http() {
  local attempt service_state
  for attempt in $(seq 1 30); do
    tomcat_ready >/dev/null 2>&1 && return 0
    service_state="$(systemctl is-active "${service_name}.service" 2>/dev/null || true)"
    [[ "${service_state}" == active || "${service_state}" == activating ]] || return 1
    sleep 1
  done
  return 1
}

report_service_failure() {
  systemctl status "${service_name}.service" --no-pager --full >&2 || true
  if command_exists journalctl; then
    journalctl -u "${service_name}.service" -n 80 --no-pager -o cat >&2 || true
  fi
}

validate_runtime() {
  local detected_version
  resolve_java_library_path
  validate_java_runtime_user_access
  if ! systemctl is-active --quiet "${service_name}.service"; then
    report_service_failure
    die "Tomcat service is not active"
  fi
  detected_version="$(tomcat_version_from_binary || true)"
  [[ "${detected_version}" == "${software_version}" ]] || die "Tomcat version verification failed: expected ${software_version}, detected ${detected_version:-unavailable}"
  LD_LIBRARY_PATH="${java_library_path}" "${java_home}/bin/java" -version >/dev/null 2>&1 || die "JDK runtime verification failed"
  if ! wait_for_tomcat_java_process; then
    report_service_failure
    die "Tomcat Java process verification failed"
  fi
  if ! wait_for_tomcat_http; then
    report_service_failure
    die "Tomcat HTTP verification failed on ${bind_address}:${tomcat_port}"
  fi
}

persist_install_parameters() {
  install -d -m 0750 "${state_dir}"
  cat >"${install_parameters_file}" <<EOF
SOFTWARE_VERSION=${software_version}
TOMCAT_PORT=${tomcat_port}
TOMCAT_BIND_ADDRESS=${bind_address}
TOMCAT_JVM_XMS_MB=${jvm_xms_mb}
TOMCAT_JVM_XMX_MB=${jvm_xmx_mb}
TOMCAT_MAX_THREADS=${max_threads}
TOMCAT_ACCEPT_COUNT=${accept_count}
TOMCAT_CONNECTION_TIMEOUT_MS=${connection_timeout_ms}
TOMCAT_URI_ENCODING=${uri_encoding}
JDK_VERSION=${jdk_version}
JAVA_HOME=${java_home}
EOF
  chmod 0600 "${install_parameters_file}"
  cat >"${installed_state_file}" <<EOF
{
  "component": "tomcat",
  "packageVersion": "${package_version}",
  "softwareVersion": "${software_version}",
  "jdkVersion": "${jdk_version}",
  "javaHome": "${java_home}",
  "installDir": "${install_dir}",
  "dataDir": "${data_dir}",
  "logDir": "${log_dir}",
  "serviceName": "${service_name}",
  "runUser": "${run_user}",
  "runGroup": "${run_group}"
}
EOF
  chmod 0600 "${installed_state_file}"
}

load_persisted_parameters() {
  if [[ -f "${install_parameters_file}" ]]; then
    # shellcheck disable=SC1090
    . "${install_parameters_file}"
    software_version="${SOFTWARE_VERSION:-${software_version}}"
    tomcat_port="${TOMCAT_PORT:-${tomcat_port}}"
    bind_address="${TOMCAT_BIND_ADDRESS:-${bind_address}}"
    jvm_xms_mb="${TOMCAT_JVM_XMS_MB:-${jvm_xms_mb}}"
    jvm_xmx_mb="${TOMCAT_JVM_XMX_MB:-${jvm_xmx_mb}}"
    max_threads="${TOMCAT_MAX_THREADS:-${max_threads}}"
    accept_count="${TOMCAT_ACCEPT_COUNT:-${accept_count}}"
    connection_timeout_ms="${TOMCAT_CONNECTION_TIMEOUT_MS:-${connection_timeout_ms}}"
    uri_encoding="${TOMCAT_URI_ENCODING:-${uri_encoding}}"
  fi
  jdk_version_for_tomcat
}

config_revision() {
  local config_file="${data_dir}/conf/server.xml" setenv_file="${data_dir}/bin/setenv.sh"
  [[ -f "${config_file}" && -f "${setenv_file}" ]] || die "configuration is unavailable"
  { sha256sum "${config_file}"; sha256sum "${setenv_file}"; } | sha256sum | awk '{print $1}'
}

prune_config_backups() {
  local -a backups=()
  mapfile -t backups < <(find "${backup_root}" -mindepth 1 -maxdepth 1 -type d -printf '%T@ %p\n' 2>/dev/null | sort -rn | cut -d' ' -f2-)
  local index
  for ((index=20; index<${#backups[@]}; index++)); do rm -rf -- "${backups[index]}"; done
}

snapshot_install_configuration() {
  local name path
  rm -rf -- "${install_config_snapshot}"
  install -d -m 0700 "${install_config_snapshot}"
  for name in server.xml setenv.sh; do
    if [[ "${name}" == server.xml ]]; then
      path="${data_dir}/conf/server.xml"
    else
      path="${data_dir}/bin/setenv.sh"
    fi
    if [[ -e "${path}" || -L "${path}" ]]; then
      [[ -f "${path}" ]] || die "TOMCAT_DATA_LAYOUT_INVALID: ${path} is not a regular file"
      cp -pL -- "${path}" "${install_config_snapshot}/${name}"
      touch "${install_config_snapshot}/${name}.present"
    else
      touch "${install_config_snapshot}/${name}.absent"
    fi
  done
}

restore_install_configuration() {
  local name path
  [[ -d "${install_config_snapshot}" ]] || return 0
  for name in server.xml setenv.sh; do
    if [[ "${name}" == server.xml ]]; then
      path="${data_dir}/conf/server.xml"
    else
      path="${data_dir}/bin/setenv.sh"
    fi
    if [[ -f "${install_config_snapshot}/${name}.present" ]]; then
      install -d -m 0750 "$(dirname "${path}")"
      cp -p -- "${install_config_snapshot}/${name}" "${path}"
    elif [[ -f "${install_config_snapshot}/${name}.absent" ]]; then
      rm -f -- "${path}"
    fi
  done
}

record_managed_path_acl() {
  local acl_path="$1" record
  record="${run_user}"$'\t'"${acl_path}"
  install -d -m 0750 "${state_dir}" "${rollback_root}"
  if [[ ! -f "${managed_path_acl_file}" ]] || ! grep -Fqx -- "${record}" "${managed_path_acl_file}"; then
    printf '%s\n' "${record}" >>"${managed_path_acl_file}"
    chmod 0600 "${managed_path_acl_file}"
  fi
  if [[ -f "${transaction_path_acl_file}" ]] && ! grep -Fqx -- "${record}" "${transaction_path_acl_file}"; then
    printf '%s\n' "${record}" >>"${transaction_path_acl_file}"
    chmod 0600 "${transaction_path_acl_file}"
  fi
}

ensure_default_data_root_access() {
  local current_acl access_mask existing_user_permissions
  [[ "${data_dir}" == /data/* ]] || return 0
  require_command runuser
  runuser -u "${run_user}" -- test -x /data && return 0
  require_command getfacl
  require_command setfacl
  [[ "$(stat -c '%u' /data)" == 0 ]] || die "RUNTIME_PATH_ACL_CONFLICT: /data is not root-owned"
  current_acl="$(getfacl -cp -- /data)" || die "RUNTIME_PATH_ACL_INSPECTION_FAILED: /data"
  existing_user_permissions="$(awk -F: -v user="${run_user}" '$1 == "user" && $2 == user {print $3; exit}' <<<"${current_acl}")"
  [[ -z "${existing_user_permissions}" ]] || die "RUNTIME_PATH_ACL_CONFLICT: existing ACL for ${run_user} on /data was preserved"
  access_mask="$(awk -F: '$1 == "mask" && $2 == "" {print $3; exit}' <<<"${current_acl}")"
  if [[ -n "${access_mask}" ]]; then
    [[ "${access_mask}" == *x* ]] || die "RUNTIME_PATH_ACL_MASK_CONFLICT: /data ACL mask denies traversal"
    setfacl --no-mask -m "u:${run_user}:--x" -- /data || die "RUNTIME_PATH_ACL_APPLY_FAILED: /data"
  else
    setfacl -m "u:${run_user}:--x" -- /data || die "RUNTIME_PATH_ACL_APPLY_FAILED: /data"
  fi
  record_managed_path_acl /data
  runuser -u "${run_user}" -- test -x /data || die "RUNTIME_PATH_TRAVERSAL_DENIED: ${run_user} cannot traverse /data"
}

remove_managed_path_acl_entries() {
  local records_file="$1" acl_user acl_path current_permissions
  [[ -f "${records_file}" ]] || return 0
  if ! command_exists getfacl || ! command_exists setfacl; then
    printf 'WARNING: managed Tomcat path ACL could not be restored because ACL utilities are unavailable.\n' >&2
    return 0
  fi
  while IFS=$'\t' read -r acl_user acl_path; do
    [[ "${acl_user}" == "${run_user}" && "${acl_path}" == /data ]] || continue
    [[ -d "${acl_path}" ]] || continue
    current_permissions="$(getfacl -cp -- "${acl_path}" 2>/dev/null | awk -F: -v user="${acl_user}" '$1 == "user" && $2 == user {print $3; exit}' || true)"
    if [[ "${current_permissions}" == "--x" ]]; then
      setfacl --no-mask -x "u:${acl_user}" -- "${acl_path}" || printf 'WARNING: managed Tomcat path ACL could not be restored for %s.\n' "${acl_path}" >&2
    elif [[ -n "${current_permissions}" ]]; then
      printf 'WARNING: managed Tomcat path ACL for %s was changed externally and was preserved.\n' "${acl_path}" >&2
    fi
  done <"${records_file}"
}

restore_transaction_path_acl() {
  local temporary record
  [[ -f "${transaction_path_acl_file}" ]] || return 0
  remove_managed_path_acl_entries "${transaction_path_acl_file}"
  if [[ -f "${managed_path_acl_file}" ]]; then
    temporary="$(mktemp "${state_dir}/.managed-path-acl.XXXXXX")"
    while IFS= read -r record; do
      grep -Fqx -- "${record}" "${transaction_path_acl_file}" || printf '%s\n' "${record}" >>"${temporary}"
    done <"${managed_path_acl_file}"
    if [[ -s "${temporary}" ]]; then
      chmod 0600 "${temporary}"
      mv -f -- "${temporary}" "${managed_path_acl_file}"
    else
      rm -f -- "${temporary}" "${managed_path_acl_file}"
    fi
  fi
  rm -f -- "${transaction_path_acl_file}"
}

commit_transaction_path_acl() {
  rm -f -- "${transaction_path_acl_file}"
}

remove_all_managed_path_acl() {
  remove_managed_path_acl_entries "${managed_path_acl_file}"
  rm -f -- "${managed_path_acl_file}" "${transaction_path_acl_file}"
}

data_layout_backup="${rollback_root}/previous-data-layout"
managed_data_entries=(bin conf logs temp webapps work)

copy_managed_data_directory() {
  local source_path="$1" destination_path="$2"
  [[ -d "${source_path}" ]] || die "TOMCAT_DATA_LAYOUT_INVALID: ${source_path} is not a directory"
  install -d -m 0750 "${destination_path}"
  cp -a -- "${source_path}/." "${destination_path}/"
}

prepare_managed_data_layout() {
  local entry link_path link_target resolved_target source_path

  rm -rf -- "${data_layout_backup}"
  install -d -m 0700 "${data_layout_backup}"

  if [[ -L "${data_dir}" ]]; then
    link_target="$(readlink -- "${data_dir}")"
    resolved_target="$(readlink -f -- "${data_dir}" 2>/dev/null || true)"
    [[ -n "${resolved_target}" && -d "${resolved_target}" ]] || die "TOMCAT_DATA_LAYOUT_INVALID: ${data_dir} has an invalid symlink target"
    case "${resolved_target}" in
      /|/bin|/boot|/dev|/etc|/home|/lib|/lib64|/proc|/root|/run|/sbin|/sys|/usr|/var)
        die "TOMCAT_DATA_LAYOUT_INVALID: refusing broad data directory target ${resolved_target}"
        ;;
    esac
    printf '%s\n' "${link_target}" >"${data_layout_backup}/root.link"
    rm -- "${data_dir}"
    install -d -m 0750 "${data_dir}"
    for entry in "${managed_data_entries[@]}"; do
      source_path="${resolved_target}/${entry}"
      if [[ -d "${source_path}" ]]; then
        copy_managed_data_directory "${source_path}" "${data_dir}/${entry}"
      elif [[ -e "${source_path}" || -L "${source_path}" ]]; then
        die "TOMCAT_DATA_LAYOUT_INVALID: ${source_path} is not a directory"
      fi
    done
    printf 'Tomcat data migration: materialized legacy link %s -> %s.\n' "${data_dir}" "${resolved_target}"
  fi

  for entry in "${managed_data_entries[@]}"; do
    link_path="${data_dir}/${entry}"
    if [[ -L "${link_path}" ]]; then
      link_target="$(readlink -- "${link_path}")"
      resolved_target="$(readlink -f -- "${link_path}" 2>/dev/null || true)"
      [[ -n "${resolved_target}" && -d "${resolved_target}" ]] || die "TOMCAT_DATA_LAYOUT_INVALID: ${link_path} has an invalid symlink target"
      printf '%s\n' "${link_target}" >"${data_layout_backup}/${entry}.link"
      rm -- "${link_path}"
      copy_managed_data_directory "${resolved_target}" "${link_path}"
      printf 'Tomcat data migration: materialized legacy link %s -> %s.\n' "${link_path}" "${resolved_target}"
    fi
  done

  link_path="${data_dir}/conf/server.xml"
  if [[ -L "${link_path}" ]]; then
    link_target="$(readlink -- "${link_path}")"
    resolved_target="$(readlink -f -- "${link_path}" 2>/dev/null || true)"
    [[ -n "${resolved_target}" && -f "${resolved_target}" ]] || die "TOMCAT_DATA_LAYOUT_INVALID: ${link_path} has an invalid symlink target"
    printf '%s\n' "${link_target}" >"${data_layout_backup}/server.xml.link"
    cp -pL -- "${resolved_target}" "${link_path}.migrating"
    rm -- "${link_path}"
    mv -- "${link_path}.migrating" "${link_path}"
    printf 'Tomcat data migration: materialized legacy link %s -> %s.\n' "${link_path}" "${resolved_target}"
  fi
}

restore_managed_data_layout() {
  local entry link_target
  [[ -d "${data_layout_backup}" ]] || return 0

  if [[ -f "${data_layout_backup}/root.link" ]]; then
    link_target="$(cat "${data_layout_backup}/root.link")"
    rm -rf -- "${data_dir}"
    ln -s -- "${link_target}" "${data_dir}"
    rm -rf -- "${data_layout_backup}"
    return 0
  fi

  if [[ -f "${data_layout_backup}/conf.link" ]]; then
    link_target="$(cat "${data_layout_backup}/conf.link")"
    rm -rf -- "${data_dir}/conf"
    ln -s -- "${link_target}" "${data_dir}/conf"
  elif [[ -f "${data_layout_backup}/server.xml.link" ]]; then
    link_target="$(cat "${data_layout_backup}/server.xml.link")"
    rm -f -- "${data_dir}/conf/server.xml"
    ln -s -- "${link_target}" "${data_dir}/conf/server.xml"
  fi

  for entry in "${managed_data_entries[@]}"; do
    [[ "${entry}" == conf ]] && continue
    if [[ -f "${data_layout_backup}/${entry}.link" ]]; then
      link_target="$(cat "${data_layout_backup}/${entry}.link")"
      rm -rf -- "${data_dir:?}/${entry}"
      ln -s -- "${link_target}" "${data_dir}/${entry}"
    fi
  done
  rm -rf -- "${data_layout_backup}"
}

commit_managed_data_layout() {
  rm -rf -- "${data_layout_backup}"
}

ensure_runtime_dirs() {
  install -d -m 0750 "${data_dir}" "${data_dir}/bin" "${data_dir}/conf" "${data_dir}/logs" "${data_dir}/temp" "${data_dir}/webapps" "${data_dir}/work"
  chown -R "${run_user}:${run_group}" "${data_dir}"
  chmod 0750 "${data_dir}" "${data_dir}/conf" "${data_dir}/bin"
  chmod 0750 "${data_dir}/logs" "${data_dir}/temp" "${data_dir}/webapps" "${data_dir}/work"
}
