#!/usr/bin/env bash
set -Eeuo pipefail
umask 027

component_id="mongodb"
package_version="1.0.17"
software_version="${SOFTWARE_VERSION:-8.0.32}"
mongodb_port="${MONGODB_PORT:-27017}"
mongodb_bind_ip="${MONGODB_BIND_IP:-127.0.0.1}"
install_dir="${INSTALL_DIR:-/usr/local/mongodb}"
data_dir="${DATA_DIR:-/data/mongodb}"
log_dir="${LOG_DIR:-/data/mongodb}"
run_user="${RUN_USER:-mongod}"
run_group="${RUN_GROUP:-mongod}"
admin_username="${MONGODB_ADMIN_USERNAME:-root}"
admin_password="${MONGODB_ADMIN_PASSWORD:-}"
state_root="${ONEINSTACK_COMPONENT_STATE:-${COMPONENT_STATE_DIR:-/var/lib/oneinstack/components}}"
state_dir="${state_root}/${component_id}"
rollback_dir="${state_dir}/rollback"
config_file="/etc/mongod.conf"
unit_file="/etc/systemd/system/mongod.service"
install_parameters_file="${state_dir}/install-parameters"
retained_data_file="${state_dir}/retained-data"
managed_path_acl_file="${state_dir}/managed-path-acl"
transaction_path_acl_file="${rollback_dir}/path-acl-added"
install_mode="${ONEINSTACK_INSTALL_MODE:-center}"
offline_package_path="${ONEINSTACK_OFFLINE_PACKAGE_PATH:-}"
offline_bundle_id="${ONEINSTACK_OFFLINE_BUNDLE_ID:-}"
offline_bundle_digest="${ONEINSTACK_OFFLINE_BUNDLE_DIGEST:-}"
server_key_url="https://pgp.mongodb.com/server-8.0.asc"
server_key_sha256="8c467ea138207ee8d0cbbce3e005c82c73c81b3263e7909bdc36b9a1feff14f6"
server_key_fingerprint="4B0752C1BCA238C0B4EE14DC41DE058A4E7DCA05"
mongosh_key_url="https://pgp.mongodb.com/mongosh.asc"
mongosh_key_sha256="5a63058d4ce36c8c0690316bf7da03706f036b2efb415a2506c50bcb4f1bae28"
mongosh_key_fingerprint="ED581728F2A469C94D18BB58CEED0419D361CB16"
mongosh_version="2.10.0"
server_url=""
server_sha256=""
server_archive=""
server_signature_url=""
mongosh_url=""
mongosh_sha256=""
mongosh_archive=""
mongosh_signature_url=""
host_arch=""
system_id=""
system_version=""
package_manager=""
source_target=""

