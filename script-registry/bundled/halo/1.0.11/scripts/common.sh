#!/usr/bin/env bash
set -Eeuo pipefail
umask 027

component_id="halo"
package_version="1.0.11"
software_version="${SOFTWARE_VERSION:-2.25.4}"
service_name="halo"
halo_port="${HALO_PORT:-8090}"
bind_address="${HALO_BIND_ADDRESS:-127.0.0.1}"
external_url="${HALO_EXTERNAL_URL:-}"
install_dir="${INSTALL_DIR:-/opt/oneinstack/halo}"
data_dir="${DATA_DIR:-/var/lib/halo}"
config_dir="${CONFIG_DIR:-/etc/halo}"
run_user="${RUN_USER:-halo}"
run_group="${RUN_GROUP:-halo}"
jvm_xms_mb="${JVM_XMS_MB:-256}"
jvm_xmx_mb="${JVM_XMX_MB:-256}"
database_type="${DATABASE_TYPE:-h2}"
database_host="${DATABASE_HOST:-127.0.0.1}"
database_port="${DATABASE_PORT:-}"
database_name="${DATABASE_NAME:-halo}"
database_username="${DATABASE_USERNAME:-halo}"
database_password="${DATABASE_PASSWORD:-}"
adopt_legacy_data="${ADOPT_LEGACY_DATA:-true}"
use_absolute_permalink="${USE_ABSOLUTE_PERMALINK:-false}"
forward_headers_strategy="${FORWARD_HEADERS_STRATEGY:-native}"
compression_enabled="${COMPRESSION_ENABLED:-true}"
static_cache_max_age_days="${STATIC_CACHE_MAX_AGE_DAYS:-365}"
log_max_file_size_mb="${LOG_MAX_FILE_SIZE_MB:-10}"
log_total_size_cap_mb="${LOG_TOTAL_SIZE_CAP_MB:-1024}"
log_max_history="${LOG_MAX_HISTORY:-0}"
state_root="${ONEINSTACK_COMPONENT_STATE:-${COMPONENT_STATE_DIR:-/var/lib/oneinstack/components}}"
state_dir="${state_root}/${component_id}"
rollback_dir="${state_dir}/rollback"
install_parameters_file="${state_dir}/install-parameters"
managed_acl_file="${state_dir}/managed-path-acl"
transaction_acl_file="${rollback_dir}/path-acl-added"
retained_data_file="${state_dir}/retained-data"
config_file="${config_dir}/application.yaml"
environment_file="${config_dir}/halo.env"
unit_file="/etc/systemd/system/${service_name}.service"
jar_file="${install_dir}/halo.jar"
jre_dir="${install_dir}/jre"
legacy_data_dir="/root/.halo2"
install_mode="${ONEINSTACK_INSTALL_MODE:-center}"
offline_package_path="${ONEINSTACK_OFFLINE_PACKAGE_PATH:-}"
offline_bundle_id="${ONEINSTACK_OFFLINE_BUNDLE_ID:-}"
offline_bundle_digest="${ONEINSTACK_OFFLINE_BUNDLE_DIGEST:-}"
script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
component_root="$(cd -- "${script_dir}/.." && pwd)"
# shellcheck disable=SC2034 # Used by precheck.sh after sourcing this file.
bundled_adoptium_key="${component_root}/assets/adoptium-release-key.asc"
adoptium_key_url="https://packages.adoptium.net/artifactory/api/gpg/key/public"
adoptium_key_sha256="a46d5d3ab75c3c86dddf1bfd2957a067a24b1c6b2d2ed2bc69294bf970c5160b"
adoptium_key_fingerprint="3B04D753C9050D9A5D343F39843C48A565F8F04B"
# shellcheck disable=SC2034 # Used by status/config/verify after sourcing this file.
jre_version="21.0.12+8"
jar_url=""
jar_sha256=""
jre_url=""
jre_signature_url=""
jre_signature_sha256=""
jre_sha256=""
jre_archive=""
host_arch=""
system_id=""
system_version=""
package_manager=""

