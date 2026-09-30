#!/usr/bin/env bash
set -Eeuo pipefail
umask 027

component_id="redis"
component_version="1.0.17"
software_version="${SOFTWARE_VERSION:-7.4.8}"
install_dir="${INSTALL_DIR:-/usr/local/redis}"
data_dir="${DATA_DIR:-/data/redis}"
redis_port="${REDIS_PORT:-6379}"
redis_bind="${REDIS_BIND:-127.0.0.1 ::1}"
redis_username="${REDIS_USERNAME:-default}"
redis_password="${REDIS_PASSWORD:-}"
state_root="${ONEINSTACK_COMPONENT_STATE:-/var/lib/oneinstack/components}"
state_dir="${state_root}/${component_id}"
if [[ -z "${INSTALL_DIR+x}" && -r "${state_dir}/install-dir" ]]; then read -r install_dir <"${state_dir}/install-dir"; fi
if [[ -z "${DATA_DIR+x}" && -r "${state_dir}/data-dir" ]]; then read -r data_dir <"${state_dir}/data-dir"; fi
rollback_dir="${state_dir}/rollback"
external_migration_dir="${rollback_dir}/external"
managed_path_acl_file="${state_dir}/managed-path-acl"
transaction_path_acl_file="${rollback_dir}/path-acl-added"
unit_file="/etc/systemd/system/redis.service"
service_name="redis.service"
install_mode="${ONEINSTACK_INSTALL_MODE:-center}"
offline_package_path="${ONEINSTACK_OFFLINE_PACKAGE_PATH:-}"
host_arch=""
system_id=""
system_version=""
package_manager=""
source_url=""
source_sha256=""
source_archive=""
source_path=""
source_temp_path=""
work_dir=""

die() { printf 'ERROR: %s\n' "$*" >&2; exit 1; }
require_command() { command -v "$1" >/dev/null 2>&1 || die "Required command not found: $1"; }
emit_progress() {
  local percent="$1" code="$2" message="$3" fd="${ONEINSTACK_PROGRESS_FD:-}"
  [[ "${fd}" =~ ^[0-9]+$ ]] || return 0
  message="${message//\\/\\\\}"; message="${message//\"/\\\"}"; message="${message//$'\n'/ }"
  { printf '{"type":"progress","percent":%s,"code":"%s","message":"%s"}\n' \
      "${percent}" "${code}" "${message}" >&"${fd}"; } 2>/dev/null || true
}
require_root() { [[ "$(id -u)" -eq 0 ]] || die "This action must run as root."; }