die() { printf 'ERROR: %s\n' "$*" >&2; exit 1; }
require_command() { command -v "$1" >/dev/null 2>&1 || die "Required command not found: $1"; }
emit_progress() {
  local percent="$1" code="$2" message="$3" fd="${ONEINSTACK_PROGRESS_FD:-}"
  [[ "${fd}" =~ ^[0-9]+$ ]] || return 0
  message="${message//\\/\\\\}"; message="${message//\"/\\\"}"; message="${message//$'\n'/ }"
  # shellcheck disable=SC2261
  printf '{"type":"progress","percent":%s,"code":"%s","message":"%s"}\n' \
    "${percent}" "${code}" "${message}" >&"${fd}" 2>/dev/null || true
}
require_root() { [[ "$(id -u)" -eq 0 ]] || die "This action must run as root."; }
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
    die "${label} must be a normalized absolute path."
  case "${value}" in /|/usr|/usr/local|/etc|/var|/data|/home|/root|/bin|/sbin|/lib|/lib64) die "${label} is too broad: ${value}" ;; esac
  [[ "${value}" != *[[:space:]\"\'\;\|\&\$\`\*\?\[\]\{\}\(\)\<\>\\\#]* ]] ||
    die "${label} contains unsupported characters."
}
paths_overlap() {
  local left="$1" right="$2"
  [[ "${left}" == "${right}" || "${left}" == "${right}/"* || "${right}" == "${left}/"* ]]
}
valid_ipv6_literal() {
  local value="$1" compressed="${1/::/}" normalized part colons
  local -a ipv6_parts
  [[ "${value}" =~ ^[0-9A-Fa-f:]+$ && "${value}" != *:::* ]] || return 1
  [[ "${value}" == *:*:* ]] || return 1
  [[ "${value}" != :* || "${value}" == ::* ]] || return 1
  [[ "${value}" != *: || "${value}" == *:: ]] || return 1
  colons="${value//[^:]/}"
  ((${#colons} <= 7)) || return 1
  if [[ "${value}" != *::* ]]; then
    ((${#colons} == 7)) || return 1
  elif [[ "${compressed}" == *::* ]]; then
    return 1
  fi
  normalized="${value/::/:0:}"
  IFS=':' read -r -a ipv6_parts <<<"${normalized}"
  for part in "${ipv6_parts[@]}"; do
    [[ -z "${part}" || ${#part} -le 4 ]] || return 1
  done
}
validate_bind_ip() {
  local value="$1" item label
  local -a bind_items labels
  [[ -n "${value}" && "${value}" != *[$'\r\n\t ']* && "${value}" != *[\{\}\[\]\#\&\*!\|\>\<\'\"\\]* ]] ||
    die "MONGODB_BIND_IP contains whitespace or YAML control characters."
  IFS=',' read -r -a bind_items <<<"${value}"
  ((${#bind_items[@]} > 0 && ${#bind_items[@]} <= 32)) || die "MONGODB_BIND_IP must contain 1-32 addresses."
  for item in "${bind_items[@]}"; do
    if [[ "${item}" == *:* ]]; then
      valid_ipv6_literal "${item}" || die "MONGODB_BIND_IP contains an invalid IPv6 address."
      continue
    fi
    [[ "${item}" =~ ^[A-Za-z0-9][A-Za-z0-9.-]{0,252}$ && "${item}" != *..* && "${item}" != .* && "${item}" != *. ]] ||
      die "MONGODB_BIND_IP contains an invalid IP address or hostname."
    IFS='.' read -r -a labels <<<"${item}"
    for label in "${labels[@]}"; do
      [[ -n "${label}" && ${#label} -le 63 && "${label}" != -* && "${label}" != *- ]] ||
        die "MONGODB_BIND_IP contains an invalid hostname."
    done
  done
}
is_loopback_bind() {
  local item
  local -a bind_items
  IFS=',' read -r -a bind_items <<<"$1"
  for item in "${bind_items[@]}"; do
    case "${item}" in 127.*|::1|localhost) ;; *) return 1 ;; esac
  done
  return 0
}
version_at_least() {
  local current="$1" required="$2" index left right
  local -a current_parts required_parts
  IFS='.' read -r -a current_parts <<<"${current}"
  IFS='.' read -r -a required_parts <<<"${required}"
  for index in 0 1 2; do
    left="${current_parts[index]:-0}"; right="${required_parts[index]:-0}"
    left="${left%%[^0-9]*}"; right="${right%%[^0-9]*}"
    ((10#${left:-0} > 10#${right:-0})) && return 0
    ((10#${left:-0} < 10#${right:-0})) && return 1
  done
  return 0
}
kernel_is_incompatible() {
  local release base major minor patch
  release="$(uname -r)"; base="${release%%-*}"
  IFS='.' read -r major minor patch _ <<<"${base}"
  [[ "${major}" =~ ^[0-9]+$ && "${minor}" =~ ^[0-9]+$ ]] || return 0
  patch="${patch:-0}"; patch="${patch%%[^0-9]*}"
  if ((major == 6 && minor >= 19)); then return 0; fi
  if ((major == 7 && minor == 0 && 10#${patch:-0} <= 13)); then return 0; fi
  return 1
}
check_cpu() {
  case "${host_arch}" in
    amd64)
      grep -Eiq '(^|[[:space:]])avx([[:space:]]|$)' /proc/cpuinfo ||
        die "CPU_UNSUPPORTED: MongoDB 8.0 x86_64 requires AVX and an officially supported microarchitecture."
      ;;
    arm64)
      grep -Eiq '(^|[[:space:]])asimd([[:space:]]|$)' /proc/cpuinfo &&
        grep -Eiq '(^|[[:space:]])asimddp([[:space:]]|$)' /proc/cpuinfo ||
        die "CPU_UNSUPPORTED: MongoDB 8.0 arm64 requires ARMv8.2-A or later."
      ;;
  esac
}
detect_host() {
  local amazon_release kernel_release
  [[ -r /etc/os-release ]] || die "HOST_PLATFORM_UNSUPPORTED: /etc/os-release is unavailable."
  # shellcheck disable=SC1091
  source /etc/os-release
  system_id="${ID,,}"
  system_version="${VERSION_ID:-}"
  if [[ "${system_id}" == centos ]]; then
    grep -Eiq 'CentOS[[:space:]]+Stream' /etc/os-release ||
      die "HOST_PLATFORM_UNSUPPORTED: ordinary CentOS is not supported; CentOS Stream is required."
    system_id="centos-stream"
  fi
  host_arch="$(architecture_name)"
  case "${system_id}:${system_version}" in
    ubuntu:20.04|ubuntu:22.04|ubuntu:24.04) ;;
    debian:12) [[ "${host_arch}" == amd64 ]] || die "HOST_PLATFORM_UNSUPPORTED: Debian 12 arm64 is not enabled." ;;
    rhel:*|rocky:*|almalinux:*|ol:*)
      case "${system_version%%.*}" in 8|9|10) ;; *) die "HOST_PLATFORM_UNSUPPORTED: unsupported Enterprise Linux release ${system_version}." ;; esac
      if [[ "${system_version%%.*}" == 8 ]] && ! version_at_least "${system_version}" 8.8; then
        die "HOST_PLATFORM_UNSUPPORTED: MongoDB requires Enterprise Linux 8.8 or later."
      fi
      if [[ "${system_version%%.*}" == 9 ]] && ! version_at_least "${system_version}" 9.3; then
        die "HOST_PLATFORM_UNSUPPORTED: MongoDB requires Enterprise Linux 9.3 or later."
      fi
      if [[ "${system_id}" == ol ]]; then
        [[ "${host_arch}" == amd64 ]] || die "HOST_PLATFORM_UNSUPPORTED: Oracle Linux arm64 is not enabled."
        kernel_release="$(uname -r)"
        [[ "${kernel_release,,}" != *uek* ]] || die "HOST_PLATFORM_UNSUPPORTED: Oracle Linux UEK is unsupported; use RHCK."
      fi
      ;;
    centos-stream:*)
      case "${system_version%%.*}" in 8|9|10) ;; *) die "HOST_PLATFORM_UNSUPPORTED: unsupported CentOS Stream release ${system_version}." ;; esac
      [[ "${host_arch}" == amd64 ]] || die "HOST_PLATFORM_UNSUPPORTED: CentOS Stream arm64 is not enabled pending real-host acceptance."
      ;;
    amzn:2023)
      [[ -r /etc/system-release ]] || die "HOST_PLATFORM_UNSUPPORTED: cannot verify the Amazon Linux point release."
      amazon_release="$(</etc/system-release)"
      [[ "${amazon_release}" =~ 2023\.([0-9]+) ]] || die "HOST_PLATFORM_UNSUPPORTED: cannot verify the Amazon Linux point release."
      ((10#${BASH_REMATCH[1]} >= 3)) || die "HOST_PLATFORM_UNSUPPORTED: Amazon Linux 2023.3 or later is required."
      ;;
    sles:15*)
      [[ "${host_arch}" == amd64 ]] || die "HOST_PLATFORM_UNSUPPORTED: SLES 15 arm64 is not enabled."
      version_at_least "${system_version}" 15.5 || die "HOST_PLATFORM_UNSUPPORTED: SLES 15 SP5 or later is required."
      ;;
    *) die "HOST_PLATFORM_UNSUPPORTED: unsupported Linux platform ${system_id:-unknown} ${system_version:-unknown}." ;;
  esac
  kernel_is_incompatible && die "KERNEL_UNSUPPORTED: Linux kernels 6.19 through 7.0.13 are incompatible with MongoDB 8.0."
  check_cpu
  if command -v apt-get >/dev/null 2>&1; then package_manager=apt
  elif command -v dnf >/dev/null 2>&1; then package_manager=dnf
  elif command -v yum >/dev/null 2>&1; then package_manager=yum
  elif command -v zypper >/dev/null 2>&1; then package_manager=zypper
  else die "HOST_DEPENDENCY_UNSUPPORTED: apt, dnf, yum, or zypper is required."; fi
}
select_source() {
  local platform="" machine="x86_64"
  [[ "${host_arch}" != arm64 ]] || machine="aarch64"
  case "${system_id}:${system_version%%.*}" in
    ubuntu:20) platform=ubuntu2004 ;;
    ubuntu:22) platform=ubuntu2204 ;;
    ubuntu:24) platform=ubuntu2404 ;;
    debian:12) platform=debian12 ;;
    rhel:8|rocky:8|almalinux:8|ol:8|centos-stream:8) platform=rhel8 ;;
    rhel:9|rocky:9|almalinux:9|ol:9|centos-stream:9) platform=rhel93 ;;
    rhel:10|rocky:10|almalinux:10|ol:10|centos-stream:10) platform=rhel10 ;;
    amzn:2023) platform=amazon2023 ;;
    sles:15) platform=suse15 ;;
    *) die "SOURCE_UNAVAILABLE: no MongoDB artifact selector matches ${system_id} ${system_version}." ;;
  esac
  if [[ "${software_version}" == 8.0.17 && "${platform}" == rhel10 ]]; then
    die "SOURCE_UNAVAILABLE: MongoDB 8.0.17 has no official RHEL 10 archive; select 8.0.32."
  fi
  if [[ "${host_arch}" == arm64 && ( "${system_id}" == debian || "${system_id}" == ol || "${system_id}" == centos-stream || "${system_id}" == sles ) ]]; then
    die "SOURCE_UNAVAILABLE: this platform and architecture combination is not enabled."
  fi
  source_target="${platform}"
  server_archive="mongodb-linux-${machine}-${platform}-${software_version}.tgz"
  server_url="https://fastdl.mongodb.org/linux/${server_archive}"
  server_signature_url="${server_url}.sig"
  case "${software_version}:${platform}:${host_arch}" in
    8.0.17:ubuntu2004:amd64) server_sha256=b90f31da88ba94ad20b7a18801a6abd85b7f31bf42677e11b53ec5f82cd5dedf ;;
    8.0.17:ubuntu2004:arm64) server_sha256=6f8de47d323c93a2a3d800f9ba529321d7c24dce8e6f344b933c89b9d744dd3c ;;
    8.0.17:ubuntu2204:amd64) server_sha256=4372a8e503a61814c565d4ccbc5f6765787944772e8969c13ba96c99cca11f75 ;;
    8.0.17:ubuntu2204:arm64) server_sha256=2709ec6acd3de02666d02318d436f5d68dfb7d20b025d24a12a0914aacff4bd3 ;;
    8.0.17:ubuntu2404:amd64) server_sha256=fe7bccea2ac1eed16867e9ae5a60481455ee30796f285fe67d0d02a6c2abbdda ;;
    8.0.17:ubuntu2404:arm64) server_sha256=8dc5919025dbabe2103b8e187e2102492af93c08842c37114272bd894e5cfc13 ;;
    8.0.17:debian12:amd64) server_sha256=b774ed64ec13732d25cd8c937bcd221ceadd0c746371f2279aa63aa492c1d0d0 ;;
    8.0.17:rhel8:amd64) server_sha256=c8986f6fc001d456bd2160c2e96d24201599627eb5832f49d95673779a2768cf ;;
    8.0.17:rhel8:arm64) server_sha256=f7118b78fe5f60724109bf3548d46d4c898f9ec14a7bb4821d43a080a8a98572 ;;
    8.0.17:rhel93:amd64) server_sha256=9bcb2b0ae5d114a752e3fa9105a50020a104b6c5eaf69d7c248cae43b812196e ;;
    8.0.17:rhel93:arm64) server_sha256=7192d831f58cc5eece0e3d24921dc4612c99b506ee432c703c8ba687f6322894 ;;
    8.0.17:amazon2023:amd64) server_sha256=29c195b32b1dd3f0dfa8a497e2199ec313e1e86d3b56966f49822ccb65f1cfcd ;;
    8.0.17:amazon2023:arm64) server_sha256=6f26e7d510d5136eb3397cc15e54adb28adb9e0eeb5648d4f3e2aac3a561dfb4 ;;
    8.0.17:suse15:amd64) server_sha256=255490e01c2f10ebbf56b284e08d2b1830e5693842b5ba9849d88a92ce5c3cb5 ;;
    8.0.32:ubuntu2004:amd64) server_sha256=db3533f4fdeeb9f590b2b4e8219007bc3d5ae65b5b5c843aecca3f730cf0eb41 ;;
    8.0.32:ubuntu2004:arm64) server_sha256=7063f857e5ce0b2bbd168b5a6f140de83dddaf819af3c3974aabf667b6c993cc ;;
    8.0.32:ubuntu2204:amd64) server_sha256=41da8d92dde2896ebda278cf296ae86462da6b39eda81ce6ce4fb9ce819adb2e ;;
    8.0.32:ubuntu2204:arm64) server_sha256=7411bc9efef53346a26e67241ca9db9926c30b7a056e2515068223b497682c81 ;;
    8.0.32:ubuntu2404:amd64) server_sha256=b411be17c31ef249767ed91974d876e007c91afd5f45e1534057d247eada9f0d ;;
    8.0.32:ubuntu2404:arm64) server_sha256=8cca3993520a7f189790264ee57fb5d55a91094fb4be5f06bbbaf67c974d273f ;;
    8.0.32:debian12:amd64) server_sha256=b61cda162c6592347b7503c683145304f74c900b1c10a395520c7c24d3f8445c ;;
    8.0.32:rhel8:amd64) server_sha256=0c582aa72b45fb74e15c47f4d13d329a506ae5b7ceef65362fa11b40826da0aa ;;
    8.0.32:rhel8:arm64) server_sha256=a7ff6ade9efb7c0566a4b2f3bc828a245729c1a0553c311d992f3564fab3f5bf ;;
    8.0.32:rhel93:amd64) server_sha256=91d7f9fd463d3b91359528bcce8d8c2c30ff6aa90f1f58480e225d70a7057566 ;;
    8.0.32:rhel93:arm64) server_sha256=76e77cf57421604d3f302078e73d37523d067716f587c6c634ed94adbc8540d0 ;;
    8.0.32:rhel10:amd64) server_sha256=dd14cbda4cbdee634d459f88c1cb8e4120f7591495b9334d29e30d35730ba718 ;;
    8.0.32:rhel10:arm64) server_sha256=fca3f0670838e46859f54d800fd8f81dd21ab031852f1bd15a81dbce52bf64de ;;
    8.0.32:amazon2023:amd64) server_sha256=6c2a714294985566a21cb9b97dd7422a0d46e43affca95d2d1dc303f0e60515e ;;
    8.0.32:amazon2023:arm64) server_sha256=b55cd433aa3c3ea43b9545d3625e52068e25f18eea7ba156b12d97b28d4fc4f9 ;;
    8.0.32:suse15:amd64) server_sha256=b8b6267af41a4d879eb69bfee4955ab7d6dcfcd9541b84ba23b348e9d9b8315c ;;
    *) die "SOURCE_UNAVAILABLE: no pinned MongoDB checksum matches ${software_version}/${platform}/${host_arch}." ;;
  esac
  if [[ "${host_arch}" == amd64 ]]; then
    mongosh_archive="mongosh-${mongosh_version}-linux-x64.tgz"
    mongosh_sha256="42034ba0fc9a48fd65ddcc5150b2e9d8a777965019220744baee42ee9669d543"
  else
    mongosh_archive="mongosh-${mongosh_version}-linux-arm64.tgz"
    mongosh_sha256="585da7587d862a2fc54f38e4db1d5bc17823cf29a51d3d73e5838b86778cb98a"
  fi
  mongosh_url="https://github.com/mongodb-js/mongosh/releases/download/v${mongosh_version}/${mongosh_archive}"
  mongosh_signature_url="${mongosh_url}.sig"
}
load_persisted_parameters() {
  local key value marker
  [[ -r "${install_parameters_file}" ]] || return 0
  while IFS='=' read -r key value; do
    marker="ONEINSTACK_PARAMETER_${key}_EXPLICIT"
    case "${key}" in
      MONGODB_PORT) [[ "${!marker:-false}" == true ]] || mongodb_port="${value}" ;;
      MONGODB_BIND_IP) [[ "${!marker:-false}" == true ]] || mongodb_bind_ip="${value}" ;;
      INSTALL_DIR) [[ "${!marker:-false}" == true ]] || install_dir="${value}" ;;
      DATA_DIR) [[ "${!marker:-false}" == true ]] || data_dir="${value}" ;;
      LOG_DIR) [[ "${!marker:-false}" == true ]] || log_dir="${value}" ;;
      RUN_USER) [[ "${!marker:-false}" == true ]] || run_user="${value}" ;;
      RUN_GROUP) [[ "${!marker:-false}" == true ]] || run_group="${value}" ;;
      MONGODB_ADMIN_USERNAME) [[ "${!marker:-false}" == true ]] || admin_username="${value}" ;;
    esac
  done <"${install_parameters_file}"
}
load_persisted_parameters
validate_inputs() {
  [[ "${software_version}" == 8.0.17 || "${software_version}" == 8.0.32 ]] || die "SOFTWARE_VERSION must be 8.0.17 or 8.0.32."
  [[ "${install_mode}" == center || "${install_mode}" == offline ]] || die "ONEINSTACK_INSTALL_MODE must be center or offline."
  if [[ "${install_mode}" == offline ]]; then
    [[ -n "${offline_package_path}" && "${offline_package_path}" == /* && "$(realpath -m -- "${offline_package_path}")" == "${offline_package_path}" ]] ||
      die "OFFLINE_BUNDLE_INVALID: offline mode requires a normalized absolute Bundle path."
    [[ "${offline_bundle_digest}" =~ ^[0-9a-f]{64}$ && "${offline_bundle_id}" == "sha256:${offline_bundle_digest}" ]] ||
      die "OFFLINE_BUNDLE_IDENTITY_MISMATCH: Panel Bundle identity and digest are required and must match."
  elif [[ -n "${offline_package_path}" || -n "${offline_bundle_id}" || -n "${offline_bundle_digest}" ]]; then
    die "Offline Bundle metadata cannot be supplied in online mode."
  fi
  [[ "${mongodb_port}" =~ ^[0-9]+$ && "${mongodb_port}" -ge 1 && "${mongodb_port}" -le 65535 ]] || die "MONGODB_PORT must be 1-65535."
  validate_bind_ip "${mongodb_bind_ip}"
  validate_path "${install_dir}" INSTALL_DIR; validate_path "${data_dir}" DATA_DIR
  validate_path "${log_dir}" LOG_DIR; validate_path "${state_root}" ONEINSTACK_COMPONENT_STATE
  ! paths_overlap "${install_dir}" "${data_dir}" && ! paths_overlap "${install_dir}" "${log_dir}" || die "INSTALL_DIR must not overlap DATA_DIR or LOG_DIR."
  ! paths_overlap "${state_root}" "${install_dir}" && ! paths_overlap "${state_root}" "${data_dir}" && ! paths_overlap "${state_root}" "${log_dir}" ||
    die "ONEINSTACK_COMPONENT_STATE must not overlap runtime directories."
  [[ "${run_user}" =~ ^[a-z_][a-z0-9_-]{0,30}$ ]] || die "RUN_USER is invalid."
  [[ "${run_group}" =~ ^[a-z_][a-z0-9_-]{0,30}$ ]] || die "RUN_GROUP is invalid."
  [[ "${admin_username}" =~ ^[A-Za-z_][A-Za-z0-9._-]{0,63}$ ]] || die "MONGODB_ADMIN_USERNAME is invalid."
  if [[ -n "${admin_password}" ]]; then
    [[ ${#admin_password} -ge 12 && ${#admin_password} -le 128 && "${admin_password}" != *[$'\r\n\0']* ]] ||
      die "MONGODB_ADMIN_PASSWORD must contain 12-128 characters without control line breaks."
  fi
}
managed_installation_present() {
  [[ -r "${state_dir}/version" && -r "${install_parameters_file}" && -x "${install_dir}/bin/mongod" && -f "${config_file}" ]] &&
    grep -Fqx '# Managed by Oneinstack MongoDB component' "${config_file}"
}
install_parameter_from_file() {
  local file="$1" key="$2"
  awk -F= -v key="${key}" '$1 == key {sub(/^[^=]*=/, ""); print; exit}' "${file}"
}
retained_archive_matches() {
  local archive="$1" archived_version parameters
  [[ -d "${archive}" && -x "${archive}/install/bin/mongod" && -f "${archive}/mongod.conf" ]] || return 1
  grep -Fqx '# Managed by Oneinstack MongoDB component' "${archive}/mongod.conf" || return 1
  grep -Eq '^[[:space:]]+authorization:[[:space:]]+enabled[[:space:]]*$' "${archive}/mongod.conf" || return 1
  [[ "$(config_scalar_file "${archive}/mongod.conf" storage dbPath '')" == "${data_dir}" ]] || return 1
  [[ "$(config_scalar_file "${archive}/mongod.conf" systemLog path '')" == "${log_dir}/mongod.log" ]] || return 1
  archived_version="$("${archive}/install/bin/mongod" --version 2>/dev/null | awk '/db version/ {sub(/^v/,"",$3); print $3; exit}')"
  [[ "${archived_version}" == "${software_version}" ]] || return 1
  parameters="${archive}/install-parameters"
  if [[ -r "${parameters}" ]]; then
    [[ "$(install_parameter_from_file "${parameters}" SOFTWARE_VERSION)" == "${software_version}" ]] || return 1
    [[ "$(install_parameter_from_file "${parameters}" INSTALL_DIR)" == "${install_dir}" ]] || return 1
    [[ "$(install_parameter_from_file "${parameters}" DATA_DIR)" == "${data_dir}" ]] || return 1
    [[ "$(install_parameter_from_file "${parameters}" LOG_DIR)" == "${log_dir}" ]] || return 1
    [[ "$(install_parameter_from_file "${parameters}" RUN_USER)" == "${run_user}" ]] || return 1
    [[ "$(install_parameter_from_file "${parameters}" RUN_GROUP)" == "${run_group}" ]] || return 1
    [[ "$(install_parameter_from_file "${parameters}" MONGODB_ADMIN_USERNAME)" == "${admin_username}" ]] || return 1
  fi
  return 0
}
retained_data_archive() {
  local archive archive_name
  if [[ -r "${retained_data_file}" ]]; then
    archive_name="$(<"${retained_data_file}")"
    [[ "${archive_name}" =~ ^[0-9]{8}T[0-9]{6}Z$ ]] || return 1
    archive="${state_dir}/removed/${archive_name}"
    retained_archive_matches "${archive}" || return 1
    printf '%s' "${archive}"
    return 0
  fi
  for archive in "${state_dir}"/removed/*; do
    [[ -d "${archive}" ]] || continue
    if retained_archive_matches "${archive}"; then
      printf '%s' "${archive}"
      return 0
    fi
  done
  return 1
}
persist_install_parameters() {
  install -d -m 0750 -- "${state_dir}"
  local temporary
  temporary="$(mktemp "${state_dir}/.install-parameters.XXXXXX")"
  {
    printf 'SOFTWARE_VERSION=%s\n' "${software_version}"
    printf 'MONGODB_PORT=%s\n' "${mongodb_port}"
    printf 'MONGODB_BIND_IP=%s\n' "${mongodb_bind_ip}"
    printf 'INSTALL_DIR=%s\n' "${install_dir}"
    printf 'DATA_DIR=%s\n' "${data_dir}"
    printf 'LOG_DIR=%s\n' "${log_dir}"
    printf 'RUN_USER=%s\n' "${run_user}"
    printf 'RUN_GROUP=%s\n' "${run_group}"
    printf 'MONGODB_ADMIN_USERNAME=%s\n' "${admin_username}"
  } >"${temporary}"
  chmod 0600 "${temporary}"
  mv -f -- "${temporary}" "${install_parameters_file}"
}
reject_immutable_runtime_changes() {
  local key value marker current
  [[ -r "${state_dir}/version" && -r "${install_parameters_file}" ]] || return 0
  while IFS='=' read -r key value; do
    case "${key}" in
      INSTALL_DIR) current="${install_dir}" ;;
      DATA_DIR) current="${data_dir}" ;;
      LOG_DIR) current="${log_dir}" ;;
      RUN_USER) current="${run_user}" ;;
      RUN_GROUP) current="${run_group}" ;;
      *) continue ;;
    esac
    marker="ONEINSTACK_PARAMETER_${key}_EXPLICIT"
    [[ "${!marker:-false}" != true || "${current}" == "${value}" ]] || die "${key} cannot change after MongoDB installation."
  done <"${install_parameters_file}"
}
validate_upgrade_direction() {
  [[ -r "${state_dir}/version" ]] || return 0
  local current; current="$(<"${state_dir}/version")"
  [[ "${current}" == 8.0.17 || "${current}" == 8.0.32 ]] || die "UPGRADE_UNSUPPORTED: managed MongoDB version ${current} is unknown."
  [[ ! ( "${current}" == 8.0.32 && "${software_version}" == 8.0.17 ) ]] || die "DOWNGRADE_UNSUPPORTED: MongoDB 8.0.32 cannot be downgraded to 8.0.17."
}
port_is_listening() {
  if command -v ss >/dev/null 2>&1; then
    ss -H -ltn "sport = :$1" 2>/dev/null | grep -Eq "[:.]$1[[:space:]]"
    return
  fi
  local hex; hex="$(printf '%04X' "$1")"
  awk -v port="${hex}" 'NR > 1 {split($2,a,":"); if (a[2]==port && $4=="0A") found=1} END {exit !found}' /proc/net/tcp /proc/net/tcp6 2>/dev/null
}
precheck_ownership() {
  local external="" retained_archive="" retained_port=""
  if managed_installation_present; then return 0; fi
  if [[ -e "${install_dir}" ]] && find "${install_dir}" -mindepth 1 -maxdepth 1 -print -quit | grep -q .; then
    die "EXTERNAL_INSTALLATION_DETECTED: target install directory is non-empty and not managed by Oneinstack."
  fi
  [[ ! -e "${config_file}" ]] || die "EXTERNAL_CONFIGURATION_DETECTED: refusing to replace unknown ${config_file}."
  if [[ -e "${data_dir}" ]] && find "${data_dir}" -mindepth 1 -maxdepth 1 -print -quit | grep -q .; then
    retained_archive="$(retained_data_archive || true)"
    [[ -n "${retained_archive}" ]] ||
      die "EXTERNAL_DATA_DETECTED: target data directory is non-empty and has no matching Oneinstack preserved-data record."
    retained_port="$(config_scalar_file "${retained_archive}/mongod.conf" net port "${mongodb_port}")"
    port_is_listening "${retained_port}" && die "PORT_CONFLICT: preserved MongoDB port ${retained_port} is already occupied."
  fi
  external="$(command -v mongod || true)"
  [[ -z "${external}" || "${external}" == "${install_dir}/bin/mongod" ]] ||
    die "EXTERNAL_INSTALLATION_DETECTED: an unmanaged mongod binary is already installed at ${external}."
  if [[ -z "${retained_archive}" ]] && port_is_listening "${mongodb_port}"; then
    die "PORT_CONFLICT: MongoDB port ${mongodb_port} is already occupied."
  fi
  [[ -n "${admin_password}" ]] ||
    die "MONGODB_ADMIN_PASSWORD is required for a new installation or preserved-data authentication."
}
ensure_account() {
  getent group "${run_group}" >/dev/null 2>&1 || groupadd --system "${run_group}"
  if ! id "${run_user}" >/dev/null 2>&1; then
    useradd --system --gid "${run_group}" --home-dir "${data_dir}" --shell /usr/sbin/nologin "${run_user}"
  fi
  [[ "$(id -gn "${run_user}")" == "${run_group}" ]] || getent group "${run_group}" | grep -Eq "(^|,)${run_user}(,|$)" ||
    die "RUN_USER does not belong to RUN_GROUP."
}
record_managed_path_acl() {
  local acl_user="$1" acl_path="$2" record
  record="${acl_user}"$'\t'"${acl_path}"$'\t--x'
  install -d -m 0750 -- "${state_dir}"
  if [[ ! -f "${managed_path_acl_file}" ]] || ! grep -Fqx -- "${record}" "${managed_path_acl_file}"; then
    printf '%s\n' "${record}" >>"${managed_path_acl_file}"
    chmod 0600 "${managed_path_acl_file}"
  fi
  if [[ -f "${rollback_dir}/transaction-started" ]] &&
    { [[ ! -f "${transaction_path_acl_file}" ]] || ! grep -Fqx -- "${record}" "${transaction_path_acl_file}"; }; then
    printf '%s\n' "${record}" >>"${transaction_path_acl_file}"
    chmod 0600 "${transaction_path_acl_file}"
  fi
}
ensure_runtime_path_traversal() {
  local acl_user="$1" managed_path="$2" description="$3"
  local current_path index current_acl access_mask existing_line effective_permissions
  local -a path_chain=()
  current_path="${managed_path%/}"
  while [[ "${current_path}" != / ]]; do
    path_chain+=("${current_path}")
    current_path="$(dirname -- "${current_path}")"
  done
  for ((index=${#path_chain[@]}; index > 0; index--)); do
    current_path="${path_chain[index - 1]}"
    [[ -d "${current_path}" && ! -L "${current_path}" ]] ||
      die "RUNTIME_PATH_INVALID: ${description} contains a missing or symbolic-link directory: ${current_path}."
    if runuser -u "${acl_user}" -- test -x "${current_path}"; then
      continue
    fi
    require_command getfacl
    require_command setfacl
    current_acl="$(getfacl -cp -- "${current_path}")" ||
      die "RUNTIME_PATH_ACL_INSPECTION_FAILED: unable to inspect ${current_path}."
    existing_line="$(awk -F: -v user="${acl_user}" '$1 == "user" && $2 == user {print; exit}' <<<"${current_acl}")"
    if [[ -n "${existing_line}" ]]; then
      effective_permissions="$(awk -F'#effective:' '{print $2}' <<<"${existing_line}" | tr -d '[:space:]')"
      [[ -n "${effective_permissions}" ]] || effective_permissions="$(awk -F: '{print $3}' <<<"${existing_line}" | tr -d '[:space:]')"
      [[ "${effective_permissions}" == *x* ]] ||
        die "RUNTIME_PATH_ACL_CONFLICT: existing ACL for ${acl_user} on ${current_path} denies traversal and was preserved."
      die "RUNTIME_PATH_TRAVERSAL_DENIED: ${acl_user} cannot traverse ${current_path} despite its existing ACL entry."
    fi
    access_mask="$(awk -F: '$1 == "mask" && $2 == "" {print $3; exit}' <<<"${current_acl}")"
    if [[ -n "${access_mask}" ]]; then
      [[ "${access_mask}" == *x* ]] ||
        die "RUNTIME_PATH_ACL_MASK_CONFLICT: ACL mask on ${current_path} denies traversal; refusing to broaden unrelated ACL access."
      setfacl --no-mask -m "u:${acl_user}:--x" -- "${current_path}" ||
        die "RUNTIME_PATH_ACL_APPLY_FAILED: unable to grant ${acl_user} traverse access to ${current_path}."
    else
      setfacl -m "u:${acl_user}:--x" -- "${current_path}" ||
        die "RUNTIME_PATH_ACL_APPLY_FAILED: unable to grant ${acl_user} traverse access to ${current_path}."
    fi
    record_managed_path_acl "${acl_user}" "${current_path}"
    runuser -u "${acl_user}" -- test -x "${current_path}" ||
      die "RUNTIME_PATH_TRAVERSAL_DENIED: ${acl_user} still cannot traverse ${current_path} after applying the managed ACL."
  done
}
verify_runtime_path_access() {
  require_command runuser
  runuser -u "${run_user}" -- test -x "${install_dir}/bin/mongod" ||
    die "RUNTIME_BINARY_PERMISSION_DENIED: ${run_user} cannot execute the managed mongod binary."
  runuser -u "${run_user}" -- test -r "${data_dir}" ||
    die "DATA_DIRECTORY_PERMISSION_DENIED: ${run_user} cannot read ${data_dir}."
  runuser -u "${run_user}" -- test -w "${data_dir}" ||
    die "DATA_DIRECTORY_PERMISSION_DENIED: ${run_user} cannot write ${data_dir}."
  runuser -u "${run_user}" -- test -w "${log_dir}" ||
    die "LOG_DIRECTORY_PERMISSION_DENIED: ${run_user} cannot write ${log_dir}."
}
remove_managed_path_acl_entries() {
  local records_file="$1" acl_user acl_path expected_permissions current_permissions
  [[ -f "${records_file}" ]] || return 0
  if ! command -v getfacl >/dev/null 2>&1 || ! command -v setfacl >/dev/null 2>&1; then
    printf 'WARNING: managed MongoDB path ACL could not be restored because ACL utilities are unavailable.\n' >&2
    return 0
  fi
  while IFS=$'\t' read -r acl_user acl_path expected_permissions; do
    [[ "${acl_user}" =~ ^[a-z_][a-z0-9_-]{0,30}$ && "${acl_path}" == /* && "${acl_path}" != / && -d "${acl_path}" && ! -L "${acl_path}" ]] || continue
    expected_permissions="${expected_permissions:---x}"
    current_permissions="$(getfacl -cp -- "${acl_path}" 2>/dev/null | awk -F: -v user="${acl_user}" '$1 == "user" && $2 == user {print $3; exit}' || true)"
    if [[ "${current_permissions}" == "${expected_permissions}" ]]; then
      setfacl --no-mask -x "u:${acl_user}" -- "${acl_path}" ||
        printf 'WARNING: managed MongoDB path ACL could not be restored for %s.\n' "${acl_path}" >&2
    elif [[ -n "${current_permissions}" ]]; then
      printf 'WARNING: managed MongoDB path ACL for %s was changed externally and was preserved.\n' "${acl_path}" >&2
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
}
verify_checksum() { printf '%s  %s\n' "$2" "$1" | sha256sum --check --status; }
gpg_command() { command -v gpg >/dev/null 2>&1 && printf gpg || { command -v gpg2 >/dev/null 2>&1 && printf gpg2; }; }
verify_signature() {
  local archive="$1" signature="$2" key_file="$3" expected_key="$4" gpg_cmd home actual status marker result_marker signing_fingerprint primary_fingerprint result
  gpg_cmd="$(gpg_command || true)"; [[ -n "${gpg_cmd}" ]] || die "GPG is required to verify MongoDB release signatures."
  home="$(mktemp -d)"; chmod 0700 "${home}"
  "${gpg_cmd}" --batch --homedir "${home}" --import "${key_file}" >/dev/null 2>&1 || { rm -rf -- "${home}"; die "Release signing key import failed."; }
  actual="$("${gpg_cmd}" --batch --homedir "${home}" --with-colons --fingerprint 2>/dev/null | awk -F: '$1=="fpr" {print $10}')"
  grep -Fxq "${expected_key}" <<<"${actual}" || { rm -rf -- "${home}"; die "Release signing key fingerprint verification failed."; }
  status="$("${gpg_cmd}" --batch --homedir "${home}" --status-fd=1 --verify "${signature}" "${archive}" 2>/dev/null)" || { rm -rf -- "${home}"; die "Release signature verification failed."; }
  rm -rf -- "${home}"
  result=false
  while IFS=' ' read -r marker result_marker signing_fingerprint _ _ _ _ _ _ _ _ primary_fingerprint _; do
    [[ "${marker}" == '[GNUPG:]' && "${result_marker}" == VALIDSIG ]] || continue
    if [[ "${signing_fingerprint}" == "${expected_key}" || "${primary_fingerprint}" == "${expected_key}" ]]; then result=true; fi
  done <<<"${status}"
  [[ "${result}" == true ]] || die "Release signature was not made by the pinned publisher key."
}
fetch_url() { curl --proto '=https' --tlsv1.2 --fail --location --retry 3 --connect-timeout 20 --output "$1" "$2"; }
obtain_verified_artifact() {
  local name="$1" url="$2" signature_url="$3" checksum="$4" key_url="$5" key_checksum="$6" fingerprint="$7" destination="$8"
  local cache_dir="/var/cache/oneinstack/mongodb" source signature key_file temp_dir
  if [[ "${install_mode}" == offline ]]; then
    source="${offline_package_path}/artifacts/${host_arch}/${name}"
    signature="${source}.sig"
    key_file="${offline_package_path}/keys/$(basename -- "${key_url}")"
  else
    install -d -m 0750 -- "${cache_dir}"
    source="${cache_dir}/${name}"; signature="${source}.sig"; key_file="${cache_dir}/$(basename -- "${key_url}")"
    if [[ ! -f "${source}" ]] || ! verify_checksum "${source}" "${checksum}" || [[ ! -f "${signature}" || ! -f "${key_file}" ]]; then
      temp_dir="$(mktemp -d "${cache_dir}/download.XXXXXX")"
      fetch_url "${temp_dir}/${name}" "${url}"
      fetch_url "${temp_dir}/${name}.sig" "${signature_url}"
      fetch_url "${temp_dir}/$(basename -- "${key_url}")" "${key_url}"
      verify_checksum "${temp_dir}/${name}" "${checksum}" || die "CHECKSUM_MISMATCH: ${name}."
      verify_checksum "${temp_dir}/$(basename -- "${key_url}")" "${key_checksum}" || die "CHECKSUM_MISMATCH: release key."
      verify_signature "${temp_dir}/${name}" "${temp_dir}/${name}.sig" "${temp_dir}/$(basename -- "${key_url}")" "${fingerprint}"
      install -m 0640 -- "${temp_dir}/${name}" "${source}"
      install -m 0640 -- "${temp_dir}/${name}.sig" "${signature}"
      install -m 0640 -- "${temp_dir}/$(basename -- "${key_url}")" "${key_file}"
      rm -rf -- "${temp_dir}"
    fi
  fi
  [[ -f "${source}" && -f "${signature}" && -f "${key_file}" ]] || die "OFFLINE_BUNDLE_INCOMPLETE: missing ${name}, signature, or release key."
  verify_checksum "${source}" "${checksum}" || die "CHECKSUM_MISMATCH: ${name}."
  verify_checksum "${key_file}" "${key_checksum}" || die "CHECKSUM_MISMATCH: release key."
  verify_signature "${source}" "${signature}" "${key_file}" "${fingerprint}"
  cp -- "${source}" "${destination}"
}
validate_bundle_inventory() {
  local inventory listed digest relative
  inventory="$(mktemp)"; listed="$(mktemp)"
  find "${offline_package_path}" ! -type d ! -type f -print -quit | grep -q . && die "OFFLINE_BUNDLE_INVALID: special files are forbidden."
  (cd -- "${offline_package_path}" && find . -type f ! -name files.sha256 -print | sed 's#^./##' | sort) >"${inventory}"
  while read -r digest relative; do
    [[ "${digest}" =~ ^[0-9a-f]{64}$ && -n "${relative}" && "${relative}" != /* && "${relative}" != ../* && "${relative}" != */../* ]] ||
      die "OFFLINE_BUNDLE_INVALID: files.sha256 contains an unsafe entry."
    printf '%s\n' "${relative}"
  done <"${offline_package_path}/files.sha256" | sort >"${listed}"
  cmp -s "${inventory}" "${listed}" || die "OFFLINE_BUNDLE_INVALID: files.sha256 inventory does not match Bundle contents."
  rm -f -- "${inventory}" "${listed}"
}
validate_offline_bundle() {
  [[ "${install_mode}" == offline ]] || return 0
  [[ -f "${offline_package_path}/bundle-info" && -f "${offline_package_path}/files.sha256" ]] || die "OFFLINE_BUNDLE_INVALID: metadata is missing."
  find "${offline_package_path}" -type l -print -quit | grep -q . && die "OFFLINE_BUNDLE_INVALID: symbolic links are forbidden."
  validate_bundle_inventory
  (cd -- "${offline_package_path}" && sha256sum --check --strict files.sha256) >/dev/null || die "OFFLINE_BUNDLE_CHECKSUM_MISMATCH: Bundle digest verification failed."
  local key value component="" bundle_package="" bundle_software="" bundle_os="" bundle_version="" bundle_arch=""
  while IFS='=' read -r key value; do
    case "${key}" in
      component) component="${value}" ;; packageVersion) bundle_package="${value}" ;; softwareVersion) bundle_software="${value}" ;;
      osId) bundle_os="${value}" ;; osVersion) bundle_version="${value}" ;; architecture) bundle_arch="${value}" ;;
      *) die "OFFLINE_BUNDLE_INVALID: unknown bundle-info field ${key}." ;;
    esac
  done <"${offline_package_path}/bundle-info"
  [[ "${component}" == mongodb && "${bundle_package}" == "${package_version}" && "${bundle_software}" == "${software_version}" &&
    "${bundle_os}" == "${system_id}" && "${bundle_version}" == "${system_version}" && "${bundle_arch}" == "${host_arch}" ]] ||
    die "OFFLINE_BUNDLE_PLATFORM_MISMATCH: Bundle identity does not match this request and host."
  local package_root="${offline_package_path}/packages/${system_id}/${system_version}/${host_arch}"
  [[ -d "${package_root}" ]] || die "OFFLINE_DEPENDENCY_MISSING: dependency directory is absent."
  case "${package_manager}" in
    apt)
      find "${package_root}" -maxdepth 1 -type f -name 'acl_*.deb' -print -quit | grep -q . ||
        die "OFFLINE_DEPENDENCY_MISSING: ACL package is required for MongoDB runtime path permissions."
      ;;
    dnf|yum|zypper)
      find "${package_root}" -maxdepth 1 -type f -name 'acl-[0-9]*.rpm' -print -quit | grep -q . ||
        die "OFFLINE_DEPENDENCY_MISSING: ACL package is required for MongoDB runtime path permissions."
      ;;
  esac
}
install_dependencies() {
  if [[ "${install_mode}" == offline ]]; then
    local package_root="${offline_package_path}/packages/${system_id}/${system_version}/${host_arch}"
    local -a packages=()
    [[ -d "${package_root}" ]] || die "OFFLINE_DEPENDENCY_MISSING: dependency directory is absent."
    case "${package_manager}" in
      apt)
        mapfile -t packages < <(find "${package_root}" -maxdepth 1 -type f -name '*.deb' -print | sort)
        ((${#packages[@]} > 0)) || die "OFFLINE_DEPENDENCY_MISSING: no .deb files were bundled."
        DEBIAN_FRONTEND=noninteractive dpkg -i "${packages[@]}" || die "OFFLINE_DEPENDENCY_MISSING: local .deb dependency installation failed."
        ;;
      dnf|yum)
        mapfile -t packages < <(find "${package_root}" -maxdepth 1 -type f -name '*.rpm' -print | sort)
        ((${#packages[@]} > 0)) || die "OFFLINE_DEPENDENCY_MISSING: no .rpm files were bundled."
        "${package_manager}" --disablerepo='*' --setopt=install_weak_deps=False install -y "${packages[@]}" ||
          die "OFFLINE_DEPENDENCY_MISSING: local RPM dependency installation failed."
        ;;
      zypper)
        mapfile -t packages < <(find "${package_root}" -maxdepth 1 -type f -name '*.rpm' -print | sort)
        ((${#packages[@]} > 0)) || die "OFFLINE_DEPENDENCY_MISSING: no .rpm files were bundled."
        zypper --non-interactive --no-refresh install --no-recommends "${packages[@]}" ||
          die "OFFLINE_DEPENDENCY_MISSING: local SLES dependency installation failed."
        ;;
    esac
    return
  fi
  case "${package_manager}" in
    apt)
      export DEBIAN_FRONTEND=noninteractive
      apt-get update
      local -a packages=(acl ca-certificates curl gnupg gzip iproute2 openssl tar libcurl4 liblzma5 libgssapi-krb5-2 libwrap0 libsasl2-2 libsasl2-modules libsasl2-modules-gssapi-mit)
      local ldap_package="libldap-2.5-0"
      if [[ "${system_id}" == ubuntu && "${system_version}" == 20.04 ]]; then
        ldap_package="libldap-2.4-2"
      elif apt-cache policy libldap2 2>/dev/null | awk '/Candidate:/ {found=1; if ($2 != "(none)") available=1} END {exit !(found && available)}'; then
        ldap_package="libldap2"
      fi
      packages+=("${ldap_package}")
      apt-get install -y --no-install-recommends "${packages[@]}"
      ;;
    dnf|yum) "${package_manager}" install -y acl ca-certificates cyrus-sasl cyrus-sasl-gssapi cyrus-sasl-plain curl gnupg2 gzip krb5-libs libcurl libpcap openldap openssl-libs tar xz-libs ;;
    zypper) zypper --non-interactive --gpg-auto-import-keys install acl ca-certificates cyrus-sasl curl gpg2 gzip krb5 libcurl4 libldap-2_4-2 libopenssl3 libpcap1 libwrap0 libzio1 tar xz ;;
  esac
}
service_is_active() { systemctl is-active --quiet mongod.service 2>/dev/null; }
service_start() { systemctl daemon-reload; systemctl enable --now mongod.service; }
service_stop() { systemctl stop mongod.service 2>/dev/null || true; }
service_restart() { systemctl daemon-reload; systemctl restart mongod.service; }
write_unit() {
  local temporary; temporary="$(mktemp /etc/systemd/system/.mongod.service.XXXXXX)"
  cat >"${temporary}" <<EOF
[Unit]
Description=MongoDB Database Server (Oneinstack managed)
After=network-online.target
Wants=network-online.target
[Service]
Type=simple
User=${run_user}
Group=${run_group}
ExecStart=${install_dir}/bin/mongod --config ${config_file}
RuntimeDirectory=mongodb
RuntimeDirectoryMode=0750
Restart=on-failure
RestartSec=5
LimitNOFILE=64000
LimitNPROC=64000
PrivateTmp=true
[Install]
WantedBy=multi-user.target
EOF
  chmod 0644 "${temporary}"; mv -f -- "${temporary}" "${unit_file}"
}
write_mongod_config() {
  local destination="$1" bind="$2" port="$3" auth="$4" max_connections="${5:-0}" cache_gb="${6:-0}" profile_mode="${7:-off}" slow_ms="${8:-100}"
  cat >"${destination}" <<EOF
# Managed by Oneinstack MongoDB component
storage:
  dbPath: "${data_dir}"
EOF
  if [[ "${cache_gb}" -gt 0 ]]; then
    cat >>"${destination}" <<EOF
  wiredTiger:
    engineConfig:
      cacheSizeGB: ${cache_gb}
EOF
  fi
  cat >>"${destination}" <<EOF
systemLog:
  destination: file
  path: "${log_dir}/mongod.log"
  logAppend: true
net:
  port: ${port}
  bindIp: "${bind}"
EOF
  if [[ "${max_connections}" -gt 0 ]]; then
    printf '  maxIncomingConnections: %s\n' "${max_connections}" >>"${destination}"
  fi
  cat >>"${destination}" <<EOF
processManagement:
  fork: false
security:
  authorization: ${auth}
operationProfiling:
  mode: ${profile_mode}
  slowOpThresholdMs: ${slow_ms}
EOF
  chmod 0640 "${destination}"; chown root:"${run_group}" "${destination}"
}
validate_native_config() { "${install_dir}/bin/mongod" --config "$1" --outputConfig >/dev/null 2>&1 || die "CONFIG_INVALID: mongod rejected the candidate configuration."; }
wait_for_mongodb() {
  local port="$1" bind="${2:-127.0.0.1}" count probe_host
  probe_host="${bind%%,*}"
  case "${probe_host}" in 0.0.0.0) probe_host=127.0.0.1 ;; ::) probe_host=::1 ;; esac
  for count in $(seq 1 120); do
    service_is_active || die "SERVICE_FAILED: mongod exited before becoming ready."
    if port_is_listening "${port}" && "${install_dir}/bin/mongosh" --quiet --host "${probe_host}" --port "${port}" --norc --eval 'quit(db.runCommand({ping:1}).ok === 1 ? 0 : 1)' >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
  done
  die "SERVICE_TIMEOUT: MongoDB did not become healthy on port ${port}."
}
prepare_rollback() {
  install -d -m 0750 -- "${state_dir}"
  [[ ! -d "${rollback_dir}" ]] || die "ROLLBACK_PENDING: an incomplete MongoDB transaction already exists."
  install -d -m 0700 -- "${rollback_dir}"
  : >"${rollback_dir}/transaction-started"
  service_is_active && { : >"${rollback_dir}/was-active"; service_stop; }
  [[ ! -e "${install_dir}" ]] || mv -- "${install_dir}" "${rollback_dir}/install"
  [[ ! -e "${config_file}" ]] || cp -a -- "${config_file}" "${rollback_dir}/mongod.conf"
  [[ ! -e "${unit_file}" ]] || cp -a -- "${unit_file}" "${rollback_dir}/mongod.service"
  [[ ! -e "${install_parameters_file}" ]] || cp -a -- "${install_parameters_file}" "${rollback_dir}/install-parameters"
  [[ ! -e "${state_dir}/version" ]] || cp -a -- "${state_dir}/version" "${rollback_dir}/version"
  [[ ! -e "${state_dir}/password-configured" ]] || cp -a -- "${state_dir}/password-configured" "${rollback_dir}/password-configured"
}
restore_rollback() {
  [[ -f "${rollback_dir}/transaction-started" ]] || { echo "MongoDB rollback skipped because no transaction snapshot exists."; return 0; }
  service_stop
  [[ ! -e "${install_dir}" ]] || rm -rf -- "${install_dir}"
  [[ ! -e "${rollback_dir}/install" ]] || mv -- "${rollback_dir}/install" "${install_dir}"
  if [[ -e "${rollback_dir}/mongod.conf" ]]; then cp -a -- "${rollback_dir}/mongod.conf" "${config_file}"; else rm -f -- "${config_file}"; fi
  if [[ -e "${rollback_dir}/mongod.service" ]]; then cp -a -- "${rollback_dir}/mongod.service" "${unit_file}"; else rm -f -- "${unit_file}"; fi
  for name in install-parameters version password-configured; do
    if [[ -e "${rollback_dir}/${name}" ]]; then cp -a -- "${rollback_dir}/${name}" "${state_dir}/${name}"; else rm -f -- "${state_dir}/${name}"; fi
  done
  if [[ -f "${rollback_dir}/new-data" && -d "${data_dir}" ]]; then
    mv -- "${data_dir}" "${state_dir}/failed-data-$(date -u +%Y%m%dT%H%M%SZ)"
  fi
  restore_transaction_path_acl
  systemctl daemon-reload
  [[ ! -f "${rollback_dir}/was-active" ]] || service_start
  rm -rf -- "${rollback_dir}"
}
commit_transaction() { rm -rf -- "${rollback_dir}"; rm -f -- "${retained_data_file}"; }
actual_version() { "${install_dir}/bin/mongod" --version | awk '/db version/ {sub(/^v/,"",$3); print $3; exit}'; }
config_scalar_file() {
  local source_file="$1" section="$2" key="$3" fallback="$4" value
  value="$(awk -v section="${section}" -v key="${key}" '
    /^[^[:space:]#][^:]*:[[:space:]]*$/ {current=$1; sub(/:$/, "", current); next}
    current==section && $0 ~ "^[[:space:]]+" key ":[[:space:]]*" {line=$0; sub("^[[:space:]]+" key ":[[:space:]]*", "", line); sub(/[[:space:]]+#.*/, "", line); sub(/^"/, "", line); sub(/"$/, "", line); print line; exit}
  ' "${source_file}")"
  printf '%s' "${value:-${fallback}}"
}
config_scalar() { config_scalar_file "${config_file}" "$1" "$2" "$3"; }