die() { printf 'ERROR: %s\n' "$*" >&2; exit 1; }
require_root() { [[ "$(id -u)" -eq 0 ]] || die "This action must run as root."; }
require_command() { command -v "$1" >/dev/null 2>&1 || die "HOST_DEPENDENCY_MISSING: required command not found: $1"; }
gpg_command() {
  if command -v gpg >/dev/null 2>&1; then command -v gpg
  elif command -v gpg2 >/dev/null 2>&1; then command -v gpg2
  else die "HOST_DEPENDENCY_MISSING: required command not found: gpg or gpg2"; fi
}
emit_progress() {
  local percent="$1" code="$2" message="$3" fd="${ONEINSTACK_PROGRESS_FD:-}"
  [[ "${fd}" =~ ^[0-9]+$ ]] || return 0
  message="${message//\\/\\\\}"; message="${message//\"/\\\"}"; message="${message//$'\n'/ }"
  # shellcheck disable=SC2261
  printf '{"type":"progress","percent":%s,"code":"%s","message":"%s"}\n' \
    "${percent}" "${code}" "${message}" >&"${fd}" 2>/dev/null || true
}
has_systemd() { command -v systemctl >/dev/null 2>&1 && [[ -d /run/systemd/system ]]; }
architecture_name() {
  case "$(uname -m)" in
    x86_64) printf 'amd64' ;;
    aarch64|arm64) printf 'arm64' ;;
    *) die "HOST_ARCH_UNSUPPORTED: unsupported CPU architecture $(uname -m)." ;;
  esac
}
validate_path() {
  local value="$1" label="$2"
  [[ "${value}" == /* && "$(realpath -m -- "${value}")" == "${value}" ]] ||
    die "INVALID_PARAMETER: ${label} must be a normalized absolute path."
  case "${value}" in
    /|/usr|/usr/local|/opt|/etc|/var|/var/lib|/data|/home|/root) die "INVALID_PARAMETER: ${label} is too broad: ${value}." ;;
  esac
  [[ "${value}" != *[[:space:]\"\'\;\|\&\$\`\*\?\[\]\{\}\(\)\<\>\\\#]* ]] ||
    die "INVALID_PARAMETER: ${label} contains unsupported characters."
}
paths_overlap() {
  local left="$1" right="$2"
  [[ "${left}" == "${right}" || "${left}" == "${right}/"* || "${right}" == "${left}/"* ]]
}
validate_host_value() {
  local value="$1" label="$2"
  [[ -n "${value}" && "${value}" != *[$'\r\n\t ']* && "${value}" != *[\{\}\[\]\#\&\*\!\|\>\<\'\"\\]* ]] ||
    die "INVALID_PARAMETER: ${label} contains whitespace or control characters."
  if [[ "${value}" == *:* ]]; then
    [[ "${value}" =~ ^[0-9A-Fa-f:]+$ && "${value}" == *:*:* && "${value}" != *:::* ]] ||
      die "INVALID_PARAMETER: ${label} is not a valid IPv6 literal."
  else
    [[ "${value}" =~ ^[A-Za-z0-9][A-Za-z0-9.-]{0,252}$ && "${value}" != *..* && "${value}" != .* && "${value}" != *. ]] ||
      die "INVALID_PARAMETER: ${label} is not a valid IPv4 address or hostname."
  fi
}
validate_external_url() {
  [[ -z "${external_url}" ]] && return 0
  [[ "${external_url}" =~ ^https?://(\[[0-9A-Fa-f:]+\]|[A-Za-z0-9._~-]+)(:([0-9]{1,5}))?(/[^[:space:]\"\'\<\>]*)?$ ]] ||
    die "INVALID_PARAMETER: HALO_EXTERNAL_URL must be an HTTP or HTTPS URL without credentials."
  [[ -z "${BASH_REMATCH[3]:-}" ]] || ((10#${BASH_REMATCH[3]} >= 1 && 10#${BASH_REMATCH[3]} <= 65535)) ||
    die "INVALID_PARAMETER: HALO_EXTERNAL_URL contains an invalid port."
}
validate_identifier() {
  [[ "$1" =~ ^[A-Za-z_][A-Za-z0-9_]{0,62}$ ]] || die "INVALID_PARAMETER: $2 is not a safe database identifier."
}
validate_account_name() {
  [[ "$1" =~ ^[a-z_][a-z0-9_-]{0,30}\$?$ ]] || die "INVALID_PARAMETER: $2 is not a safe system account name."
}
validate_boolean() { [[ "$1" == true || "$1" == false ]] || die "INVALID_PARAMETER: $2 must be true or false."; }
validate_integer_range() {
  local value="$1" label="$2" minimum="$3" maximum="$4"
  if [[ ! "${value}" =~ ^[0-9]+$ ]] || ((10#${value} < minimum || 10#${value} > maximum)); then
    die "INVALID_PARAMETER: ${label} must be between ${minimum} and ${maximum}."
  fi
}

load_persisted_parameters() {
  local key value marker
  [[ -r "${install_parameters_file}" ]] || return 0
  while IFS='=' read -r key value; do
    marker="ONEINSTACK_PARAMETER_${key}_EXPLICIT"
    case "${key}" in
      HALO_PORT) [[ "${!marker:-false}" == true ]] || halo_port="${value}" ;;
      HALO_BIND_ADDRESS) [[ "${!marker:-false}" == true ]] || bind_address="${value}" ;;
      HALO_EXTERNAL_URL) [[ "${!marker:-false}" == true ]] || external_url="${value}" ;;
      INSTALL_DIR) [[ "${!marker:-false}" == true ]] || install_dir="${value}" ;;
      DATA_DIR) [[ "${!marker:-false}" == true ]] || data_dir="${value}" ;;
      CONFIG_DIR) [[ "${!marker:-false}" == true ]] || config_dir="${value}" ;;
      RUN_USER) [[ "${!marker:-false}" == true ]] || run_user="${value}" ;;
      RUN_GROUP) [[ "${!marker:-false}" == true ]] || run_group="${value}" ;;
      JVM_XMS_MB) [[ "${!marker:-false}" == true ]] || jvm_xms_mb="${value}" ;;
      JVM_XMX_MB) [[ "${!marker:-false}" == true ]] || jvm_xmx_mb="${value}" ;;
      DATABASE_TYPE) [[ "${!marker:-false}" == true ]] || database_type="${value}" ;;
      DATABASE_HOST) [[ "${!marker:-false}" == true ]] || database_host="${value}" ;;
      DATABASE_PORT) [[ "${!marker:-false}" == true ]] || database_port="${value}" ;;
      DATABASE_NAME) [[ "${!marker:-false}" == true ]] || database_name="${value}" ;;
      DATABASE_USERNAME) [[ "${!marker:-false}" == true ]] || database_username="${value}" ;;
      USE_ABSOLUTE_PERMALINK) use_absolute_permalink="${value}" ;;
      FORWARD_HEADERS_STRATEGY) forward_headers_strategy="${value}" ;;
      COMPRESSION_ENABLED) compression_enabled="${value}" ;;
      STATIC_CACHE_MAX_AGE_DAYS) static_cache_max_age_days="${value}" ;;
      LOG_MAX_FILE_SIZE_MB) log_max_file_size_mb="${value}" ;;
      LOG_TOTAL_SIZE_CAP_MB) log_total_size_cap_mb="${value}" ;;
      LOG_MAX_HISTORY) log_max_history="${value}" ;;
    esac
  done <"${install_parameters_file}"
  config_file="${config_dir}/application.yaml"
  environment_file="${config_dir}/halo.env"
  jar_file="${install_dir}/halo.jar"
  jre_dir="${install_dir}/jre"
}
load_database_password() {
  [[ -n "${database_password}" || ! -r "${environment_file}" ]] ||
    database_password="$(sed -n 's/^HALO_DATABASE_PASSWORD=//p' "${environment_file}" | tail -n1)"
}
reject_immutable_runtime_changes() {
  local key value marker current stored_database_password
  [[ ( -f "${state_dir}/installed" || -f "${retained_data_file}" ) && -r "${install_parameters_file}" ]] || return 0
  if [[ "${ONEINSTACK_PARAMETER_DATABASE_PASSWORD_EXPLICIT:-false}" == true ]]; then
    stored_database_password="$(sed -n 's/^HALO_DATABASE_PASSWORD=//p' "${environment_file}" 2>/dev/null | tail -n1)"
    [[ -n "${stored_database_password}" && "${database_password}" == "${stored_database_password}" ]] ||
      die "IMMUTABLE_PARAMETER: DATABASE_PASSWORD cannot change during an upgrade or online configuration."
  fi
  while IFS='=' read -r key value; do
    case "${key}" in
      INSTALL_DIR) current="${install_dir}" ;;
      DATA_DIR) current="${data_dir}" ;;
      CONFIG_DIR) current="${config_dir}" ;;
      RUN_USER) current="${run_user}" ;;
      RUN_GROUP) current="${run_group}" ;;
      DATABASE_TYPE) current="${database_type}" ;;
      DATABASE_HOST) current="${database_host}" ;;
      DATABASE_PORT) current="${database_port}" ;;
      DATABASE_NAME) current="${database_name}" ;;
      DATABASE_USERNAME) current="${database_username}" ;;
      *) continue ;;
    esac
    marker="ONEINSTACK_PARAMETER_${key}_EXPLICIT"
    if [[ "${!marker:-false}" == true && "${current}" != "${value}" ]]; then
      die "IMMUTABLE_PARAMETER: ${key} cannot change after installation; reinstall to change it."
    fi
  done <"${install_parameters_file}"
}
load_persisted_parameters
load_database_password

validate_memory() {
  local total_mb
  total_mb="$(awk '/^MemTotal:/ {print int($2/1024); exit}' /proc/meminfo 2>/dev/null || true)"
  [[ "${total_mb}" =~ ^[0-9]+$ && "${total_mb}" -gt 0 ]] || return 0
  ((10#${jvm_xmx_mb} + 256 <= total_mb)) ||
    die "HOST_MEMORY_INSUFFICIENT: JVM Xmx ${jvm_xmx_mb} MB requires at least $((10#${jvm_xmx_mb} + 256)) MB total memory."
}
normalize_database_port() {
  case "${database_type}" in
    # H2 does not use a network endpoint. Normalize any generic form value
    # before immutable-parameter comparison so an irrelevant 3306/5432 value
    # cannot make a managed H2 upgrade look like a database migration.
    h2) database_port=0 ;;
    postgresql) [[ -n "${database_port}" ]] || database_port=5432 ;;
    mysql|mariadb) [[ -n "${database_port}" ]] || database_port=3306 ;;
    *) die "INVALID_PARAMETER: DATABASE_TYPE must be h2, postgresql, mysql, or mariadb." ;;
  esac
}
validate_inputs() {
  [[ "${software_version}" == 2.22.9 || "${software_version}" == 2.25.4 ]] ||
    die "SOFTWARE_VERSION_UNSUPPORTED: only Halo 2.22.9 and 2.25.4 are supported."
  [[ "${install_mode}" == center || "${install_mode}" == offline ]] ||
    die "INVALID_INSTALL_MODE: expected center or offline."
  if [[ "${install_mode}" == offline ]]; then
    [[ "${offline_bundle_digest}" =~ ^[0-9a-f]{64}$ && "${offline_bundle_id}" == "sha256:${offline_bundle_digest}" ]] ||
      die "OFFLINE_BUNDLE_IDENTITY_MISMATCH: Panel Bundle identity and digest are required and must match."
  elif [[ -n "${offline_package_path}" || -n "${offline_bundle_id}" || -n "${offline_bundle_digest}" ]]; then
    die "OFFLINE_MODE_INVALID: offline Bundle metadata cannot be supplied in Center mode."
  fi
  validate_integer_range "${halo_port}" HALO_PORT 1 65535
  validate_host_value "${bind_address}" HALO_BIND_ADDRESS
  validate_external_url
  validate_path "${state_root}" ONEINSTACK_COMPONENT_STATE
  validate_path "${install_dir}" INSTALL_DIR
  validate_path "${data_dir}" DATA_DIR
  validate_path "${config_dir}" CONFIG_DIR
  paths_overlap "${install_dir}" "${data_dir}" && die "INVALID_PARAMETER: INSTALL_DIR and DATA_DIR must not overlap."
  paths_overlap "${install_dir}" "${config_dir}" && die "INVALID_PARAMETER: INSTALL_DIR and CONFIG_DIR must not overlap."
  paths_overlap "${data_dir}" "${config_dir}" && die "INVALID_PARAMETER: DATA_DIR and CONFIG_DIR must not overlap."
  paths_overlap "${state_dir}" "${install_dir}" && die "INVALID_PARAMETER: component state and INSTALL_DIR must not overlap."
  paths_overlap "${state_dir}" "${data_dir}" && die "INVALID_PARAMETER: component state and DATA_DIR must not overlap."
  paths_overlap "${state_dir}" "${config_dir}" && die "INVALID_PARAMETER: component state and CONFIG_DIR must not overlap."
  validate_account_name "${run_user}" RUN_USER
  validate_account_name "${run_group}" RUN_GROUP
  validate_integer_range "${jvm_xms_mb}" JVM_XMS_MB 64 1048576
  validate_integer_range "${jvm_xmx_mb}" JVM_XMX_MB 64 1048576
  ((10#${jvm_xmx_mb} >= 10#${jvm_xms_mb})) || die "INVALID_PARAMETER: JVM_XMX_MB must be greater than or equal to JVM_XMS_MB."
  normalize_database_port
  if [[ "${database_type}" != h2 ]]; then
    validate_host_value "${database_host}" DATABASE_HOST
    validate_integer_range "${database_port}" DATABASE_PORT 1 65535
    validate_identifier "${database_name}" DATABASE_NAME
    validate_identifier "${database_username}" DATABASE_USERNAME
    [[ -n "${database_password}" ]] || die "DATABASE_PASSWORD_REQUIRED: external database credentials require DATABASE_PASSWORD."
  fi
  [[ -z "${database_password}" || ( "${database_password}" =~ ^[A-Za-z0-9._@%+=!#?-]{8,128}$ ) ]] ||
    die "INVALID_PARAMETER: DATABASE_PASSWORD must be 8-128 safe printable characters."
  validate_boolean "${use_absolute_permalink}" USE_ABSOLUTE_PERMALINK
  validate_boolean "${compression_enabled}" COMPRESSION_ENABLED
  validate_boolean "${adopt_legacy_data}" ADOPT_LEGACY_DATA
  [[ "${forward_headers_strategy}" == native || "${forward_headers_strategy}" == framework || "${forward_headers_strategy}" == none ]] ||
    die "INVALID_PARAMETER: FORWARD_HEADERS_STRATEGY must be native, framework, or none."
  validate_integer_range "${static_cache_max_age_days}" STATIC_CACHE_MAX_AGE_DAYS 0 3650
  validate_integer_range "${log_max_file_size_mb}" LOG_MAX_FILE_SIZE_MB 1 1024
  validate_integer_range "${log_total_size_cap_mb}" LOG_TOTAL_SIZE_CAP_MB 10 1048576
  validate_integer_range "${log_max_history}" LOG_MAX_HISTORY 0 3650
  validate_memory
  reject_immutable_runtime_changes
}

detect_host() {
  [[ -r /etc/os-release ]] || die "HOST_PLATFORM_UNSUPPORTED: /etc/os-release is unavailable."
  # shellcheck disable=SC1091
  source /etc/os-release
  system_id="${ID,,}"; system_version="${VERSION_ID:-}"
  if [[ "${system_id}" == centos ]] && grep -Eiq 'CentOS[[:space:]]+Stream' /etc/os-release; then
    system_id="centos-stream"
  fi
  case "${system_id}:${system_version}" in
    ubuntu:20.04|ubuntu:22.04|ubuntu:24.04|ubuntu:26.04) ;;
    debian:11|debian:12|debian:13) ;;
    rocky:*|almalinux:*)
      case "${system_version%%.*}" in 8|9) ;; *) die "HOST_PLATFORM_UNSUPPORTED: unsupported Enterprise Linux ${system_version}." ;; esac ;;
    centos:7*) ;;
    *) die "HOST_PLATFORM_UNSUPPORTED: unsupported Linux platform ${system_id:-unknown} ${system_version:-unknown}." ;;
  esac
  host_arch="$(architecture_name)"
  case "${system_id}" in
    ubuntu|debian) command -v apt-get >/dev/null 2>&1 || die "HOST_DEPENDENCY_UNSUPPORTED: apt is required."; package_manager=apt ;;
    centos) command -v yum >/dev/null 2>&1 || die "HOST_DEPENDENCY_UNSUPPORTED: yum is required on CentOS 7."; package_manager=yum ;;
    anolis|euleros)
      if command -v dnf >/dev/null 2>&1; then package_manager=dnf
      elif command -v yum >/dev/null 2>&1; then package_manager=yum
      else die "HOST_DEPENDENCY_UNSUPPORTED: dnf or yum is required."; fi
      ;;
    sles|opensuse|opensuse-leap|opensuse-tumbleweed) command -v zypper >/dev/null 2>&1 || die "HOST_DEPENDENCY_UNSUPPORTED: zypper is required."; package_manager=zypper ;;
    *) command -v dnf >/dev/null 2>&1 || die "HOST_DEPENDENCY_UNSUPPORTED: dnf is required."; package_manager=dnf ;;
  esac
}
select_artifacts() {
  [[ -n "${host_arch}" ]] || host_arch="$(architecture_name)"
  jar_url="https://dl.halo.run/release/halo-${software_version}.jar"
  case "${software_version}" in
    2.22.9) jar_sha256="c6a12f67c62bf9e57f284364d797e9cab5c7cdb586f70fa128b240893ff4dbff" ;;
    2.25.4) jar_sha256="537756c406ff013db34d47a3740093ed2cd1cbdedf5b3da34ca8017de61aa04e" ;;
    *) die "SOFTWARE_VERSION_UNSUPPORTED: no artifact for Halo ${software_version}." ;;
  esac
  case "${host_arch}" in
    amd64)
      jre_archive="OpenJDK21U-jre_x64_linux_hotspot_21.0.12_8.tar.gz"
      jre_sha256="8a379a67c91a3ae61ffb33d46e0a40c7ba35e70713c4db31cfca30492f792eff"
      jre_signature_sha256="60331db9e224e50067fb67cc7348da16dd769a2cdcac60ca6f78dbe7dc6df1fe"
      ;;
    arm64)
      jre_archive="OpenJDK21U-jre_aarch64_linux_hotspot_21.0.12_8.tar.gz"
      jre_sha256="5f9c96b656827b9d14ebeda7739e25be554fa6d25669b03847c1df6e869c0679"
      jre_signature_sha256="bb9e026fa0ae867a689d9d5c50160f6a3b4891f4c04faf4bfa9817c5b994b9be"
      ;;
    *) die "HOST_ARCH_UNSUPPORTED: no Temurin runtime for ${host_arch}." ;;
  esac
  jre_url="https://github.com/adoptium/temurin21-binaries/releases/download/jdk-21.0.12%2B8/${jre_archive}"
  jre_signature_url="${jre_url}.sig"
}
verify_checksum() {
  local file="$1" expected="$2" actual
  [[ -f "${file}" ]] || die "ARTIFACT_MISSING: ${file}."
  actual="$(sha256sum "${file}" | awk '{print $1}')"
  [[ "${actual}" == "${expected}" ]] || die "ARTIFACT_CHECKSUM_MISMATCH: $(basename -- "${file}")."
}
verify_signature() {
  local artifact="$1" signature="$2" key="$3" home status imported gpg_bin
  gpg_bin="$(gpg_command)"
  home="$(mktemp -d "${TMPDIR:-/tmp}/oneinstack-halo-gpg.XXXXXX")"
  chmod 0700 "${home}"
  status="${home}/status"
  if ! "${gpg_bin}" --batch --homedir "${home}" --status-fd 1 --import "${key}" >"${status}" 2>/dev/null; then
    rm -rf -- "${home}"; die "SIGNING_KEY_INVALID: cannot import the Adoptium key."
  fi
  imported="$(awk '$1 == "[GNUPG:]" && $2 == "IMPORT_OK" {print toupper($4)}' "${status}" | tail -n1)"
  [[ "${imported}" == "${adoptium_key_fingerprint}" ]] || { rm -rf -- "${home}"; die "SIGNING_KEY_MISMATCH: unexpected Adoptium fingerprint."; }
  if ! "${gpg_bin}" --batch --homedir "${home}" --status-fd 1 --verify "${signature}" "${artifact}" >"${status}" 2>/dev/null; then
    rm -rf -- "${home}"; die "ARTIFACT_SIGNATURE_INVALID: $(basename -- "${artifact}")."
  fi
  grep -Eq "^\[GNUPG:\] VALIDSIG ${adoptium_key_fingerprint}([[:space:]]|$)" "${status}" || {
    rm -rf -- "${home}"; die "ARTIFACT_SIGNATURE_MISMATCH: unexpected JRE signer."
  }
  rm -rf -- "${home}"
}

expected_bundle_id() { printf 'halo:%s:%s:%s:%s:%s\n' "${package_version}" "${software_version}" "${system_id}" "${system_version}" "${host_arch}"; }
bundle_value() { sed -n "s/^$1=//p" "${offline_package_path}/bundle-info" | tail -n1; }
validate_offline_bundle() {
  [[ "${install_mode}" == offline ]] || return 0
  validate_path "${offline_package_path}" ONEINSTACK_OFFLINE_PACKAGE_PATH
  [[ -f "${offline_package_path}/files.sha256" && -f "${offline_package_path}/bundle-info" && -f "${offline_package_path}/manifest.yaml" ]] ||
    die "OFFLINE_BUNDLE_INVALID: manifest, bundle-info, or files.sha256 is missing."
  find "${offline_package_path}" -mindepth 1 \( -type l -o \( ! -type f ! -type d \) \) -print -quit | grep -q . &&
    die "OFFLINE_BUNDLE_INVALID: symbolic links and special files are forbidden."
  local listed actual
  listed="$(awk '{print $2}' "${offline_package_path}/files.sha256" | sed 's|^\*\?||' | LC_ALL=C sort)"
  actual="$(cd -- "${offline_package_path}" && find . -type f ! -name files.sha256 -print | sed 's|^./||' | LC_ALL=C sort)"
  [[ "${listed}" == "${actual}" ]] || die "OFFLINE_BUNDLE_INVALID: files.sha256 is not an exact file inventory."
  (cd -- "${offline_package_path}" && sha256sum --check --strict files.sha256 >/dev/null) ||
    die "OFFLINE_BUNDLE_CHECKSUM_MISMATCH: files.sha256 validation failed."
  [[ "$(bundle_value component)" == halo && "$(bundle_value packageVersion)" == "${package_version}" &&
     "$(bundle_value softwareVersion)" == "${software_version}" && "$(bundle_value osId)" == "${system_id}" &&
     "$(bundle_value osVersion)" == "${system_version}" && "$(bundle_value architecture)" == "${host_arch}" &&
     "$(bundle_value haloJarSha256)" == "${jar_sha256}" && "$(bundle_value jreVersion)" == "${jre_version}" &&
     "$(bundle_value jreArchive)" == "${jre_archive}" && "$(bundle_value jreSha256)" == "${jre_sha256}" &&
     "$(bundle_value bundleId)" == "$(expected_bundle_id)" &&
     "$(bundle_value jreSignatureSha256)" == "${jre_signature_sha256}" &&
     "$(bundle_value jrePublisherFingerprint)" == "${adoptium_key_fingerprint}" ]] ||
    die "OFFLINE_BUNDLE_IDENTITY_MISMATCH: Bundle does not match this component, host, version, and architecture."
  local manifest_package_version
  manifest_package_version="$(sed -nE 's/^[[:space:]]+version:[[:space:]]+"?([^"[:space:]]+)"?[[:space:]]*$/\1/p' "${offline_package_path}/manifest.yaml" | head -n1)"
  [[ "${manifest_package_version}" == "${package_version}" ]] ||
    die "OFFLINE_BUNDLE_PACKAGE_VERSION_MISMATCH: expected Halo package ${package_version}."
  verify_checksum "${offline_package_path}/artifacts/common/halo-${software_version}.jar" "${jar_sha256}"
  verify_checksum "${offline_package_path}/artifacts/${host_arch}/${jre_archive}" "${jre_sha256}"
  verify_checksum "${offline_package_path}/artifacts/${host_arch}/${jre_archive}.sig" "${jre_signature_sha256}"
  verify_checksum "${offline_package_path}/keys/adoptium-release-key.asc" "${adoptium_key_sha256}"
  [[ -f "${offline_package_path}/artifacts/${host_arch}/${jre_archive}.sig" ]] || die "OFFLINE_BUNDLE_INVALID: JRE signature is missing."
  find "${offline_package_path}/packages/${system_id}/${system_version}/${host_arch}" -maxdepth 1 -type f \( -name '*.deb' -o -name '*.rpm' \) -print -quit | grep -q . ||
    die "OFFLINE_DEPENDENCY_MISSING: no exact host dependency closure is present."
}
install_dependencies() {
  local package_dir_path
  if [[ "${install_mode}" == offline ]]; then
    package_dir_path="${offline_package_path}/packages/${system_id}/${system_version}/${host_arch}"
    local -a packages=()
    case "${package_manager}" in
      apt)
        mapfile -t packages < <(find "${package_dir_path}" -maxdepth 1 -type f -name '*.deb' -print | sort)
        ((${#packages[@]} > 0)) || die "OFFLINE_DEPENDENCY_MISSING: no DEB packages found."
        dpkg -i -- "${packages[@]}" || DEBIAN_FRONTEND=noninteractive apt-get --no-download -y -f install
        ;;
      dnf)
        mapfile -t packages < <(find "${package_dir_path}" -maxdepth 1 -type f -name '*.rpm' -print | sort)
        ((${#packages[@]} > 0)) || die "OFFLINE_DEPENDENCY_MISSING: no RPM packages found."
        dnf -y --disablerepo='*' install "${packages[@]}"
        ;;
      yum)
        mapfile -t packages < <(find "${package_dir_path}" -maxdepth 1 -type f -name '*.rpm' -print | sort)
        ((${#packages[@]} > 0)) || die "OFFLINE_DEPENDENCY_MISSING: no RPM packages found."
        yum -y --disablerepo='*' localinstall "${packages[@]}"
        ;;
      zypper)
        mapfile -t packages < <(find "${package_dir_path}" -maxdepth 1 -type f -name '*.rpm' -print | sort)
        ((${#packages[@]} > 0)) || die "OFFLINE_DEPENDENCY_MISSING: no RPM packages found."
        rpm -Uvh --replacepkgs "${packages[@]}"
        ;;
    esac
    return 0
  fi
  case "${package_manager}" in
    apt) apt-get update; DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends acl ca-certificates coreutils curl findutils gnupg gzip iproute2 passwd procps tar util-linux ;;
    dnf) dnf install -y acl ca-certificates coreutils curl findutils gnupg2 gzip iproute procps-ng shadow-utils tar util-linux ;;
    yum) yum install -y acl ca-certificates coreutils curl findutils gnupg2 gzip iproute procps-ng shadow-utils tar util-linux ;;
    zypper) zypper --non-interactive install --no-recommends acl ca-certificates coreutils curl findutils gpg2 gzip iproute2 procps shadow tar util-linux ;;
  esac
}
download_file() {
  local url="$1" target="$2"
  require_command curl
  if [[ ! -f "${target}" ]]; then
    curl --proto '=https' --tlsv1.2 --fail --location --retry 3 --connect-timeout 20 --output "${target}.part" "${url}"
    mv -f -- "${target}.part" "${target}"
  fi
}
prepare_artifacts() {
  local cache_dir jar_source jre_source signature_source key_source
  if [[ "${install_mode}" == offline ]]; then
    jar_source="${offline_package_path}/artifacts/common/halo-${software_version}.jar"
    jre_source="${offline_package_path}/artifacts/${host_arch}/${jre_archive}"
    signature_source="${jre_source}.sig"
    key_source="${offline_package_path}/keys/adoptium-release-key.asc"
  else
    cache_dir="${state_root}/downloads/halo-${package_version}"
    install -d -m 0750 -- "${cache_dir}"
    jar_source="${cache_dir}/halo-${software_version}.jar"
    jre_source="${cache_dir}/${jre_archive}"
    signature_source="${jre_source}.sig"
    key_source="${cache_dir}/adoptium-release-key.asc"
    download_file "${jar_url}" "${jar_source}"
    download_file "${jre_url}" "${jre_source}"
    download_file "${jre_signature_url}" "${signature_source}"
    download_file "${adoptium_key_url}" "${key_source}"
  fi
  verify_checksum "${jar_source}" "${jar_sha256}"
  verify_checksum "${jre_source}" "${jre_sha256}"
  verify_checksum "${signature_source}" "${jre_signature_sha256}"
  verify_checksum "${key_source}" "${adoptium_key_sha256}"
  verify_signature "${jre_source}" "${signature_source}" "${key_source}"
  printf '%s\n%s\n%s\n%s\n' "${jar_source}" "${jre_source}" "${signature_source}" "${key_source}"
}

service_is_active() { has_systemd && systemctl is-active --quiet "${service_name}.service" 2>/dev/null; }
service_start() {
  systemctl daemon-reload
  systemctl enable "${service_name}.service"
  systemctl start "${service_name}.service"
}
service_stop() { systemctl stop "${service_name}.service" 2>/dev/null || true; }
service_restart() { systemctl daemon-reload; systemctl restart "${service_name}.service"; }
port_is_listening() {
  local port="$1"
  if command -v ss >/dev/null 2>&1; then ss -ltn 2>/dev/null | awk -v port=":${port}" 'NR > 1 && $4 ~ port "$" {found=1} END {exit !found}'
  elif command -v netstat >/dev/null 2>&1; then netstat -ltn 2>/dev/null | awk -v port=":${port}" '$4 ~ port "$" {found=1} END {exit !found}'
  else return 1; fi
}
managed_service_owns_port() {
  local port="$1" main_pid listeners line
  service_is_active || return 1
  main_pid="$(systemctl show "${service_name}.service" --property=MainPID --no-pager 2>/dev/null | sed -n 's/^MainPID=//p' | head -n1 || true)"
  [[ "${main_pid}" =~ ^[1-9][0-9]*$ ]] || return 1
  if command -v ss >/dev/null 2>&1; then
    listeners="$(ss -ltnp 2>/dev/null | awk -v port=":${port}" 'NR > 1 && $4 ~ port "$" {print}' || true)"
    [[ -n "${listeners}" ]] || return 1
    while IFS= read -r line; do
      [[ "${line}" == *"pid=${main_pid},"* ]] || return 1
    done <<<"${listeners}"
    return 0
  fi
  if command -v netstat >/dev/null 2>&1; then
    listeners="$(netstat -ltnp 2>/dev/null | awk -v port=":${port}" '$4 ~ port "$" {print}')"
    [[ -n "${listeners}" ]] || return 1
    while IFS= read -r line; do
      [[ "${line}" == *" ${main_pid}/"* ]] || return 1
    done <<<"${listeners}"
    return 0
  fi
  return 1
}
managed_installation_present() {
  [[ -f "${state_dir}/installed" && -f "${state_dir}/ownership" && "$(cat "${state_dir}/ownership" 2>/dev/null)" == managed && -f "${unit_file}" ]] &&
    { managed_unit_matches || legacy_managed_installation; }
}
managed_unit_matches() {
  [[ -f "${unit_file}" ]] || return 1
  grep -Fq "ExecStart=${jre_dir}/bin/java" "${unit_file}" &&
    grep -Fq " -jar ${jar_file} " "${unit_file}" &&
    grep -Fxq "User=${run_user}" "${unit_file}" &&
    grep -Fxq "Group=${run_group}" "${unit_file}" &&
    grep -Fxq "WorkingDirectory=${data_dir}" "${unit_file}" &&
    grep -Fxq "EnvironmentFile=${environment_file}" "${unit_file}"
}
legacy_managed_installation() {
  [[ -f "${state_dir}/installed" && -f "${unit_file}" ]] || return 1
  grep -Eq '^User=root$' "${unit_file}" && grep -Eq '^ExecStart=.*halo\.jar' "${unit_file}"
}
legacy_r2dbc_value() {
  local key="$1" legacy_config="${legacy_data_dir}/application.yaml"
  [[ -r "${legacy_config}" ]] || return 0
  awk -v key="${key}" '
    {
      line=$0
      match(line, /^[[:space:]]*/)
      indent=RLENGTH
      content=substr(line, indent + 1)
      if (!in_r2dbc) {
        if (content == "r2dbc:") { in_r2dbc=1; base_indent=indent }
        next
      }
      if (content == "" || content ~ /^#/) next
      if (indent <= base_indent) exit
      if (content ~ ("^" key ":[[:space:]]*")) {
        sub("^" key ":[[:space:]]*", "", content)
        sub(/[[:space:]]+#.*$/, "", content)
        gsub(/^[[:space:]\047\"]+|[[:space:]\047\"]+$/, "", content)
        print content
        exit
      }
    }
  ' "${legacy_config}"
}
legacy_database_platform() {
  local legacy_config="${legacy_data_dir}/application.yaml" platform
  platform="$(sed -nE 's/^[[:space:]]+platform:[[:space:]]*([^[:space:]#]+).*$/\1/p' "${legacy_config}" 2>/dev/null | tr -d "\"'" | tail -n1)"
  if [[ -z "${platform}" && -s "${legacy_data_dir}/db/halo-next.mv.db" ]]; then platform=h2; fi
  printf '%s' "${platform}"
}
legacy_unmanaged_adoption() {
  [[ "${adopt_legacy_data}" == true && ! -f "${state_dir}/installed" && ! -f "${retained_data_file}" && -d "${legacy_data_dir}" ]]
}
legacy_migration_requested() { legacy_managed_installation || legacy_unmanaged_adoption; }
legacy_container_exists() {
  local runtime output
  for runtime in docker podman; do
    command -v "${runtime}" >/dev/null 2>&1 || continue
    output="$("${runtime}" ps -a --no-trunc --format '{{.Image}} {{.Names}} {{.Mounts}}' 2>/dev/null || true)"
    grep -Eiq '(^|[[:space:]/])halo([:@/[:space:]]|$)|/root/\.halo2' <<<"${output}" && return 0
  done
  return 1
}
validate_legacy_snapshot_capacity() {
  local legacy_kb available_kb required_kb probe_path
  legacy_kb="$(du -sk -- "${legacy_data_dir}" | awk '{print $1}')"
  probe_path="${state_root}"
  while [[ ! -d "${probe_path}" && "${probe_path}" != / ]]; do probe_path="$(dirname -- "${probe_path}")"; done
  available_kb="$(df -Pk -- "${probe_path}" | awk 'NR == 2 {print $4}')"
  [[ "${legacy_kb}" =~ ^[0-9]+$ && "${available_kb}" =~ ^[0-9]+$ ]] ||
    die "LEGACY_SNAPSHOT_CAPACITY_UNKNOWN: cannot determine space required for the legacy snapshot."
  required_kb=$((legacy_kb * 2 + 65536))
  ((available_kb >= required_kb)) ||
    die "LEGACY_SNAPSHOT_SPACE_INSUFFICIENT: migration requires at least ${required_kb} KiB free for snapshot and rollback."
}
validate_unmanaged_legacy_adoption() {
  local legacy_platform legacy_username legacy_password foreign_entry parameter marker
  [[ "${software_version}" == 2.25.4 ]] ||
    die "LEGACY_MIGRATION_TARGET_REQUIRED: unmanaged /root/.halo2 data can only be migrated to Halo 2.25.4."
  [[ -d "${legacy_data_dir}" && ! -L "${legacy_data_dir}" ]] ||
    die "LEGACY_DATA_INVALID: /root/.halo2 must be a real directory."
  foreign_entry="$(find "${legacy_data_dir}" -type l -print -quit 2>/dev/null || true)"
  [[ -z "${foreign_entry}" ]] || die "LEGACY_DATA_UNSAFE: symbolic links are not allowed in /root/.halo2 (${foreign_entry})."
  foreign_entry="$(find "${legacy_data_dir}" ! -uid 0 -print -quit 2>/dev/null || true)"
  [[ -z "${foreign_entry}" ]] || die "LEGACY_DATA_OWNERSHIP_UNSAFE: /root/.halo2 contains a non-root-owned entry (${foreign_entry})."
  if command -v findmnt >/dev/null 2>&1 && findmnt -rn -R "${legacy_data_dir}" 2>/dev/null | grep -q .; then
    die "LEGACY_DATA_MOUNT_CONFLICT: /root/.halo2 contains or is a mount point; detach the old runtime before migration."
  fi
  if command -v pgrep >/dev/null 2>&1 && pgrep -af 'java.*halo|halo.*\.jar' >/dev/null 2>&1; then
    die "LEGACY_RUNTIME_ACTIVE: stop the existing Halo Java process before migration."
  fi
  legacy_container_exists && die "LEGACY_CONTAINER_CONFLICT: remove or rename the existing Halo container before migration."
  legacy_platform="$(legacy_database_platform)"
  case "${legacy_platform}" in
    h2)
      [[ "${database_type}" == h2 ]] || die "LEGACY_DATABASE_TYPE_MISMATCH: legacy data uses h2, requested ${database_type}."
      [[ -s "${legacy_data_dir}/db/halo-next.mv.db" && ! -L "${legacy_data_dir}/db/halo-next.mv.db" ]] ||
        die "LEGACY_H2_DATABASE_MISSING: /root/.halo2/db/halo-next.mv.db is missing or empty."
      legacy_username="$(legacy_r2dbc_value username)"
      [[ -z "${legacy_username}" || "${legacy_username}" == admin ]] ||
        die "LEGACY_H2_USERNAME_UNSUPPORTED: expected the Halo H2 user admin."
      if [[ -z "${database_password}" ]]; then
        legacy_password="$(legacy_r2dbc_value password)"
        database_password="${legacy_password:-123456}"
      fi
      [[ "${database_password}" =~ ^[A-Za-z0-9._@%+=!#?-]{6,128}$ ]] ||
        die "LEGACY_H2_PASSWORD_UNSUPPORTED: provide the existing H2 password using database-password."
      ;;
    postgresql|mysql|mariadb)
      [[ "${database_type}" == "${legacy_platform}" ]] ||
        die "LEGACY_DATABASE_TYPE_MISMATCH: requested ${database_type} does not match legacy ${legacy_platform}."
      for parameter in DATABASE_TYPE DATABASE_HOST DATABASE_PORT DATABASE_NAME DATABASE_USERNAME DATABASE_PASSWORD; do
        marker="ONEINSTACK_PARAMETER_${parameter}_EXPLICIT"
        [[ "${!marker:-false}" == true ]] ||
          die "LEGACY_DATABASE_PARAMETERS_REQUIRED: explicitly provide all external database connection parameters."
      done
      ;;
    *) die "LEGACY_DATABASE_UNSUPPORTED: cannot identify the database used by /root/.halo2." ;;
  esac
  validate_legacy_snapshot_capacity
}
validate_legacy_configuration() {
  local legacy_platform legacy_password parameter marker
  legacy_migration_requested && [[ -d "${legacy_data_dir}" ]] || return 0
  legacy_platform="$(legacy_database_platform)"
  case "${legacy_platform}" in
    h2)
      [[ "${database_type}" == h2 ]] ||
        die "LEGACY_DATABASE_TYPE_MISMATCH: legacy data uses h2, requested ${database_type}."
      if [[ -z "${database_password}" ]]; then
        legacy_password="$(legacy_r2dbc_value password)"
        database_password="${legacy_password:-123456}"
      fi
      [[ "${database_password}" =~ ^[A-Za-z0-9._@%+=!#?-]{6,128}$ ]] ||
        die "LEGACY_H2_PASSWORD_UNSUPPORTED: provide the existing H2 password using database-password."
      ;;
    postgresql|mysql|mariadb)
      [[ "${database_type}" == "${legacy_platform}" ]] ||
        die "LEGACY_DATABASE_TYPE_MISMATCH: requested ${database_type} does not match legacy ${legacy_platform}."
      for parameter in DATABASE_TYPE DATABASE_HOST DATABASE_PORT DATABASE_NAME DATABASE_USERNAME DATABASE_PASSWORD; do
        marker="ONEINSTACK_PARAMETER_${parameter}_EXPLICIT"
        [[ "${!marker:-false}" == true ]] ||
          die "LEGACY_DATABASE_PARAMETERS_REQUIRED: legacy ${legacy_platform} configuration requires explicit database type, host, port, name, username, and password."
      done
      ;;
    *) die "LEGACY_DATABASE_UNSUPPORTED: cannot safely migrate database platform ${legacy_platform}." ;;
  esac
}
check_existing_ownership() {
  local path managed_port=8090
  if [[ -f "${unit_file}" ]] && ! managed_unit_matches && ! legacy_managed_installation; then
    die "UNMANAGED_CONFLICT: ${unit_file} is not owned by this Halo component."
  fi
  if [[ -f "${state_dir}/installed" ]] && ! managed_installation_present && ! legacy_managed_installation; then
    die "MANAGED_STATE_CONFLICT: Halo ownership state or unit identity is incomplete."
  fi
  if [[ ! -f "${state_dir}/installed" && ! -f "${retained_data_file}" ]]; then
    for path in "${install_dir}" "${data_dir}" "${config_dir}"; do
      [[ ! -e "${path}" ]] || { [[ -d "${path}" && -z "$(find "${path}" -mindepth 1 -maxdepth 1 -print -quit 2>/dev/null)" ]] || die "UNMANAGED_CONFLICT: refusing to adopt non-empty ${path}."; }
    done
    if [[ -e "${legacy_data_dir}" ]]; then
      [[ "${adopt_legacy_data}" == true ]] ||
        die "UNMANAGED_CONFLICT: ${legacy_data_dir} exists without managed Halo state; set adopt-legacy-data=true to validate, snapshot, and migrate it."
      validate_unmanaged_legacy_adoption
    fi
  elif [[ -f "${retained_data_file}" && ! -f "${state_dir}/installed" ]]; then
    [[ ! -e "${install_dir}" ]] || { [[ -d "${install_dir}" && -z "$(find "${install_dir}" -mindepth 1 -maxdepth 1 -print -quit 2>/dev/null)" ]] || die "UNMANAGED_CONFLICT: retained-data recovery cannot adopt ${install_dir}."; }
  fi
  if [[ -f "${state_dir}/version" ]]; then
    local installed_version
    installed_version="$(cat "${state_dir}/version")"
    [[ "${installed_version}" != 2.25.4 || "${software_version}" != 2.22.9 ]] || die "DOWNGRADE_FORBIDDEN: Halo 2.25.4 cannot be downgraded to 2.22.9."
  fi
  validate_legacy_configuration
  if [[ -r "${install_parameters_file}" ]]; then
    managed_port="$(sed -n 's/^HALO_PORT=//p' "${install_parameters_file}" | tail -n1)"
    [[ "${managed_port}" =~ ^[0-9]+$ ]] || managed_port=8090
  fi
  if port_is_listening "${halo_port}"; then
    if [[ "${halo_port}" != "${managed_port}" ]] || ! managed_service_owns_port "${halo_port}"; then
      die "PORT_CONFLICT: TCP port ${halo_port} is already occupied by another process."
    fi
  fi
}
snapshot_transaction() {
  rm -rf -- "${rollback_dir}"
  install -d -m 0700 -- "${rollback_dir}"
  service_is_active && printf 'active\n' >"${rollback_dir}/was-active" || true
  service_stop
  [[ ! -e "${install_dir}" ]] || cp -a -- "${install_dir}" "${rollback_dir}/install"
  [[ ! -e "${data_dir}" ]] || cp -a -- "${data_dir}" "${rollback_dir}/data"
  [[ ! -e "${config_dir}" ]] || cp -a -- "${config_dir}" "${rollback_dir}/config"
  [[ ! -f "${unit_file}" ]] || cp -a -- "${unit_file}" "${rollback_dir}/halo.service"
  [[ ! -f "${install_parameters_file}" ]] || cp -a -- "${install_parameters_file}" "${rollback_dir}/install-parameters"
  [[ ! -f "${managed_acl_file}" ]] || cp -a -- "${managed_acl_file}" "${rollback_dir}/managed-path-acl"
  if legacy_migration_requested && [[ -d "${legacy_data_dir}" ]]; then
    cp -a -- "${legacy_data_dir}" "${rollback_dir}/legacy-data"
    printf 'true\n' >"${rollback_dir}/migrate-legacy"
    if [[ "${database_type}" == h2 && -n "${database_password}" ]]; then
      printf '%s\n' "${database_password}" >"${rollback_dir}/legacy-h2-password"
      chmod 0600 "${rollback_dir}/legacy-h2-password"
    fi
  fi
  printf '%s\n' "${database_type}" >"${rollback_dir}/database-type"
}
restore_path_snapshot() {
  local live="$1" backup="$2"
  rm -rf -- "${live}"
  [[ ! -e "${backup}" ]] || cp -a -- "${backup}" "${live}"
}
remove_transaction_acls() {
  [[ -f "${transaction_acl_file}" ]] || return 0
  restore_acl_records "${transaction_acl_file}"
}
restore_transaction() {
  local database_before
  database_before="$(cat "${rollback_dir}/database-type" 2>/dev/null || printf unknown)"
  service_stop
  remove_transaction_acls
  restore_path_snapshot "${install_dir}" "${rollback_dir}/install"
  restore_path_snapshot "${data_dir}" "${rollback_dir}/data"
  restore_path_snapshot "${config_dir}" "${rollback_dir}/config"
  rm -f -- "${unit_file}"
  [[ ! -f "${rollback_dir}/halo.service" ]] || cp -a -- "${rollback_dir}/halo.service" "${unit_file}"
  rm -f -- "${install_parameters_file}" "${managed_acl_file}"
  [[ ! -f "${rollback_dir}/install-parameters" ]] || cp -a -- "${rollback_dir}/install-parameters" "${install_parameters_file}"
  [[ ! -f "${rollback_dir}/managed-path-acl" ]] || cp -a -- "${rollback_dir}/managed-path-acl" "${managed_acl_file}"
  if [[ -d "${rollback_dir}/legacy-data" ]]; then
    rm -rf -- "${legacy_data_dir}"
    cp -a -- "${rollback_dir}/legacy-data" "${legacy_data_dir}"
  fi
  systemctl daemon-reload 2>/dev/null || true
  [[ ! -f "${rollback_dir}/was-active" ]] || systemctl start "${service_name}.service" 2>/dev/null || true
  if [[ -f "${rollback_dir}/created-user-current" ]]; then
    userdel "${run_user}" 2>/dev/null || true
    rm -f -- "${state_dir}/created-user"
  fi
  if [[ -f "${rollback_dir}/created-group-current" ]]; then
    groupdel "${run_group}" 2>/dev/null || true
    rm -f -- "${state_dir}/created-group"
  fi
  [[ "${database_before}" == h2 || "${database_before}" == unknown ]] ||
    printf 'WARNING: external database schema changes are outside automatic rollback scope.\n' >&2
}

ensure_runtime_account() {
  if getent group "${run_group}" >/dev/null 2>&1; then
    [[ "$(getent group "${run_group}" | awk -F: '{print $3}')" -ne 0 ]] || die "RUN_GROUP_INVALID: ${run_group} resolves to root."
  else
    groupadd --system "${run_group}"
    printf '%s\n' "${run_group}" >"${state_dir}/created-group"
    printf '%s\n' "${run_group}" >"${rollback_dir}/created-group-current"
  fi
  if id "${run_user}" >/dev/null 2>&1; then
    [[ "$(id -u "${run_user}")" -ne 0 ]] || die "RUN_USER_INVALID: ${run_user} resolves to root."
  else
    useradd --system --gid "${run_group}" --home-dir "${data_dir}" --no-create-home --shell /usr/sbin/nologin "${run_user}"
    printf '%s\n' "${run_user}" >"${state_dir}/created-user"
    printf '%s\n' "${run_user}" >"${rollback_dir}/created-user-current"
  fi
}
ensure_parent_traversal() {
  local target="$1" parent existing original_mask
  require_command getfacl; require_command setfacl
  parent="$(dirname -- "${target}")"
  while [[ "${parent}" != / ]]; do
    if runuser -u "${run_user}" -- test -x "${parent}" 2>/dev/null; then :
    else
      existing="$(getfacl -cp -- "${parent}" 2>/dev/null | sed -n "s/^user:${run_user}://p" | head -n1)"
      [[ -z "${existing}" ]] || die "PATH_ACL_CONFLICT: ${parent} has an existing non-traversable ACL for ${run_user}."
      original_mask="$(getfacl -cp -- "${parent}" 2>/dev/null | sed -n 's/^mask:://p' | head -n1)"
      setfacl -m "u:${run_user}:--x" -- "${parent}"
      printf '%s\t%s\n' "${parent}" "${original_mask:--}" >>"${transaction_acl_file}"
    fi
    parent="$(dirname -- "${parent}")"
  done
}
commit_transaction_acls() {
  [[ -s "${transaction_acl_file}" ]] || return 0
  touch "${managed_acl_file}"
  while IFS= read -r acl_record; do grep -Fxq -- "${acl_record}" "${managed_acl_file}" 2>/dev/null || printf '%s\n' "${acl_record}" >>"${managed_acl_file}"; done <"${transaction_acl_file}"
  sort -u -o "${managed_acl_file}" "${managed_acl_file}"
}
remove_managed_acls() {
  [[ -r "${managed_acl_file}" ]] || return 0
  restore_acl_records "${managed_acl_file}"
}
restore_acl_records() {
  local records="$1" parent original_mask
  [[ -r "${records}" ]] || return 0
  while IFS=$'\t' read -r parent original_mask; do
    [[ -d "${parent}" ]] || continue
    setfacl -x "u:${run_user}" -- "${parent}" 2>/dev/null || true
    if [[ "${original_mask}" == - || -z "${original_mask}" ]]; then
      setfacl -b -- "${parent}" 2>/dev/null || true
    else
      setfacl -m "m::${original_mask}" -- "${parent}" 2>/dev/null || true
    fi
  done <"${records}"
}
prepare_runtime_directories() {
  install -d -m 0750 -o root -g "${run_group}" -- "${install_dir}"
  install -d -m 0750 -o "${run_user}" -g "${run_group}" -- "${data_dir}"
  install -d -m 0750 -o root -g "${run_group}" -- "${config_dir}"
  : >"${transaction_acl_file}"
  ensure_parent_traversal "${install_dir}"
  ensure_parent_traversal "${data_dir}"
  ensure_parent_traversal "${config_dir}"
}
install_runtime_artifacts() {
  local jar_source="$1" jre_source="$2" temp extracted
  tar -tzf "${jre_source}" | awk 'BEGIN {bad=0} /^\// || /(^|\/)\.\.($|\/)/ {bad=1} END {exit bad}' ||
    die "ARTIFACT_LAYOUT_INVALID: JRE archive contains an unsafe path."
  temp="$(mktemp -d "${TMPDIR:-/tmp}/oneinstack-halo-jre.XXXXXX")"
  tar -xzf "${jre_source}" -C "${temp}" --no-same-owner --no-same-permissions
  extracted="$(find "${temp}" -mindepth 2 -maxdepth 3 -type f -path '*/bin/java' -print -quit)"
  [[ -n "${extracted}" ]] || { rm -rf -- "${temp}"; die "ARTIFACT_LAYOUT_INVALID: Temurin bin/java is missing."; }
  extracted="$(dirname -- "$(dirname -- "${extracted}")")"
  rm -rf -- "${jre_dir}"
  mv -- "${extracted}" "${jre_dir}"
  rm -rf -- "${temp}"
  install -m 0640 -o root -g "${run_group}" -- "${jar_source}" "${jar_file}"
  chown -R root:"${run_group}" "${jre_dir}"
  chmod -R go-w "${jre_dir}"
  [[ -x "${jre_dir}/bin/java" ]] || chmod 0750 "${jre_dir}/bin/java"
  "${jre_dir}/bin/java" -version 2>&1 | grep -Fq '21.0.12' || die "PRIVATE_JRE_INVALID: expected Temurin Java 21.0.12."
}

yaml_quote() { local value="${1//\'/\'\'}"; printf "'%s'" "${value}"; }
database_r2dbc_url() {
  local database_endpoint="${database_host}"
  [[ "${database_endpoint}" != *:* ]] || database_endpoint="[${database_endpoint}]"
  case "${database_type}" in
    h2) printf '%s' "r2dbc:h2:file:///\${halo.work-dir}/db/halo-next?MODE=MySQL&DB_CLOSE_ON_EXIT=FALSE" ;;
    postgresql) printf 'r2dbc:pool:postgresql://%s:%s/%s' "${database_endpoint}" "${database_port}" "${database_name}" ;;
    mysql) printf 'r2dbc:pool:mysql://%s:%s/%s' "${database_endpoint}" "${database_port}" "${database_name}" ;;
    mariadb) printf 'r2dbc:pool:mariadb://%s:%s/%s' "${database_endpoint}" "${database_port}" "${database_name}" ;;
  esac
}
database_platform() { printf '%s' "${database_type}"; }
database_runtime_username() { [[ "${database_type}" == h2 ]] && printf 'admin' || printf '%s' "${database_username}"; }
write_environment_file() {
  if [[ -z "${database_password}" && "${database_type}" == h2 && -r "${rollback_dir}/legacy-h2-password" ]]; then
    database_password="$(<"${rollback_dir}/legacy-h2-password")"
  fi
  if [[ -z "${database_password}" && "${database_type}" == h2 ]]; then
    local random_seed
    random_seed="$(</proc/sys/kernel/random/uuid)$(</proc/sys/kernel/random/uuid)"
    random_seed="${random_seed//-/}"
    database_password="${random_seed:0:32}"
  fi
  [[ -n "${database_password}" ]] || die "DATABASE_PASSWORD_REQUIRED: no stored database credential is available."
  local candidate
  candidate="$(mktemp "${config_dir}/.halo-env.XXXXXX")"
  printf 'HALO_DATABASE_PASSWORD=%s\n' "${database_password}" >"${candidate}"
  chmod 0640 "${candidate}"; chown root:"${run_group}" "${candidate}"
  mv -f -- "${candidate}" "${environment_file}"
}
write_application_config() {
  local target="${1:-${config_file}}" r2dbc platform
  r2dbc="$(database_r2dbc_url)"; platform="$(database_platform)"
  {
    printf 'server:\n'
    printf '  port: %s\n' "${halo_port}"
    printf '  address: %s\n' "$(yaml_quote "${bind_address}")"
    printf '  forward-headers-strategy: %s\n' "${forward_headers_strategy}"
    printf '  compression:\n    enabled: %s\n' "${compression_enabled}"
    printf 'spring:\n'
    printf '  r2dbc:\n'
    printf '    url: %s\n' "$(yaml_quote "${r2dbc}")"
    printf '    username: %s\n' "$(yaml_quote "$(database_runtime_username)")"
    printf '    password: %s\n' "$(yaml_quote "\${HALO_DATABASE_PASSWORD}")"
    printf '  sql:\n    init:\n      mode: always\n      platform: %s\n' "${platform}"
    printf '  web:\n    resources:\n      cache:\n        cachecontrol:\n          max-age: %sd\n' "${static_cache_max_age_days}"
    printf 'halo:\n'
    printf '  work-dir: %s\n' "$(yaml_quote "${data_dir}")"
    printf '  external-url: %s\n' "$(yaml_quote "${external_url}")"
    printf '  use-absolute-permalink: %s\n' "${use_absolute_permalink}"
    printf 'logging:\n'
    printf '  file:\n    name: %s\n' "$(yaml_quote "${data_dir}/logs/halo.log")"
    printf '  logback:\n    rollingpolicy:\n'
    printf '      max-file-size: %sMB\n' "${log_max_file_size_mb}"
    printf '      total-size-cap: %sMB\n' "${log_total_size_cap_mb}"
    printf '      max-history: %s\n' "${log_max_history}"
  } >"${target}"
  chmod 0640 "${target}"; chown root:"${run_group}" "${target}"
}
write_service_unit() {
  local candidate systemd_version
  systemd_version="$(systemctl --version 2>/dev/null | awk 'NR == 1 {print $2}')"
  [[ "${systemd_version}" =~ ^[0-9]+$ ]] || die "HOST_INIT_UNSUPPORTED: cannot determine the systemd version."
  candidate="$(mktemp /etc/systemd/system/.halo.service.XXXXXX)"
  cat >"${candidate}" <<EOF
[Unit]
Description=Halo CMS managed by OneinStack
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=${run_user}
Group=${run_group}
WorkingDirectory=${data_dir}
EnvironmentFile=${environment_file}
ExecStart=${jre_dir}/bin/java -Xms${jvm_xms_mb}m -Xmx${jvm_xmx_mb}m -Dfile.encoding=UTF-8 -jar ${jar_file} --spring.config.additional-location=optional:file:${config_dir}/
Restart=on-failure
RestartSec=5s
TimeoutStartSec=480
TimeoutStopSec=120
SuccessExitStatus=143
UMask=0027
NoNewPrivileges=true
PrivateTmp=true
EOF
  if ((10#${systemd_version} >= 232)); then
    cat >>"${candidate}" <<EOF
ProtectSystem=strict
ProtectHome=read-only
ReadWritePaths=${data_dir}
CapabilityBoundingSet=
AmbientCapabilities=
LockPersonality=true
RestrictSUIDSGID=true
SystemCallArchitectures=native
EOF
  else
    cat >>"${candidate}" <<EOF
ProtectSystem=full
ProtectHome=read-only
ReadWriteDirectories=${data_dir}
CapabilityBoundingSet=
SystemCallArchitectures=native
EOF
  fi
  cat >>"${candidate}" <<EOF

[Install]
WantedBy=multi-user.target
EOF
  chmod 0644 "${candidate}"
  mv -f -- "${candidate}" "${unit_file}"
  systemctl daemon-reload
}
persist_install_parameters() {
  local candidate
  install -d -m 0750 -- "${state_dir}"
  candidate="$(mktemp "${state_dir}/.install-parameters.XXXXXX")"
  {
    printf 'HALO_PORT=%s\n' "${halo_port}"
    printf 'HALO_BIND_ADDRESS=%s\n' "${bind_address}"
    printf 'HALO_EXTERNAL_URL=%s\n' "${external_url}"
    printf 'INSTALL_DIR=%s\n' "${install_dir}"
    printf 'DATA_DIR=%s\n' "${data_dir}"
    printf 'CONFIG_DIR=%s\n' "${config_dir}"
    printf 'RUN_USER=%s\n' "${run_user}"
    printf 'RUN_GROUP=%s\n' "${run_group}"
    printf 'JVM_XMS_MB=%s\n' "${jvm_xms_mb}"
    printf 'JVM_XMX_MB=%s\n' "${jvm_xmx_mb}"
    printf 'DATABASE_TYPE=%s\n' "${database_type}"
    printf 'DATABASE_HOST=%s\n' "${database_host}"
    printf 'DATABASE_PORT=%s\n' "${database_port}"
    printf 'DATABASE_NAME=%s\n' "${database_name}"
    printf 'DATABASE_USERNAME=%s\n' "${database_username}"
    printf 'USE_ABSOLUTE_PERMALINK=%s\n' "${use_absolute_permalink}"
    printf 'FORWARD_HEADERS_STRATEGY=%s\n' "${forward_headers_strategy}"
    printf 'COMPRESSION_ENABLED=%s\n' "${compression_enabled}"
    printf 'STATIC_CACHE_MAX_AGE_DAYS=%s\n' "${static_cache_max_age_days}"
    printf 'LOG_MAX_FILE_SIZE_MB=%s\n' "${log_max_file_size_mb}"
    printf 'LOG_TOTAL_SIZE_CAP_MB=%s\n' "${log_total_size_cap_mb}"
    printf 'LOG_MAX_HISTORY=%s\n' "${log_max_history}"
  } >"${candidate}"
  chmod 0640 "${candidate}"
  mv -f -- "${candidate}" "${install_parameters_file}"
}
migrate_legacy_data() {
  [[ -f "${rollback_dir}/migrate-legacy" && -d "${legacy_data_dir}" ]] || return 0
  cp -a -- "${legacy_data_dir}/." "${data_dir}/"
  rm -f -- "${data_dir}/application.yaml"
  chown -R "${run_user}:${run_group}" "${data_dir}"
  printf 'true\n' >"${rollback_dir}/legacy-copied"
}
health_probe_host() {
  local host="${bind_address}"
  case "${host}" in 0.0.0.0|'') host=127.0.0.1 ;; ::|'::0') host='::1' ;; esac
  printf '%s' "${host}"
}
wait_for_readiness() {
  local _attempt response host host_header
  host="$(health_probe_host)"
  host_header="${host}"
  [[ "${host_header}" != *:* ]] || host_header="[${host_header}]"
  for _attempt in $(seq 1 120); do
    service_is_active || { sleep 2; continue; }
    # shellcheck disable=SC2016 # The child bash receives host and port as positional parameters.
    response="$(timeout 5 bash -c '
      exec 3<>"/dev/tcp/${1}/${2}" || exit 1
      printf "GET /actuator/health/readiness HTTP/1.1\r\nHost: %s:%s\r\nConnection: close\r\n\r\n" "$3" "$2" >&3
      cat <&3
    ' bash "${host}" "${halo_port}" "${host_header}" 2>/dev/null || true)"
    grep -Eq '"status"[[:space:]]*:[[:space:]]*"UP"' <<<"${response}" && return 0
    sleep 2
  done
  return 1
}
verify_runtime() {
  local main_pid process_user
  service_is_active || die "SERVICE_NOT_READY: halo.service is not active."
  managed_unit_matches || die "SERVICE_IDENTITY_MISMATCH: halo.service does not use the managed private JRE and account."
  main_pid="$(systemctl show "${service_name}.service" --property=MainPID --no-pager 2>/dev/null | sed -n 's/^MainPID=//p')"
  [[ "${main_pid}" =~ ^[1-9][0-9]*$ ]] || die "SERVICE_PROCESS_MISSING: Halo MainPID is unavailable."
  process_user="$(ps -o user= -p "${main_pid}" 2>/dev/null | awk '{$1=$1; print}')"
  [[ "${process_user}" == "${run_user}" ]] || die "SERVICE_USER_MISMATCH: expected ${run_user}, got ${process_user:-unknown}."
  verify_checksum "${jar_file}" "${jar_sha256}"
  "${jre_dir}/bin/java" -version 2>&1 | grep -Fq '21.0.12' || die "PRIVATE_JRE_INVALID: Java version mismatch."
  port_is_listening "${halo_port}" || die "PORT_NOT_LISTENING: Halo port ${halo_port} is not listening."
  wait_for_readiness || die "READINESS_FAILED: /actuator/health/readiness did not report UP."
}
config_revision() {
  { sha256sum "${config_file}" "${unit_file}" "${install_parameters_file}"; } | sha256sum | awk '{print $1}'
}
commit_installation() {
  commit_transaction_acls
  install -d -m 0750 -- "${state_dir}"
  printf '%s\n' "${software_version}" >"${state_dir}/version"
  printf '%s\n' "${package_version}" >"${state_dir}/package-version"
  printf 'managed\n' >"${state_dir}/ownership"
  : >"${state_dir}/installed"
  rm -f -- "${retained_data_file}"
  [[ "${database_type}" != h2 ]] || printf 'WARNING: H2 is intended for evaluation and small sites; use an external database for production.\n' >&2
  if [[ -f "${rollback_dir}/legacy-copied" ]]; then rm -rf -- "${legacy_data_dir}"; fi
  rm -rf -- "${rollback_dir}"
}