validate_path() {
  local value="$1" label="$2"
  [[ -n "${value}" && "${value}" == /* && "${value}" != *[[:space:]]* &&
    "$(realpath -m -- "${value}")" == "${value}" ]] ||
    die "${label} must be a normalized absolute path."
  case "${value}" in
    /|/usr|/usr/local|/etc|/var|/data|/home|/root|/var/lib/oneinstack)
      die "${label} is too broad: ${value}" ;;
  esac
}

path_is_inside() {
  local child="$1" parent="$2"
  [[ "${child}" == "${parent}" || "${child}" == "${parent}/"* ]]
}

source_for_version() {
  case "${software_version}" in
    7.4.8)
      source_url="https://download.redis.io/releases/redis-7.4.8.tar.gz"
      source_sha256="f6773cb7d63be236c59c2917a82f1f08e47b77d89b2f0c9f53becb22b8ea4172"
      ;;
    8.4.0)
      source_url="https://download.redis.io/releases/redis-8.4.0.tar.gz"
      source_sha256="ca909aa15252f2ecb3a048cd086469827d636bf8334f50bb94d03fba4bfc56e8"
      ;;
    *) die "Unsupported Redis software version: ${software_version}" ;;
  esac
  source_archive="redis-${software_version}.tar.gz"
}

validate_bind() {
  local address
  local -a addresses=()
  read -r -a addresses <<<"${redis_bind}"
  ((${#addresses[@]} > 0)) || die "REDIS_BIND must contain at least one address."
  for address in "${addresses[@]}"; do
    [[ "${address}" =~ ^[0-9A-Fa-f:.]+$ ]] || die "REDIS_BIND contains an invalid address."
  done
}

parameter_explicit() {
  local key="$1"
  local env_key="ONEINSTACK_PARAMETER_${key}_EXPLICIT"
  [[ "${!env_key:-}" == "true" ]]
}

validate_inputs() {
  [[ -n "${software_version}" ]] || die "SOFTWARE_VERSION is required."
  source_for_version
  [[ "${install_mode}" == "center" || "${install_mode}" == "offline" ]] ||
    die "ONEINSTACK_INSTALL_MODE must be center or offline."
  if [[ "${install_mode}" == "offline" ]]; then
    [[ -n "${offline_package_path}" && "${offline_package_path}" == /* &&
      "$(realpath -m -- "${offline_package_path}")" == "${offline_package_path}" ]] ||
      die "Offline installation requires a normalized absolute Bundle path."
  elif [[ -n "${offline_package_path}" ]]; then
    die "Offline Bundle path cannot be used in Center mode."
  fi
  [[ "${redis_port}" =~ ^[0-9]+$ && "${redis_port}" -ge 1 && "${redis_port}" -le 65535 ]] ||
    die "Invalid REDIS_PORT."
  validate_bind
  [[ "${redis_username}" =~ ^[A-Za-z0-9._-]{1,64}$ ]] ||
    die "REDIS_USERNAME must contain 1-64 letters, numbers, dots, underscores, or hyphens."
  if [[ "${redis_username}" != "default" && -z "${redis_password}" ]]; then
    die "REDIS_PASSWORD is required when REDIS_USERNAME is not default."
  fi
  [[ -z "${redis_password}" || "${redis_password}" =~ ^[A-Za-z0-9_@%+=:,.!#?-]{8,128}$ ]] ||
    die "REDIS_PASSWORD must be 8-128 safe characters."
  [[ "${source_sha256}" =~ ^[0-9a-f]{64}$ ]] || die "Redis source SHA-256 is invalid."
  validate_path "${install_dir}" INSTALL_DIR
  validate_path "${data_dir}" DATA_DIR
  validate_path "${state_root}" ONEINSTACK_COMPONENT_STATE
  if path_is_inside "${data_dir}" "${install_dir}"; then
    die "DATA_DIR cannot be inside INSTALL_DIR."
  fi
  if path_is_inside "${install_dir}" "${data_dir}"; then
    die "INSTALL_DIR cannot be inside DATA_DIR."
  fi
}

map_host_arch() {
  case "$(uname -m)" in
    x86_64) host_arch="amd64" ;;
    aarch64|arm64) host_arch="arm64" ;;
    *) die "Redis supports amd64 and arm64 hosts only." ;;
  esac
}

select_package_manager() {
  case "${system_id}" in
    ubuntu|debian) package_manager="apt" ;;
    centos)
      if [[ "${system_version%%.*}" == "7" ]]; then package_manager="yum"; else package_manager="dnf"; fi
      ;;
    rhel|rocky|almalinux|ol|fedora|amzn) package_manager="dnf" ;;
    sles|opensuse-leap|opensuse-tumbleweed|opensuse) package_manager="zypper" ;;
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
    sles:*|opensuse-leap:*|opensuse-tumbleweed:*|opensuse:*) ;;
    *) die "Unsupported Linux release: ${system_id:-unknown} ${system_version:-unknown}" ;;
  esac
  map_host_arch
  select_package_manager
}

required_base_commands() {
  printf '%s\n' awk df dirname find grep install mktemp nproc realpath sed sha256sum stat systemctl tar
}

check_base_commands() {
  local command_name
  while IFS= read -r command_name; do require_command "${command_name}"; done < <(required_base_commands)
}

dependency_packages() {
  case "${package_manager}" in
    apt) printf '%s\n' acl build-essential ca-certificates curl gzip libssl-dev pkg-config tar xz-utils ;;
    dnf|yum) printf '%s\n' acl ca-certificates curl gcc gcc-c++ gzip make openssl-devel pkgconfig tar xz ;;
    zypper) printf '%s\n' acl ca-certificates curl gcc gcc-c++ gzip libopenssl-devel make pkg-config tar xz ;;
    *) die "Unsupported package manager: ${package_manager}" ;;
  esac
}

docker_apt_sources() {
  local source_file
  for source_file in /etc/apt/sources.list /etc/apt/sources.list.d/*; do
    [[ -f "${source_file}" ]] || continue
    if grep -Eiq '^[[:space:]]*(deb|URIs:).*download\.docker\.com/linux/' "${source_file}" 2>/dev/null; then
      printf '%s\n' "${source_file}"
    fi
  done
}

copy_apt_source_without_docker() {
  local source_file="$1" target_file="$2"
  case "${source_file}" in
    *.list)
      sed -E '/^[[:space:]]*deb(-src)?([[:space:]]|\[).*download\.docker\.com\/linux\//d' \
        "${source_file}" >"${target_file}"
      ;;
    *.sources)
      awk 'BEGIN { RS=""; ORS="\n\n" } $0 !~ /download\.docker\.com\/linux\// { print }' \
        "${source_file}" >"${target_file}"
      ;;
    *) cp -a -- "${source_file}" "${target_file}" ;;
  esac
}

apt_update_without_docker_source() (
  set -Eeuo pipefail
  local source_dir source_parts source_list source_file target_file
  source_dir="$(mktemp -d)"
  trap 'rm -rf -- "${source_dir}"' EXIT
  source_list="${source_dir}/sources.list"
  source_parts="${source_dir}/sources.list.d"
  install -d -m 0755 -- "${source_parts}"
  if [[ -f /etc/apt/sources.list ]]; then
    copy_apt_source_without_docker /etc/apt/sources.list "${source_list}"
  else
    : >"${source_list}"
  fi
  for source_file in /etc/apt/sources.list.d/*.list /etc/apt/sources.list.d/*.sources; do
    [[ -f "${source_file}" ]] || continue
    target_file="${source_parts}/$(basename -- "${source_file}")"
    copy_apt_source_without_docker "${source_file}" "${target_file}"
  done
  apt-get update -o "Dir::Etc::sourcelist=${source_list}" -o "Dir::Etc::sourceparts=${source_parts}/"
)

install_dependencies_online() {
  local -a packages=()
  mapfile -t packages < <(dependency_packages)
  emit_progress 25 dependency.metadata.refreshing "正在刷新 ${package_manager} 依赖元数据"
  case "${package_manager}" in
    apt)
      if [[ -n "$(docker_apt_sources)" ]]; then apt_update_without_docker_source; else apt-get update; fi
      DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends "${packages[@]}"
      ;;
    dnf) dnf makecache --refresh -y >/dev/null; dnf install -y "${packages[@]}" ;;
    yum) yum makecache -y >/dev/null; yum install -y "${packages[@]}" ;;
    zypper) zypper --non-interactive refresh; zypper --non-interactive install --no-recommends "${packages[@]}" ;;
  esac
}

offline_package_dir() {
  [[ -n "${offline_package_path}" ]] || die "Offline installation requires ONEINSTACK_OFFLINE_PACKAGE_PATH."
  printf '%s/packages/%s/%s/%s\n' "${offline_package_path}" "${system_id}" "${system_version}" "${host_arch}"
}

validate_offline_bundle() {
  local package_dir
  [[ -d "${offline_package_path}" ]] || die "Offline Redis Bundle is unavailable."
  [[ -f "${offline_package_path}/manifest.yaml" ]] || die "Offline Bundle manifest is missing."
  [[ -f "${offline_package_path}/files.sha256" ]] || die "Offline Bundle checksum file is missing."
  package_dir="$(offline_package_dir)"
  [[ -d "${package_dir}" ]] || die "Offline dependency directory is missing for ${system_id} ${system_version} ${host_arch}."
  (cd "${offline_package_path}" && sha256sum -c files.sha256 --status) || die "Offline Bundle checksum verification failed."
  grep -Eq '^[[:space:]]+id:[[:space:]]+redis[[:space:]]*$' "${offline_package_path}/manifest.yaml" ||
    die "Offline Bundle component is not Redis."
  grep -Eq "^[[:space:]]+version:[[:space:]]+${component_version//./\\.}[[:space:]]*$" "${offline_package_path}/manifest.yaml" ||
    die "Offline Bundle package version does not match Redis ${component_version}."
}

install_dependencies_offline() {
  local package_dir package
  local -a packages=()
  package_dir="$(offline_package_dir)"
  mapfile -t packages < <(find "${package_dir}" -maxdepth 1 -type f \( -name '*.deb' -o -name '*.rpm' \) -print | sort)
  ((${#packages[@]} > 0)) || die "Offline dependency packages are missing."
  emit_progress 25 dependency.offline.installing "正在从离线 Bundle 安装主机依赖"
  case "${package_manager}" in
    apt) dpkg -i "${packages[@]}" || apt-get -y --no-download -f install ;;
    dnf|yum) "${package_manager}" --disablerepo='*' --cacheonly install -y "${packages[@]}" ;;
    zypper) zypper --non-interactive --no-refresh --no-gpg-checks install --allow-unsigned-rpm "${packages[@]}" ;;
  esac
  for package in "${packages[@]}"; do [[ -f "${package}" ]] || die "Offline package disappeared."; done
}

install_dependencies() {
  if [[ "${install_mode}" == "offline" ]]; then install_dependencies_offline; else install_dependencies_online; fi
  require_command make
  require_command cc
}

resolve_source() {
  local artifact destination actual
  artifact="${offline_package_path}/artifacts/${host_arch}/${source_archive}"
  if [[ "${install_mode}" == "offline" ]]; then
    [[ -f "${artifact}" ]] || die "Offline Redis source artifact is missing for ${software_version} ${host_arch}."
    source_path="${artifact}"
  else
    require_command curl
    destination="${work_dir}/${source_archive}"
    source_temp_path="${destination}.part"
    rm -f -- "${source_temp_path}"
    emit_progress 35 artifact.downloading "正在下载 Redis ${software_version} 源码"
    curl --proto '=https' --tlsv1.2 --fail --location --retry 3 --connect-timeout 20 \
      --output "${source_temp_path}" "${source_url}"
    mv -- "${source_temp_path}" "${destination}"
    source_temp_path=""
    source_path="${destination}"
  fi
  actual="$(sha256sum "${source_path}" | awk '{print $1}')"
  [[ "${actual}" == "${source_sha256}" ]] || die "Redis source SHA-256 mismatch."
  emit_progress 45 artifact.verified "Redis ${software_version} 源码摘要校验通过"
}

cleanup_work() {
  [[ -z "${source_temp_path}" || ! -e "${source_temp_path}" ]] || rm -f -- "${source_temp_path}"
  [[ -z "${work_dir}" || ! -d "${work_dir}" ]] || rm -rf -- "${work_dir}"
}

ensure_account() {
  getent group redis >/dev/null || groupadd --system redis
  id redis >/dev/null 2>&1 || useradd --system --gid redis --home-dir "${data_dir}" --shell /usr/sbin/nologin redis
}

record_managed_path_acl() {
  local acl_path="$1" record
  record="redis"$'\t'"${acl_path}"
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

ensure_data_path_access() {
  local current_path current_acl access_mask existing_user_permissions
  current_path="$(dirname -- "${data_dir}")"
  while [[ "${current_path}" != "/" ]]; do
    if runuser -u redis -- test -x "${current_path}"; then
      break
    fi
    [[ -d "${current_path}" ]] ||
      die "Redis cannot inspect the DATA_DIR parent path: ${current_path}."
    require_command getfacl
    require_command setfacl
    [[ "$(stat -c '%u' -- "${current_path}")" == 0 ]] ||
      die "Redis cannot safely grant traversal access because ${current_path} is not root-owned."
    current_acl="$(getfacl -cp -- "${current_path}")" ||
      die "Redis could not inspect the ACL policy on ${current_path}."
    existing_user_permissions="$(awk -F: -v user=redis '$1 == "user" && $2 == user {print $3; exit}' <<<"${current_acl}")"
    if [[ -n "${existing_user_permissions}" ]]; then
      [[ "${existing_user_permissions}" == *x* ]] ||
        die "Redis cannot safely replace the existing redis ACL on ${current_path}."
    else
      access_mask="$(awk -F: '$1 == "mask" && $2 == "" {print $3; exit}' <<<"${current_acl}")"
      if [[ -n "${access_mask}" ]]; then
        [[ "${access_mask}" == *x* ]] ||
          die "Redis cannot safely grant traversal access because the ACL mask on ${current_path} denies execute permission."
        setfacl --no-mask -m u:redis:--x -- "${current_path}" ||
          die "Redis could not grant its runtime user traversal access to ${current_path}."
      else
        setfacl -m u:redis:--x -- "${current_path}" ||
          die "Redis could not grant its runtime user traversal access to ${current_path}."
      fi
      record_managed_path_acl "${current_path}"
      emit_progress 15 permissions.data_root.applied "已为 Redis 运行账户授予数据目录父路径最小穿越权限"
    fi
    current_path="$(dirname -- "${current_path}")"
  done
  runuser -u redis -- test -x "$(dirname -- "${data_dir}")" ||
    die "Redis runtime user cannot traverse the DATA_DIR parent path."
}

remove_managed_path_acl_entries() {
  local records_file="$1" acl_user acl_path current_permissions
  [[ -f "${records_file}" ]] || return 0
  if ! command -v getfacl >/dev/null 2>&1 || ! command -v setfacl >/dev/null 2>&1; then
    printf 'WARNING: managed Redis path ACL could not be restored because ACL tools are unavailable.\n' >&2
    return 0
  fi
  while IFS=$'\t' read -r acl_user acl_path; do
    [[ "${acl_user}" == redis && "${acl_path}" == /* && "${acl_path}" != "/" ]] || continue
    [[ -d "${acl_path}" ]] || continue
    current_permissions="$(getfacl -cp -- "${acl_path}" 2>/dev/null | awk -F: -v user="${acl_user}" '$1 == "user" && $2 == user {print $3; exit}' || true)"
    if [[ "${current_permissions}" == "--x" ]]; then
      setfacl --no-mask -x "u:${acl_user}" -- "${acl_path}" ||
        printf 'WARNING: managed Redis path ACL could not be restored for %s.\n' "${acl_path}" >&2
    elif [[ -n "${current_permissions}" ]]; then
      printf 'WARNING: managed Redis path ACL for %s was changed externally and was preserved.\n' "${acl_path}" >&2
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

normalize_runtime_permissions() {
  ensure_account
  install -d -o redis -g redis -m 0750 -- "${data_dir}"
  chown -R redis:redis "${data_dir}"
  chmod 0750 "${data_dir}"
  chmod 0755 "${install_dir}" "${install_dir}/bin"
  chown root:redis "${install_dir}/etc/redis.conf"
  chmod 0640 "${install_dir}/etc/redis.conf"
}

validate_redis_config() (
  set -Eeuo pipefail
  local candidate="$1"
  local validation_dir redis_pid="" validation_log validation_output
  validation_dir="$(mktemp -d "${TMPDIR:-/tmp}/oneinstack-redis-config.XXXXXX")"
  validation_log="${validation_dir}/redis.log"
  validation_output="${validation_dir}/redis-output.log"
  : >"${validation_log}"

  # shellcheck disable=SC2329
  cleanup() {
    local code="$?"
    set +e
    if [[ -n "${redis_pid}" ]] && kill -0 "${redis_pid}" 2>/dev/null; then
      kill -TERM "${redis_pid}" 2>/dev/null || true
      wait "${redis_pid}" 2>/dev/null || true
    fi
    rm -rf -- "${validation_dir}"
    return "${code}"
  }
  trap cleanup EXIT

  "${install_dir}/bin/redis-server" "${candidate}" \
    --daemonize no \
    --supervised no \
    --port 0 \
    --tls-port 0 \
    --tls-replication no \
    --tls-cluster no \
    --unixsocket "${validation_dir}/redis.sock" \
    --pidfile "${validation_dir}/redis.pid" \
    --dir "${validation_dir}" \
    --dbfilename redis-validation.rdb \
    --save "" \
    --appendonly no \
    --logfile "${validation_log}" \
    >"${validation_output}" 2>&1 &
  redis_pid="$!"

  for _ in {1..100}; do
    if grep -Fq "Ready to accept connections" "${validation_log}"; then
      kill -TERM "${redis_pid}" 2>/dev/null || true
      wait "${redis_pid}" 2>/dev/null || true
      return 0
    fi
    if ! kill -0 "${redis_pid}" 2>/dev/null; then
      wait "${redis_pid}" 2>/dev/null || true
      return 1
    fi
    sleep 0.1
  done

  kill -TERM "${redis_pid}" 2>/dev/null || true
  wait "${redis_pid}" 2>/dev/null || true
  return 1
)

runtime_config_value() {
  local key="$1" value
  [[ -r "${install_dir}/etc/redis.conf" ]] || return 1
  value="$(awk -v key="${key}" '$1 == key { $1=""; sub(/^[[:space:]]+/, ""); value=$0 } END { if (value == "") exit 1; print value }' "${install_dir}/etc/redis.conf")"
  printf '%s' "${value}"
}

runtime_config_username() {
  [[ -r "${install_dir}/etc/redis.conf" ]] || return 1
  awk '$1 == "user" && $2 != "default" && $2 != "" { print $2; found=1; exit } END { if (!found) print "default" }' \
    "${install_dir}/etc/redis.conf"
}

runtime_config_acl_password() {
  local username="$1"
  [[ -r "${install_dir}/etc/redis.conf" ]] || return 1
  awk -v username="${username}" '
    $1 == "user" && $2 == username {
      for (i = 3; i <= NF; i++) {
        if (substr($i, 1, 1) == ">") {
          print substr($i, 2)
          exit
        }
      }
    }
  ' "${install_dir}/etc/redis.conf"
}

load_runtime_probe_settings() {
  local value
  if ! parameter_explicit REDIS_PORT && value="$(runtime_config_value port 2>/dev/null)"; then
    redis_port="${value}"
  fi
  if ! parameter_explicit REDIS_BIND && value="$(runtime_config_value bind 2>/dev/null)"; then
    redis_bind="${value}"
  fi
  if ! parameter_explicit REDIS_USERNAME && value="$(runtime_config_username 2>/dev/null)"; then
    redis_username="${value}"
  fi
  if [[ -z "${redis_password}" ]] && value="$(runtime_config_value requirepass 2>/dev/null)"; then
    redis_password="${value}"
  fi
  if [[ -z "${redis_password}" ]] && value="$(runtime_config_acl_password "${redis_username}" 2>/dev/null)"; then
    redis_password="${value}"
  fi
  [[ "${redis_port}" =~ ^[0-9]+$ && "${redis_port}" -ge 1 && "${redis_port}" -le 65535 ]] || return 1
  validate_bind
}

verify_runtime_permissions() {
  require_command runuser
  runuser -u redis -- test -x "${install_dir}/bin/redis-server" || die "Redis runtime user cannot execute Redis."
  runuser -u redis -- test -r "${install_dir}/etc/redis.conf" || die "Redis runtime user cannot read the configuration."
  runuser -u redis -- test -x "$(dirname -- "${data_dir}")" || die "Redis runtime user cannot traverse the DATA_DIR parent path."
  runuser -u redis -- test -w "${data_dir}" || die "Redis runtime user cannot write the data directory."
}

installed_version() {
  if [[ -r "${state_dir}/version" ]]; then
    sed -n '1p' "${state_dir}/version"
  elif [[ -x "${install_dir}/bin/redis-server" ]]; then
    "${install_dir}/bin/redis-server" --version 2>/dev/null | sed -n 's/.*v=\([0-9][0-9.]*\).*/\1/p'
  fi
}

check_upgrade_direction() {
  local current
  current="$(installed_version || true)"
  [[ -z "${current}" || "${current}" == "${software_version}" ]] && return 0
  [[ "${current}" == "7.4.8" && "${software_version}" == "8.4.0" ]] ||
    die "Redis only supports the managed upgrade 7.4.8 to 8.4.0; downgrade is refused."
}

external_redis_data_present() {
  local data_file
  [[ -d /var/lib/redis/appendonlydir ]] || return 1
  data_file="$(find /var/lib/redis/appendonlydir -mindepth 1 -type f -print -quit 2>/dev/null || true)"
  [[ -n "${data_file}" ]]
}

external_redis_detected() {
  [[ ! -f "${state_dir}/version" ]] || return 1
  command -v redis-server >/dev/null 2>&1 && return 0
  [[ -f /etc/redis/redis.conf || -f /etc/redis/sentinel.conf ||
    -f /var/lib/redis/dump.rdb || -f /var/lib/redis/appendonly.aof ]] && return 0
  external_redis_data_present
}

snapshot_external_redis() {
  external_redis_detected || return 0
  command -v redis-server >/dev/null 2>&1 || die "External Redis binary cannot be identified."
  local external_version external_major external_data="/var/lib/redis" package
  external_version="$(redis-server --version | sed -n 's/.*v=\([0-9][0-9.]*\).*/\1/p')"
  external_major="${external_version%%.*}"
  [[ "${external_major}" =~ ^[0-9]+$ ]] || die "External Redis version cannot be determined."
  [[ "${external_major}" -le "${software_version%%.*}" ]] ||
    die "External Redis data format is newer than the target Redis version."
  if [[ -r /etc/redis/redis.conf ]]; then
    external_data="$(awk '$1 == "dir" {print $2; exit}' /etc/redis/redis.conf)"
    [[ -n "${external_data}" ]] || external_data="/var/lib/redis"
  fi
  external_data="$(realpath -m -- "${external_data}")"
  validate_path "${external_data}" EXTERNAL_DATA_DIR
  install -d -m 0700 -- "${external_migration_dir}"
  printf '%s\n' "${external_data}" >"${external_migration_dir}/data-path"
  printf '%s\n' "${package_manager}" >"${external_migration_dir}/package-manager"
  : >"${external_migration_dir}/package-versions"
  [[ -d /etc/redis ]] && cp -a -- /etc/redis "${external_migration_dir}/config"
  case "${package_manager}" in
    apt)
      for package in redis-server redis-tools redis; do
        dpkg-query -W -f='${Status}' "${package}" 2>/dev/null | grep -Fq 'install ok installed' || continue
        dpkg-query -W -f='${binary:Package}\t${Version}\n' "${package}" >>"${external_migration_dir}/package-versions"
      done
      ;;
    dnf|yum|zypper)
      rpm -qa --qf '%{NAME}\t%{VERSION}-%{RELEASE}\n' 'redis*' 2>/dev/null >"${external_migration_dir}/package-versions" || true
      ;;
  esac
  systemctl is-active --quiet redis-server 2>/dev/null && : >"${external_migration_dir}/redis-server-active" || true
  systemctl is-active --quiet redis 2>/dev/null && : >"${external_migration_dir}/redis-active" || true
  systemctl stop redis redis-server 2>/dev/null || true
  emit_progress 18 migration.snapshot.created "已有 Redis 配置、数据路径和服务状态已完成快照"
}

migrate_external_redis_data() {
  [[ -r "${external_migration_dir}/data-path" ]] || return 0
  local external_data
  read -r external_data <"${external_migration_dir}/data-path"
  [[ "${external_data}" != "${data_dir}" ]] || return 0
  [[ ! -e "${data_dir}/dump.rdb" && ! -d "${data_dir}/appendonlydir" ]] ||
    die "Target Redis data directory already contains persistent data."
  emit_progress 55 migration.data.copying "正在迁移已有 Redis 持久化数据"
  cp -a -- "${external_data}/." "${data_dir}/"
  chown -R redis:redis "${data_dir}"
}

commit_external_redis() {
  [[ -r "${external_migration_dir}/data-path" ]] || return 0
  local package_manager_snapshot package external_data
  read -r package_manager_snapshot <"${external_migration_dir}/package-manager"
  while IFS=$'\t' read -r package _version; do
    [[ -z "${package}" ]] && continue
    case "${package_manager_snapshot}" in
      apt) DEBIAN_FRONTEND=noninteractive apt-get remove -y "${package}" ;;
      dnf) dnf remove -y "${package}" ;;
      yum) yum remove -y "${package}" ;;
      zypper) zypper --non-interactive remove "${package}" ;;
    esac
  done <"${external_migration_dir}/package-versions"
  read -r external_data <"${external_migration_dir}/data-path"
  [[ "${external_data}" == "${data_dir}" ]] || rm -rf -- "${external_data}"
  rm -rf -- /etc/redis
  systemctl daemon-reload
  emit_progress 92 migration.commit.completed "已有 Redis 已切换到受管服务"
}

snapshot_managed_data() {
  [[ -f "${state_dir}/version" && -d "${data_dir}" ]] || return 0
  local data_size available_size backup_data
  data_size="$(du -sk -- "${data_dir}" | awk '{print $1}')"
  available_size="$(df -Pk -- "${rollback_dir}" 2>/dev/null | awk 'NR==2 {print $4}')"
  [[ "${data_size}" =~ ^[0-9]+$ && "${available_size}" =~ ^[0-9]+$ && "${available_size}" -ge "$((data_size + 524288))" ]] ||
    die "Insufficient disk space for the Redis upgrade data rollback snapshot."
  backup_data="${rollback_dir}/data"
  rm -rf -- "${backup_data}"
  emit_progress 12 upgrade.data.snapshot "正在创建 Redis 升级数据回滚快照"
  cp -a -- "${data_dir}" "${backup_data}"
}

prepare_rollback() {
  install -d -m 0750 -- "${state_dir}"
  rm -rf -- "${rollback_dir}"
  install -d -m 0750 -- "${rollback_dir}"
  : >"${rollback_dir}/transaction-started"
  chmod 0600 "${rollback_dir}/transaction-started"
  check_upgrade_direction
  if [[ -f "${state_dir}/version" ]]; then
    cp -a -- "${state_dir}/version" "${rollback_dir}/version"
    snapshot_managed_data
  fi
  if systemctl is-active --quiet "${service_name}" 2>/dev/null; then : >"${rollback_dir}/was-active"; systemctl stop "${service_name}"; fi
  [[ ! -e "${install_dir}" ]] || mv -- "${install_dir}" "${rollback_dir}/install"
  [[ ! -e "${unit_file}" ]] || cp -a -- "${unit_file}" "${rollback_dir}/redis.service"
  snapshot_external_redis
}

restore_rollback() {
  systemctl stop "${service_name}" 2>/dev/null || true
  [[ ! -e "${install_dir}" ]] || rm -rf -- "${install_dir}"
  [[ ! -e "${rollback_dir}/install" ]] || mv -- "${rollback_dir}/install" "${install_dir}"
  if [[ -d "${rollback_dir}/data" ]]; then
    rm -rf -- "${data_dir}"
    mv -- "${rollback_dir}/data" "${data_dir}"
    chown -R redis:redis "${data_dir}" 2>/dev/null || true
  fi
  if [[ -d "${external_migration_dir}/config" ]]; then
    rm -rf -- /etc/redis
    cp -a -- "${external_migration_dir}/config" /etc/redis
  fi
  if [[ -e "${rollback_dir}/redis.service" ]]; then cp -a -- "${rollback_dir}/redis.service" "${unit_file}"; else rm -f -- "${unit_file}"; fi
  systemctl daemon-reload
  [[ ! -e "${rollback_dir}/was-active" ]] || systemctl start "${service_name}"
  [[ ! -f "${external_migration_dir}/redis-server-active" ]] || systemctl start redis-server
  restore_transaction_path_acl
}

redis_probe_hosts() {
  local address
  local -a addresses=()
  read -r -a addresses <<<"${redis_bind}"
  for address in "${addresses[@]}"; do
    case "${address}" in
      127.0.0.1|0.0.0.0) printf '%s\n' 127.0.0.1 ;;
      ::1|::) printf '%s\n' ::1 ;;
      *) printf '%s\n' "${address}" ;;
    esac
  done
}

redis_runtime_probe() {
  local host response
  local -a hosts=()
  [[ -x "${install_dir}/bin/redis-cli" ]] || return 2
  mapfile -t hosts < <(redis_probe_hosts)
  for host in "${hosts[@]}"; do
    local -a cli_args=(--no-auth-warning --raw -t 1 -h "${host}" -p "${redis_port}")
    if [[ -n "${redis_password}" ]]; then
      cli_args+=(--user "${redis_username}")
      response="$(REDISCLI_AUTH="${redis_password}" "${install_dir}/bin/redis-cli" "${cli_args[@]}" ping 2>/dev/null || true)"
    else
      [[ "${redis_username}" == "default" ]] || return 2
      response="$("${install_dir}/bin/redis-cli" "${cli_args[@]}" ping 2>/dev/null || true)"
    fi
    if [[ "${response}" == "PONG" ]]; then
      return 0
    fi
    case "${response}" in
      NOAUTH*|WRONGPASS*|"ERR AUTH"*|"ERR invalid password"*) return 2 ;;
    esac
  done
  return 1
}

redis_service_state() {
  systemctl is-active "${service_name}" 2>/dev/null || true
}

redis_start_failure_diagnostics() {
  local state
  state="$(redis_service_state)"
  printf 'Redis service state: %s\n' "${state:-unknown}" >&2
  systemctl status "${service_name}" --no-pager --lines=20 >&2 || true
  journalctl -u "${service_name}" --no-pager --lines=30 >&2 || true
}

wait_for_redis_ready() {
  local attempts="${1:-180}" interval="${2:-0.5}" index state probe_result
  for ((index=0; index<attempts; index++)); do
    state="$(redis_service_state)"
    if [[ "${state}" == "active" ]]; then
      if redis_runtime_probe; then
        return 0
      else
        probe_result="$?"
      fi
      [[ "${probe_result}" -eq 2 ]] && return 1
    fi
    case "${state}" in
      failed|dead|inactive) return 1 ;;
    esac
    sleep "${interval}"
  done
  return 1
}
