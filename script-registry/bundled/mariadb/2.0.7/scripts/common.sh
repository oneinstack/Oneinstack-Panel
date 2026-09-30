#!/usr/bin/env bash
set -Eeuo pipefail
umask 027

component_id="mariadb"
software_version="${SOFTWARE_VERSION:-11.4.13}"
patch_version=""
install_dir="${INSTALL_DIR:-/usr/local/mariadb}"
data_dir="${DATA_DIR:-/data/mariadb}"
log_dir="${LOG_DIR:-/data/mariadb}"
mysql_port="${MARIADB_PORT:-3306}"
bind_address="${MARIADB_BIND_ADDRESS:-127.0.0.1}"
mysql_password="${MYSQL_PASSWORD:-}"
mysql_username="${MYSQL_USERNAME:-root}"
run_user="${RUN_USER:-mysql}"
run_group="${RUN_GROUP:-mysql}"
state_root="${COMPONENT_STATE_DIR:-${ONEINSTACK_COMPONENT_STATE:-/var/lib/oneinstack/components}}"
state_dir="${state_root}/${component_id}"
rollback_dir="${state_dir}/rollback"
unit_file="/etc/systemd/system/mariadb.service"
config_file="/etc/oneinstack/mariadb/my.cnf"
legacy_state_file="${state_dir}/installed.json"
install_parameters_file="${state_dir}/install-parameters"
source_url=""
source_signature_url=""
source_sha256=""
source_fingerprint=""
source_archive=""
host_arch=""
system_id=""
system_version=""
package_manager=""
install_mode="${ONEINSTACK_INSTALL_MODE:-center}"
offline_package_path="${ONEINSTACK_OFFLINE_PACKAGE_PATH:-}"
script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
component_root="$(cd -- "${script_dir}/.." && pwd)"
release_key_file="${component_root}/assets/MariaDB-Server-GPG-KEY"
libfmt_archive="${component_root}/assets/fmt-12.2.0.zip"
libfmt_sha256="a2f4a8d51178f954e4c339007f77edd76ba0cb2e36f87a48e5a5403d9be5878f"
libfmt_upstream_url="https://github.com/fmtlib/fmt/releases/download/12.2.0/fmt-12.2.0.zip"
pcre2_archive="${component_root}/assets/pcre2-10.47.zip"
pcre2_sha256="d74c183c86c77248ad50017c7f45bae8f88106a6cca5d87ad09917e1c6fb0784"
pcre2_upstream_url="https://github.com/PCRE2Project/pcre2/releases/download/pcre2-10.47/pcre2-10.47.zip"
centos7_rpm_base_url="https://archive.mariadb.org/mariadb-10.11.19/yum/rhel7-amd64/rpms"
centos7_rpm_release="10.11.19-1.el7_9.x86_64"

die() { echo "ERROR: $*" >&2; exit 1; }
require_command() { command -v "$1" >/dev/null 2>&1 || die "Required command not found: $1"; }
emit_progress() {
  local percent="$1" code="$2" message="$3" fd="${ONEINSTACK_PROGRESS_FD:-}"
  [[ "${fd}" =~ ^[0-9]+$ ]] || return 0
  message="${message//\\/\\\\}"; message="${message//\"/\\\"}"; message="${message//$'\n'/ }"
  # The Panel runner reserves the progress descriptor as a write-only FD.
  # shellcheck disable=SC2261
  printf '{"type":"progress","percent":%s,"code":"%s","message":"%s"}\n' \
    "${percent}" "${code}" "${message}" >&"${fd}" 2>/dev/null || true
}
require_root() { [[ "$(id -u)" -eq 0 ]] || die "This action must run as root."; }
redirect_bundled_build_archive() {
  local cmake_file="$1" archive="$2" expected_sha="$3" upstream_url="$4" label="$5"
  local local_url temporary line actual_sha replaced=false
  require_command sha256sum
  [[ -f "${archive}" ]] || die "Bundled ${label} archive is missing."
  actual_sha="$(sha256sum "${archive}" | awk '{print $1}')"
  [[ "${actual_sha}" == "${expected_sha}" ]] || die "Bundled ${label} archive checksum verification failed."
  [[ -f "${cmake_file}" ]] || die "MariaDB ${label} CMake definition is missing."
  local_url="file://${archive}"
  temporary="$(mktemp "$(dirname -- "${cmake_file}")/.oneinstack-dependency.XXXXXX")"
  while IFS= read -r line || [[ -n "${line}" ]]; do
    if [[ "${line}" == *"${upstream_url}"* ]]; then
      line="${line//${upstream_url}/${local_url}}"
      replaced=true
    fi
    printf '%s\n' "${line}"
  done <"${cmake_file}" >"${temporary}"
  if [[ "${replaced}" != true ]]; then
    rm -f -- "${temporary}"
    die "MariaDB ${label} source definition does not match the pinned upstream URL."
  fi
  chmod 0644 "${temporary}"
  mv -f -- "${temporary}" "${cmake_file}"
  grep -Fq -- "${local_url}" "${cmake_file}" ||
    die "MariaDB ${label} source was not redirected to the bundled archive."
}
prepare_bundled_build_dependencies() {
  local source_root="$1"
  redirect_bundled_build_archive "${source_root}/cmake/libfmt.cmake" \
    "${libfmt_archive}" "${libfmt_sha256}" "${libfmt_upstream_url}" "fmt 12.2.0"
  redirect_bundled_build_archive "${source_root}/cmake/pcre.cmake" \
    "${pcre2_archive}" "${pcre2_sha256}" "${pcre2_upstream_url}" "PCRE2 10.47"
}
validate_path() {
  local value="$1" label="$2"
  [[ "${value}" == /* && "$(realpath -m -- "${value}")" == "${value}" ]] || die "${label} must be a normalized absolute path."
  case "${value}" in /|/usr|/usr/local|/etc|/var|/data|/home|/root) die "${label} is too broad: ${value}" ;; esac
}
validate_ip_address() {
  local value="$1" octet
  local -a octets=()
  if [[ "${value}" == *.* ]]; then
    [[ "${value}" =~ ^([0-9]{1,3}\.){3}[0-9]{1,3}$ ]] || return 1
    IFS='.' read -r -a octets <<<"${value}"
    for octet in "${octets[@]}"; do
      [[ "${octet}" =~ ^[0-9]+$ ]] && ((10#${octet} <= 255)) || return 1
    done
    return 0
  fi
  [[ "${value}" == *:* ]] || return 1
  command -v getent >/dev/null 2>&1 || return 1
  getent ahostsv6 "${value}" >/dev/null 2>&1
}
architecture_name() {
  case "$(uname -m)" in
    x86_64) printf 'amd64' ;;
    aarch64|arm64) printf 'arm64' ;;
    *) die "Unsupported CPU architecture: $(uname -m)" ;;
  esac
}
glibc_is_older_than_228() {
  local version major minor
  require_command ldd
  version="$(LC_ALL=C ldd --version 2>&1 | awk 'NR == 1 { for (i = 1; i <= NF; i++) if ($i ~ /^[0-9]+\.[0-9]+$/) { print $i; exit } }')"
  [[ "${version}" =~ ^[0-9]+\.[0-9]+$ ]] || die "Cannot determine the host glibc version."
  IFS='.' read -r major minor <<<"${version}"
  (( major < 2 || (major == 2 && minor < 28) ))
}
is_centos7_rpm_runtime() {
  [[ "${system_id}" == centos && "${system_version%%.*}" == 7 &&
    "${host_arch}" == amd64 && "${software_version}" == 10.11.19 ]]
}
select_source() {
  if is_centos7_rpm_runtime; then
    source_archive="MariaDB-server-${centos7_rpm_release}.rpm"
    source_url="${centos7_rpm_base_url}/${source_archive}"
    source_signature_url=""
    source_fingerprint=""
    source_sha256="44f2d9ab69ce7f9bb51e7bad4ece2982f1cf1ad9882a0d55f5362456e54e8a21"
    return
  fi
  source_archive="mariadb-${software_version}.tar.gz"
  source_url="https://archive.mariadb.org/mariadb-${software_version}/source/${source_archive}"
  source_signature_url="${source_url}.asc"
  source_fingerprint="177F4010FE56CA3336300305F1656F24C74CD1D8"
  case "${software_version}" in
    10.11.19) source_sha256="b8e543ee69d380fb1cfd563226f49e0fe96e4d67e7b7a9045ee514a168ed2066" ;;
    11.4.13) source_sha256="1bb254b106d0a7ca871cfa18fa6e18d4b80a7430f9ec9d1571ec4271a13def96" ;;
    *) die "Unsupported MariaDB version: ${software_version}." ;;
  esac
}
load_persisted_parameters() {
  local persisted="${state_dir}/install-parameters" key value marker
  [[ -r "${persisted}" ]] || return 0
  while IFS='=' read -r key value; do
    marker="ONEINSTACK_PARAMETER_${key}_EXPLICIT"
    case "${key}" in
      MARIADB_PORT) [[ "${!marker:-false}" == true ]] || mysql_port="${value}" ;;
      MARIADB_BIND_ADDRESS) [[ "${!marker:-false}" == true ]] || bind_address="${value}" ;;
      MYSQL_USERNAME) [[ "${!marker:-false}" == true ]] || mysql_username="${value}" ;;
      INSTALL_DIR) [[ "${!marker:-false}" == true ]] || install_dir="${value}" ;;
      DATA_DIR) [[ "${!marker:-false}" == true ]] || data_dir="${value}" ;;
      LOG_DIR) [[ "${!marker:-false}" == true ]] || log_dir="${value}" ;;
      RUN_USER) [[ "${!marker:-false}" == true ]] || run_user="${value}" ;;
      RUN_GROUP) [[ "${!marker:-false}" == true ]] || run_group="${value}" ;;
    esac
  done <"${persisted}"
}
reject_immutable_runtime_changes() {
  local persisted="${state_dir}/install-parameters" key value marker current
  [[ -f "${state_dir}/version" && -r "${persisted}" ]] || return 0
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
    if [[ "${!marker:-false}" == true && "${current}" != "${value}" ]]; then
      die "${key} cannot change during an upgrade; reinstall the component to change its runtime identity."
    fi
  done <"${persisted}"
}
load_persisted_parameters
has_systemd() { command -v systemctl >/dev/null 2>&1 && [[ -d /run/systemd/system ]]; }
service_is_active() {
  if has_systemd; then systemctl is-active --quiet mariadb.service 2>/dev/null; return; fi
  [[ -r "${data_dir}/mysqld.pid" ]] && kill -0 "$(cat "${data_dir}/mysqld.pid")" 2>/dev/null
}
service_start() {
  if has_systemd; then
    systemctl daemon-reload
    systemctl enable --now mariadb.service
  else
    install -d -m 0755 -o "${run_user}" -g "${run_group}" /run/mariadb
    runuser -u "${run_user}" -- "${install_dir}/bin/mariadbd" --defaults-file="${config_file}" >/dev/null 2>&1 &
    printf '%s\n' "$!" >"${data_dir}/mysqld.pid"
  fi
}
service_stop() {
  if has_systemd; then
    systemctl stop mariadb.service 2>/dev/null || true
  elif [[ -r "${data_dir}/mysqld.pid" ]]; then
    kill "$(cat "${data_dir}/mysqld.pid")" 2>/dev/null || true
  fi
}
service_restart() { service_stop; service_start; }
service_status_value() {
  local property="$1"
  if has_systemd; then
    local unit value
    for unit in mariadb.service mysqld.service; do
      # CentOS 7 ships systemd 219, whose systemctl does not support --value.
      # Parse the stable Property=value form so status probes work across the
      # complete supported matrix without weakening the fixed property allowlist.
      value="$(systemctl show "${unit}" --property="${property}" --no-pager 2>/dev/null |
        sed -n "s/^${property}=//p" | head -n1 || true)"
      if [[ -n "${value}" && "${value}" != "not-found" ]]; then
        printf '%s' "${value}"
        return 0
      fi
    done
    return 0
  elif [[ "${property}" == "ActiveState" ]] && service_is_active; then
    printf 'active'
  else
    printf 'inactive'
  fi
}
verify_existing_database_password() {
  [[ -d "${data_dir}/mysql" ]] || return 0
  [[ -n "${mysql_password}" ]] ||
    die "MYSQL_PASSWORD is required to verify an existing managed MariaDB data directory."
  local client_file output
  [[ -x "${install_dir}/bin/mariadb" ]] || return 0
  client_file="$(mktemp "${TMPDIR:-/tmp}/oneinstack-mariadb-precheck.XXXXXX")"
  chmod 0600 "${client_file}"
  {
    printf '[client]\n'
    printf 'user=%s\n' "${mysql_username}"
    printf "password='%s'\n" "${mysql_password}"
    printf 'host=127.0.0.1\n'
    printf 'port=%s\n' "${mysql_port}"
    printf 'protocol=tcp\n'
  } >"${client_file}"
  if output="$("${install_dir}/bin/mariadb" --defaults-file="${client_file}" --batch --skip-column-names --execute='SELECT 1' 2>&1)" &&
    grep -Fxq '1' <<<"${output}"; then
    rm -f -- "${client_file}"
    return 0
  fi
  rm -f -- "${client_file}"
  if grep -Eqi 'access denied|using password|authentication' <<<"${output}"; then
    die "Existing managed MariaDB data was found, but the supplied root password was rejected."
  fi
  die "Existing managed MariaDB data could not be authenticated."
}
persisted_runtime_value() {
  local requested_key="$1" key value
  [[ -r "${install_parameters_file}" ]] || return 1
  while IFS='=' read -r key value; do
    if [[ "${key}" == "${requested_key}" ]]; then
      printf '%s' "${value}"
      return 0
    fi
  done <"${install_parameters_file}"
  return 1
}
persist_install_parameters() {
	install -d -m 0750 -- "${state_dir}"
	local temporary
	temporary="$(mktemp "${state_dir}/.install-parameters.XXXXXX")"
	{
		printf 'SOFTWARE_VERSION=%s\n' "${software_version}"
		printf 'MARIADB_PORT=%s\n' "${mysql_port}"
		printf 'MARIADB_BIND_ADDRESS=%s\n' "${bind_address}"
		printf 'MYSQL_USERNAME=%s\n' "${mysql_username}"
		printf 'INSTALL_DIR=%s\n' "${install_dir}"
		printf 'DATA_DIR=%s\n' "${data_dir}"
		printf 'LOG_DIR=%s\n' "${log_dir}"
		printf 'RUN_USER=%s\n' "${run_user}"
		printf 'RUN_GROUP=%s\n' "${run_group}"
	} >"${temporary}"
	chmod 0600 "${temporary}"
	mv -f -- "${temporary}" "${install_parameters_file}"
}
validate_inputs() {
  case "${software_version}" in 10.11.19|11.4.13) ;; *) die "Unsupported MariaDB version: ${software_version}; only 10.11.19 and 11.4.13 are supported." ;; esac
  # shellcheck disable=SC2034 # consumed by install.sh and verify.sh after sourcing
  patch_version="${software_version}"
  [[ "${install_mode}" == center || "${install_mode}" == offline ]] ||
    die "ONEINSTACK_INSTALL_MODE must be center or offline."
  if [[ "${install_mode}" == offline ]]; then
    [[ -n "${offline_package_path}" && "${offline_package_path}" == /* &&
      "$(realpath -m -- "${offline_package_path}")" == "${offline_package_path}" ]] ||
      die "Offline installation requires a normalized absolute Bundle path."
  elif [[ -n "${offline_package_path}" ]]; then
    die "Offline Bundle path cannot be used in Center mode."
  fi
  [[ "${mysql_port}" =~ ^[0-9]+$ && "${mysql_port}" -ge 1 && "${mysql_port}" -le 65535 ]] ||
    die "Invalid MARIADB_PORT."
	validate_ip_address "${bind_address}" ||
    die "MARIADB_BIND_ADDRESS must be an IP address."
  [[ -z "${mysql_password}" || "${mysql_password}" =~ ^[A-Za-z0-9_@%+=:,.!#?-]{12,128}$ ]] ||
    die "MYSQL_PASSWORD must be 12-128 safe characters."
  [[ "${mysql_username}" =~ ^[a-z_][a-z0-9_-]{0,31}$ ]] || die "MYSQL_USERNAME is invalid."
  [[ "${run_user}" =~ ^[a-z_][a-z0-9_-]{0,31}$ ]] || die "RUN_USER is invalid."
  [[ "${run_group}" =~ ^[a-z_][a-z0-9_-]{0,31}$ ]] || die "RUN_GROUP is invalid."
  validate_path "${install_dir}" INSTALL_DIR
  validate_path "${data_dir}" DATA_DIR
  validate_path "${log_dir}" LOG_DIR
  validate_path "${state_root}" ONEINSTACK_COMPONENT_STATE
  case "${install_dir}${data_dir}${log_dir}${state_root}" in
    *[[:space:]]*) die "MariaDB managed paths cannot contain whitespace." ;;
  esac
  select_source
}
preserved_data_recovery_allowed() {
  [[ ! -f "${state_dir}/version" && -f "${state_dir}/pending-version" ]] || return 1
  preserved_managed_data_present
}
recover_preserved_root_password() {
  preserved_data_recovery_allowed || return 1
  local recovery_socket="/run/mariadb/oneinstack-recovery.sock"
  local recovery_pid_file="/run/mariadb/oneinstack-recovery.pid"
  local recovery_log="${log_dir}/mariadb-recovery.log"
  local recovery_pid="" ready=false output

  emit_progress 84 recover_root_password "检测到卸载保留数据认证失败，正在自动重置 MariaDB root 密码"
  service_stop
  rm -f -- "${recovery_socket}" "${recovery_pid_file}"
  install -d -o "${run_user}" -g "${run_group}" -m 0755 /run/mariadb
  "${install_dir}/bin/mariadbd" \
    --defaults-file="${config_file}" \
    --user="${run_user}" \
    --skip-grant-tables \
    --skip-networking \
    --socket="${recovery_socket}" \
    --pid-file="${recovery_pid_file}" \
    --log-error="${recovery_log}" \
    >/dev/null 2>&1 &
  recovery_pid="$!"
  for _ in $(seq 1 60); do
    if [[ -S "${recovery_socket}" ]]; then
      ready=true
      break
    fi
    kill -0 "${recovery_pid}" 2>/dev/null || break
    sleep 1
  done
  if [[ "${ready}" != true ]]; then
    kill "${recovery_pid}" 2>/dev/null || true
    wait "${recovery_pid}" 2>/dev/null || true
    die "MariaDB recovery socket was not ready; the preserved data was not modified."
  fi

  if ! output="$("${install_dir}/bin/mariadb" \
    --no-defaults \
    --protocol=socket \
    --socket="${recovery_socket}" \
    -uroot <<EOF
FLUSH PRIVILEGES;
CREATE USER IF NOT EXISTS 'root'@'localhost' IDENTIFIED BY '${mysql_password}';
ALTER USER 'root'@'localhost' IDENTIFIED BY '${mysql_password}';
CREATE USER IF NOT EXISTS 'root'@'127.0.0.1' IDENTIFIED BY '${mysql_password}';
ALTER USER 'root'@'127.0.0.1' IDENTIFIED BY '${mysql_password}';
GRANT ALL PRIVILEGES ON *.* TO 'root'@'localhost' WITH GRANT OPTION;
GRANT ALL PRIVILEGES ON *.* TO 'root'@'127.0.0.1' WITH GRANT OPTION;
FLUSH PRIVILEGES;
EOF
  )"; then
    printf '%s\n' "${output}" >&2
    "${install_dir}/bin/mariadb-admin" --no-defaults --protocol=socket \
      --socket="${recovery_socket}" -uroot shutdown >/dev/null 2>&1 || true
    kill "${recovery_pid}" 2>/dev/null || true
    wait "${recovery_pid}" 2>/dev/null || true
    die "MariaDB root password recovery failed; the preserved data was not modified."
  fi

  "${install_dir}/bin/mariadb-admin" --no-defaults --protocol=socket \
    --socket="${recovery_socket}" -uroot shutdown >/dev/null 2>&1 || {
      kill "${recovery_pid}" 2>/dev/null || true
    }
  wait "${recovery_pid}" 2>/dev/null || true
  rm -f -- "${recovery_socket}" "${recovery_pid_file}"

  service_start
  ready=false
  for _ in $(seq 1 180); do
    if [[ -S /run/mariadb/mariadb.sock ]] && service_is_active; then
      ready=true
      break
    fi
    service_is_active || break
    sleep 1
  done
  [[ "${ready}" == true ]] || die "MariaDB did not restart after root password recovery."
}
check_host() {
  [[ -r /etc/os-release ]] || die "Cannot identify the Linux distribution."
  # shellcheck disable=SC1091
  source /etc/os-release
  system_id="${ID:-}"
  system_version="${VERSION_ID:-}"
  if [[ "${system_id}" == centos && "${NAME:-}" == *Stream* ]]; then
    system_id="centos-stream"
  fi
  case "${system_id}" in
    ubuntu) case "${system_version}" in 22.04|24.04|26.04) ;; *) die "Unsupported Ubuntu release: ${system_version:-unknown}" ;; esac ;;
    debian) case "${system_version}" in 12|13) ;; *) die "Unsupported Debian release: ${system_version:-unknown}" ;; esac ;;
    rhel|rocky|almalinux|ol)
      system_version="${system_version%%.*}"
      case "${system_version}" in 8|9|10) ;; *) die "Unsupported Enterprise Linux release: ${VERSION_ID:-unknown}" ;; esac
      ;;
    centos-stream)
      system_version="${system_version%%.*}"
      case "${system_version}" in 9|10) ;; *) die "Unsupported CentOS Stream release: ${VERSION_ID:-unknown}" ;; esac
      ;;
    centos)
      system_version="${system_version%%.*}"
      [[ "${system_version}" == 7 ]] || die "Unsupported CentOS Linux release: ${VERSION_ID:-unknown}"
      ;;
    amzn) [[ "${system_version}" == 2023 ]] || die "Unsupported Amazon Linux release: ${system_version:-unknown}" ;;
    sles)
      system_version="${system_version%%.*}"
      case "${system_version}" in 15|16) ;; *) die "Unsupported SLES release: ${VERSION_ID:-unknown}" ;; esac
      ;;
    *) die "Unsupported Linux distribution: ${system_id:-unknown}" ;;
  esac
  host_arch="$(architecture_name)"
  if command -v apt-get >/dev/null 2>&1; then package_manager=apt
  elif command -v dnf >/dev/null 2>&1; then package_manager=dnf
  elif command -v yum >/dev/null 2>&1; then package_manager=yum
  elif command -v zypper >/dev/null 2>&1; then package_manager=zypper
  else die "No supported package manager (apt, dnf, yum, or zypper) was found."; fi
  if [[ "${system_id}" == centos && "${system_version}" == 7 ]]; then
    [[ "${host_arch}" == amd64 ]] || die "CentOS 7 MariaDB is supported only on x86_64."
    [[ "${software_version}" == 10.11.19 ]] ||
      die "CentOS 7 supports only MariaDB 10.11.19; MariaDB 11.4 has no verified RHEL 7 runtime."
  fi
  has_systemd || die "MariaDB requires a systemd-based host."
  select_source
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
    *)
      cp -a -- "${source_file}" "${target_file}"
      ;;
  esac
}
apt_update_without_docker_source() (
  set -Eeuo pipefail
  local source_dir source_list source_file target_file
  source_dir="$(mktemp -d)"
  trap 'rm -rf -- "${source_dir}"' EXIT
  source_list="${source_dir}/sources.list"
  if [[ -f /etc/apt/sources.list ]]; then
    copy_apt_source_without_docker /etc/apt/sources.list "${source_list}"
  else
    : >"${source_list}"
  fi
  for source_file in /etc/apt/sources.list.d/*.list /etc/apt/sources.list.d/*.sources; do
    [[ -f "${source_file}" ]] || continue
    target_file="${source_dir}/$(basename -- "${source_file}")"
    copy_apt_source_without_docker "${source_file}" "${target_file}"
  done
  apt-get update \
    -o "Dir::Etc::sourcelist=${source_list}" \
    -o "Dir::Etc::sourceparts=${source_dir}/"
)
apt_update_for_mysql() {
  local docker_sources=()
  mapfile -t docker_sources < <(docker_apt_sources)
  if ((${#docker_sources[@]} > 0)); then
    emit_progress 6 apt.repository.filtered "检测到 Docker APT 源，已隔离其源配置以更新 MariaDB 依赖"
    apt_update_without_docker_source
  else
    apt-get update
  fi
}
install_dependencies() {
  if [[ "${install_mode}" == offline ]]; then
    install_dependencies_offline
  elif is_centos7_rpm_runtime; then
    yum install -y \
      ca-certificates cpio curl gnupg2 iproute libaio ncurses-libs openssl-libs \
      pcre2 perl perl-DBI perl-Data-Dumper rpm systemd-libs zlib
  else
    case "${package_manager}" in
      apt)
        export DEBIAN_FRONTEND=noninteractive
        apt_update_for_mysql
        apt-get install -y --no-install-recommends \
          build-essential bison ca-certificates cmake curl gnupg iproute2 \
          libaio-dev libncurses-dev libssl-dev libsystemd-dev make perl pkg-config tar
        ;;
      dnf|yum)
        local pkgconfig_package=pkgconfig
        [[ "${package_manager}" == dnf ]] && pkgconfig_package=pkgconf-pkg-config
        "${package_manager}" install -y \
          bison ca-certificates cmake curl gcc gcc-c++ gnupg2 iproute \
          libaio-devel make ncurses-devel openssl-devel perl systemd-devel tar "${pkgconfig_package}"
        ;;
      zypper)
        zypper --non-interactive install -y \
          bison ca-certificates cmake curl gcc gcc-c++ gpg2 iproute2 \
          libaio-devel libopenssl-devel make ncurses-devel perl pkg-config systemd-devel tar
        ;;
      esac
  fi
  if is_centos7_rpm_runtime; then
    require_command cpio
    require_command ldd
    require_command perl
    require_command rpm
    require_command rpm2cpio
  else
    require_command cmake
    require_command make
    require_command tar
    command -v gpg >/dev/null 2>&1 || command -v gpg2 >/dev/null 2>&1 ||
      die "GnuPG is required to verify the MariaDB source signature."
    command -v c++ >/dev/null 2>&1 || command -v g++ >/dev/null 2>&1 ||
      die "A C++ compiler is required to build MariaDB."
  fi
}
offline_package_dir() {
	[[ -n "${offline_package_path}" ]] || die "Offline installation requires ONEINSTACK_OFFLINE_PACKAGE_PATH."
	printf '%s/packages/%s/%s/%s\n' "${offline_package_path}" "${system_id}" "${system_version}" "${host_arch}"
}
centos7_rpm_archives() {
  printf '%s\n' \
    "MariaDB-common-${centos7_rpm_release}.rpm" \
    "MariaDB-shared-${centos7_rpm_release}.rpm" \
    "MariaDB-client-${centos7_rpm_release}.rpm" \
    "MariaDB-server-${centos7_rpm_release}.rpm"
}
centos7_rpm_sha256() {
  case "$1" in
    MariaDB-common-*) printf '%s' 50de147a93083e3b8d13dc8c6cb690cf2dd3924b3e989cf386933f5ba63b449d ;;
    MariaDB-shared-*) printf '%s' 23402bec671faf0eb2c84391462d074f74b67d4059c9ce8f8d008cf65054ec22 ;;
    MariaDB-client-*) printf '%s' 874d0d7e8d6fd7c807cb05a95a2e50925a3913a9eca3f794fd27ac8413377a24 ;;
    MariaDB-server-*) printf '%s' 44f2d9ab69ce7f9bb51e7bad4ece2982f1cf1ad9882a0d55f5362456e54e8a21 ;;
    *) return 1 ;;
  esac
}
verify_centos7_rpm_set() (
  set -Eeuo pipefail
  local rpm_dir="$1" archive expected_checksum expected_name metadata rpm_db
  [[ -d "${rpm_dir}" ]] || die "MariaDB CentOS 7 RPM artifact directory is missing."
  while IFS= read -r archive; do
    [[ -f "${rpm_dir}/${archive}" ]] || die "MariaDB CentOS 7 artifact is missing: ${archive}."
    expected_checksum="$(centos7_rpm_sha256 "${archive}")"
    printf '%s  %s\n' "${expected_checksum}" "${rpm_dir}/${archive}" | sha256sum --check --status ||
      die "MariaDB CentOS 7 artifact checksum failed: ${archive}."
    expected_name="${archive%%-"${centos7_rpm_release}".rpm}"
    metadata="$(rpm -qp --qf '%{NAME}|%{VERSION}|%{RELEASE}|%{ARCH}' "${rpm_dir}/${archive}" 2>/dev/null)" ||
      die "MariaDB CentOS 7 artifact metadata is invalid: ${archive}."
    [[ "${metadata}" == "${expected_name}|10.11.19|1.el7_9|x86_64" ]] ||
      die "MariaDB CentOS 7 artifact identity is invalid: ${archive}."
  done < <(centos7_rpm_archives)

  [[ -f "${release_key_file}" ]] || die "Embedded MariaDB release key is missing."
  rpm_db="$(mktemp -d)"
  trap 'rm -rf -- "${rpm_db}"' EXIT
  rpm --dbpath "${rpm_db}" --initdb
  rpm --dbpath "${rpm_db}" --import "${release_key_file}"
  while IFS= read -r archive; do
    LC_ALL=C rpm --dbpath "${rpm_db}" --checksig "${rpm_dir}/${archive}" >/dev/null ||
      die "MariaDB CentOS 7 RPM signature verification failed: ${archive}."
  done < <(centos7_rpm_archives)
)
download_centos7_rpm_set() {
  local destination="$1" archive expected_checksum cache_dir cache_file temporary
  install -d -m 0755 -- "${destination}"
  if [[ "${install_mode}" == offline ]]; then
    while IFS= read -r archive; do
      cp -- "${offline_package_path}/artifacts/${host_arch}/${archive}" "${destination}/${archive}"
    done < <(centos7_rpm_archives)
    verify_centos7_rpm_set "${destination}"
    return
  fi

  cache_dir="/var/cache/oneinstack/downloads"
  install -d -m 0750 -- "${cache_dir}"
  while IFS= read -r archive; do
    expected_checksum="$(centos7_rpm_sha256 "${archive}")"
    cache_file="${cache_dir}/${archive}"
    if [[ ! -f "${cache_file}" ]] ||
      ! printf '%s  %s\n' "${expected_checksum}" "${cache_file}" | sha256sum --check --status; then
      temporary="$(mktemp "${cache_file}.tmp.XXXXXX")"
      if ! curl --proto '=https' --tlsv1.2 --fail --location --retry 3 --connect-timeout 20 \
        --output "${temporary}" "${centos7_rpm_base_url}/${archive}"; then
        rm -f -- "${temporary}"
        die "MariaDB CentOS 7 RPM download failed: ${archive}."
      fi
      printf '%s  %s\n' "${expected_checksum}" "${temporary}" | sha256sum --check --status || {
        rm -f -- "${temporary}"
        die "MariaDB CentOS 7 RPM checksum failed: ${archive}."
      }
      chmod 0640 "${temporary}"
      mv -f -- "${temporary}" "${cache_file}"
    fi
    cp -- "${cache_file}" "${destination}/${archive}"
  done < <(centos7_rpm_archives)
  verify_centos7_rpm_set "${destination}"
}
stage_centos7_rpm_runtime() {
  local rpm_dir="$1" stage_install="$2" extraction_root="$3" archive missing runtime_version
  install -d -m 0755 -- "${extraction_root}" "${stage_install}/bin" "${stage_install}/lib64"
  while IFS= read -r archive; do
    rpm2cpio "${rpm_dir}/${archive}" | (cd "${extraction_root}" && cpio -idm --quiet)
  done < <(centos7_rpm_archives)

  [[ -x "${extraction_root}/usr/sbin/mariadbd" ]] || die "Extracted MariaDB server binary is missing."
  [[ -x "${extraction_root}/usr/bin/mariadb" ]] || die "Extracted MariaDB client binary is missing."
  [[ -x "${extraction_root}/usr/bin/mariadb-install-db" ]] || die "Extracted mariadb-install-db is missing."
  cp -a -- "${extraction_root}/usr/bin/." "${stage_install}/bin/"
  install -d -m 0755 -- "${stage_install}/sbin" "${stage_install}/share" "${stage_install}/lib64"
  cp -a -- "${extraction_root}/usr/sbin/." "${stage_install}/sbin/"
  cp -a -- "${extraction_root}/usr/share/mysql" "${stage_install}/share/mysql"
  cp -a -- "${extraction_root}/usr/lib64/mysql" "${stage_install}/lib64/mysql"
  find "${extraction_root}/usr/lib64" -maxdepth 1 -name 'libmaria*' -exec cp -a -- {} "${stage_install}/lib64/" \;
  ln -f -- "${stage_install}/sbin/mariadbd" "${stage_install}/bin/mariadbd"
  ln -f -- "${stage_install}/sbin/mysqld" "${stage_install}/bin/mysqld"

  missing="$(ldd "${stage_install}/bin/mariadbd" 2>&1 | awk '/not found/ {print}')"
  [[ -z "${missing}" ]] || die "MariaDB CentOS 7 server runtime dependencies are missing: ${missing//$'\n'/; }."
  missing="$(ldd "${stage_install}/bin/mariadb" 2>&1 | awk '/not found/ {print}')"
  [[ -z "${missing}" ]] || die "MariaDB CentOS 7 client runtime dependencies are missing: ${missing//$'\n'/; }."
  runtime_version="$("${stage_install}/bin/mariadbd" --version 2>&1 | grep -Eo '[0-9]+\.[0-9]+\.[0-9]+' | head -n1)"
  [[ "${runtime_version}" == "${software_version}" ]] ||
    die "MariaDB CentOS 7 staged runtime is ${runtime_version:-unknown}, expected ${software_version}."
}
validate_offline_bundle() {
  local package_dir artifact signature key expected_extension package package_arch
  local -a packages=()
  [[ -d "${offline_package_path}" ]] || die "Offline MariaDB Bundle is unavailable."
  for key in manifest.yaml files.sha256 bundle-info assets/MariaDB-Server-GPG-KEY \
    assets/fmt-12.2.0.zip assets/pcre2-10.47.zip; do
    [[ -f "${offline_package_path}/${key}" ]] || die "Offline MariaDB Bundle is missing ${key}."
  done
  (cd "${offline_package_path}" && sha256sum -c files.sha256 --status) ||
    die "Offline MariaDB Bundle checksum verification failed."
  grep -Eq '^[[:space:]]+id:[[:space:]]+mariadb[[:space:]]*$' "${offline_package_path}/manifest.yaml" ||
		die "Offline MariaDB Bundle manifest identity is invalid."
  grep -Fxq 'component=mariadb' "${offline_package_path}/bundle-info" || die "Offline Bundle component identity is invalid."
  grep -Fxq 'packageVersion=2.0.7' "${offline_package_path}/bundle-info" || die "Offline Bundle package version is invalid."
  grep -Fxq "softwareVersion=${software_version}" "${offline_package_path}/bundle-info" || die "Offline Bundle software version does not match."
  grep -Fxq "osId=${system_id}" "${offline_package_path}/bundle-info" || die "Offline Bundle operating system does not match."
  grep -Fxq "osVersion=${system_version}" "${offline_package_path}/bundle-info" || die "Offline Bundle operating system version does not match."
  grep -Fxq "architecture=${host_arch}" "${offline_package_path}/bundle-info" || die "Offline Bundle architecture does not match."
  if is_centos7_rpm_runtime; then
    require_command rpm
    verify_centos7_rpm_set "${offline_package_path}/artifacts/${host_arch}"
  else
    artifact="${offline_package_path}/artifacts/${host_arch}/${source_archive}"
    signature="${artifact}.asc"
    [[ -f "${artifact}" && -f "${signature}" ]] || die "Offline MariaDB source artifact or signature is missing."
    verify_source_checksum "${artifact}" || die "Offline MariaDB source checksum verification failed."
    if command -v gpg >/dev/null 2>&1 || command -v gpg2 >/dev/null 2>&1; then
      verify_source_signature "${artifact}" "${signature}"
    fi
  fi
  package_dir="$(offline_package_dir)"
  [[ -d "${package_dir}" ]] || die "Offline MariaDB dependency directory is missing."
  case "${package_manager}" in apt) expected_extension=deb ;; *) expected_extension=rpm ;; esac
  mapfile -t packages < <(find "${package_dir}" -maxdepth 1 -type f -name "*.${expected_extension}" -print | sort)
  ((${#packages[@]} > 0)) || die "Offline MariaDB dependency packages are missing."
  [[ "$(find "${package_dir}" -maxdepth 1 -type f ! -name "*.${expected_extension}" -print -quit)" == "" ]] ||
		die "Offline MariaDB dependency directory contains an unexpected package type."
  for package in "${packages[@]}"; do
    if [[ "${expected_extension}" == deb ]]; then
      require_command dpkg-deb
      package_arch="$(dpkg-deb -f "${package}" Architecture 2>/dev/null)" || die "Invalid Debian dependency package: ${package##*/}."
      [[ "${package_arch}" == all || "${package_arch}" == "${host_arch}" ]] || die "Dependency package architecture does not match: ${package##*/}."
    else
      require_command rpm
      package_arch="$(rpm -qp --qf '%{ARCH}' "${package}" 2>/dev/null)" || die "Invalid RPM dependency package: ${package##*/}."
      case "${host_arch}:${package_arch}" in amd64:x86_64|amd64:noarch|arm64:aarch64|arm64:noarch) ;; *) die "Dependency package architecture does not match: ${package##*/}." ;; esac
    fi
  done
  case "${package_manager}" in
    apt) DEBIAN_FRONTEND=noninteractive apt-get --simulate --no-download --no-install-recommends install "${packages[@]}" >/dev/null ;;
    dnf|yum) "${package_manager}" --disablerepo='*' --cacheonly --setopt=tsflags=test install -y "${packages[@]}" >/dev/null ;;
    zypper) zypper --non-interactive --no-refresh --no-remote --dry-run install "${packages[@]}" >/dev/null ;;
  esac || die "Offline MariaDB dependencies are incomplete or incompatible."
}
install_dependencies_offline() {
	local package_dir
	local -a packages=()
	package_dir="$(offline_package_dir)"
	case "${package_manager}" in
		apt)
			mapfile -t packages < <(find "${package_dir}" -maxdepth 1 -type f -name '*.deb' -print | sort)
			DEBIAN_FRONTEND=noninteractive apt-get -y --no-download --no-install-recommends install "${packages[@]}"
			;;
		dnf|yum)
			mapfile -t packages < <(find "${package_dir}" -maxdepth 1 -type f -name '*.rpm' -print | sort)
			"${package_manager}" --disablerepo='*' --cacheonly install -y "${packages[@]}"
			;;
		zypper)
			mapfile -t packages < <(find "${package_dir}" -maxdepth 1 -type f -name '*.rpm' -print | sort)
			zypper --non-interactive --no-refresh --no-remote install "${packages[@]}"
			;;
	esac
}
ensure_account() {
	getent group "${run_group}" >/dev/null || groupadd --system "${run_group}"
	id "${run_user}" >/dev/null 2>&1 || useradd --system --gid "${run_group}" --home-dir "${data_dir}" --shell /usr/sbin/nologin "${run_user}"
	id -nG "${run_user}" | tr ' ' '\n' | grep -Fxq "${run_group}" || usermod -a -G "${run_group}" "${run_user}"
}
runtime_plugin_dir() {
  local candidate
  for candidate in "${install_dir}/lib/plugin" "${install_dir}/lib64/mysql/plugin"; do
    if [[ -d "${candidate}" ]]; then
      printf '%s' "${candidate}"
      return 0
    fi
  done
  return 1
}
runtime_share_dir() {
  local candidate
  for candidate in "${install_dir}/share" "${install_dir}/share/mysql"; do
    if [[ -d "${candidate}/english" && -d "${candidate}/charsets" ]]; then
      printf '%s' "${candidate}"
      return 0
    fi
  done
  return 1
}
normalize_runtime_permissions() {
  ensure_account
	install -d -o "${run_user}" -g "${run_group}" -m 0750 -- "${data_dir}" "${log_dir}"
	chown -R "${run_user}:${run_group}" "${data_dir}" "${log_dir}"
  chmod 0750 "${data_dir}"
  chmod 0755 "${install_dir}" "${install_dir}/bin"
  emit_progress 58 permissions.runtime.applied "MariaDB runtime ownership and directory permissions applied"
}
verify_runtime_permissions() {
  require_command runuser
	runuser -u "${run_user}" -- test -x "${install_dir}/bin/mariadbd" ||
    die "MariaDB runtime user cannot execute the managed server binary."
	runuser -u "${run_user}" -- test -r "${data_dir}" ||
    die "MariaDB runtime user cannot read the managed data directory."
	runuser -u "${run_user}" -- test -w "${data_dir}" ||
    die "MariaDB runtime user cannot write the managed data directory."
  emit_progress 10 permissions.runtime.verified "MariaDB runtime access verified as the mysql user"
}
legacy_managed_installation_present() {
  [[ -r "${legacy_state_file}" ]] || return 1
  grep -Eq '"component"[[:space:]]*:[[:space:]]*"mariadb"' "${legacy_state_file}"
}
managed_installation_present() {
  { [[ -f "${state_dir}/version" ]] || legacy_managed_installation_present; } &&
    [[ -x "${install_dir}/bin/mariadbd" ]] &&
    [[ -d "${data_dir}/mysql" ]]
}
preserved_managed_data_present() {
  [[ -d "${data_dir}/mysql" && -d "${state_dir}/removed" ]] || return 1
  local removed_config
  for removed_config in "${state_dir}/removed"/*/my.cnf; do
    [[ -f "${removed_config}" ]] || continue
    if awk -F= -v expected="${data_dir}" '
      /^[[:space:]]*datadir[[:space:]]*=/ {
        value=$2
        gsub(/^[[:space:]]+|[[:space:]]+$/, "", value)
        if (value == expected) found=1
      }
      END { exit(found ? 0 : 1) }
    ' "${removed_config}"; then
      return 0
    fi
  done
  return 1
}
preserved_managed_runtime_version() {
  local removed_dir binary
  for removed_dir in "${state_dir}/removed"/*; do
    binary="${removed_dir}/install/bin/mariadbd"
    [[ -x "${binary}" ]] || continue
    "${binary}" --version 2>&1 |
      grep -Eo '[0-9]+\.[0-9]+\.[0-9]+' | head -n1
    return 0
  done
  return 1
}
validate_preserved_managed_data() {
  preserved_managed_data_present || return 1
  local current
  current="$(preserved_managed_runtime_version || true)"
  [[ "${current}" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] ||
    die "Preserved MariaDB runtime version cannot be determined; refusing automatic data takeover."
  [[ "${current}" == "${software_version}" ]] ||
    die "Preserved MariaDB runtime ${current} does not match requested ${software_version}; use backup and restore for a version change."
}
managed_runtime_version() {
  if [[ -x "${install_dir}/bin/mariadbd" ]]; then
    "${install_dir}/bin/mariadbd" --version 2>&1 |
      grep -Eo '[0-9]+\.[0-9]+\.[0-9]+' | head -n1
  elif [[ -r "${state_dir}/patch-version" ]]; then
    head -n1 "${state_dir}/patch-version"
  elif legacy_managed_installation_present; then
    sed -nE 's/.*"runtimeVersion"[[:space:]]*:[[:space:]]*"([^"]+)".*/\1/p' "${legacy_state_file}" | head -n1
  fi
}
validate_managed_upgrade_path() {
  managed_installation_present || return 0
  local current current_line target_line lowest
  current="$(managed_runtime_version)"
  [[ "${current}" =~ ^[0-9]+\.[0-9]+\.[0-9]+ ]] ||
    die "Managed MariaDB runtime version cannot be determined."
  current_line="${current%.*}"
  target_line="${software_version%.*}"
  [[ "${current_line}" == 10.11 || "${current_line}" == 11.4 ]] ||
    die "Managed MariaDB ${current} is outside the supported upgrade lines; use backup and restore."
  [[ "${current}" == "${software_version}" ]] && return 0
  [[ "${current_line}" == "${target_line}" ]] ||
    die "Cross-LTS MariaDB upgrade ${current_line} to ${target_line} is not supported; use backup and restore."
  [[ "${target_line}" == 10.11 ]] ||
    die "Only a managed MariaDB 10.11 patch upgrade to 10.11.19 is supported."
  lowest="$(printf '%s\n%s\n' "${current}" "${software_version}" | sort -V | head -n1)"
  [[ "${lowest}" == "${current}" ]] ||
    die "MariaDB downgrade from ${current} to ${software_version} is not supported."
}
external_mysql_runtime_detected() {
  managed_installation_present && return 1
  [[ -x /usr/local/mysql/bin/mysqld || -x /usr/local/percona/bin/mysqld ||
    -x /usr/local/mariadb/bin/mariadbd ]] && return 0
  command -v mysqld >/dev/null 2>&1 && return 0
  command -v mariadbd >/dev/null 2>&1 && return 0
  dpkg-query -W -f='${Status}' mysql-server mariadb-server percona-server-server 2>/dev/null |
    grep -Fq 'install ok installed' && return 0
  rpm_server_package_detected
}
external_mysql_detected() {
  external_mysql_runtime_detected && return 0
  [[ -d /var/lib/mysql || -d "${data_dir}/mysql" ]] && return 0
  return 1
}
rpm_server_package_detected() {
  local package
  while IFS= read -r package; do
    case "${package}" in
      mysql-server-*|mysql-community-server-*|mariadb-server-*|MariaDB-server-*|percona-server-server-*|Percona-Server-server-*)
        return 0
        ;;
    esac
  done < <(rpm -qa 2>/dev/null || true)
  return 1
}
rpm_mariadb_or_percona_server_detected() {
  local package
  while IFS= read -r package; do
    case "${package}" in
      mariadb-server-*|MariaDB-server-*|percona-server-server-*|Percona-Server-server-*)
        return 0
        ;;
    esac
  done < <(rpm -qa 2>/dev/null || true)
  return 1
}
snapshot_external_mysql() {
  external_mysql_detected &&
    die "An external MySQL-compatible server was detected; MariaDB refuses automatic takeover or migration."
  return 0
}
migrate_external_mysql_data() { return 0; }
commit_external_mysql() { return 0; }
verify_source_signature() {
  local archive="$1" signature="$2" gpg_home gpg_command status_line
  local key_verified=false signature_verified=false marker status signing_fingerprint primary_fingerprint
  gpg_command=gpg
  command -v gpg >/dev/null 2>&1 || gpg_command=gpg2
  require_command "${gpg_command}"
  [[ -f "${release_key_file}" ]] || die "Embedded MariaDB release key is missing."
  gpg_home="$(mktemp -d "$(dirname -- "${archive}")/.gnupg.XXXXXX")"
  chmod 0700 "${gpg_home}"
  "${gpg_command}" --batch --homedir "${gpg_home}" --import "${release_key_file}" >/dev/null 2>&1
  while IFS= read -r status_line; do
    [[ "${status_line}" == "${source_fingerprint}" ]] && key_verified=true
  done < <("${gpg_command}" --batch --homedir "${gpg_home}" --with-colons --fingerprint 2>/dev/null |
    awk -F: '$1 == "fpr" {print toupper($10)}')
  [[ "${key_verified}" == true ]] || {
    rm -rf -- "${gpg_home}"
    die "MariaDB release signing key fingerprint verification failed."
  }
  status_line="$("${gpg_command}" --batch --homedir "${gpg_home}" --status-fd=1 \
    --verify "${signature}" "${archive}" 2>/dev/null)" || {
      rm -rf -- "${gpg_home}"
      die "MariaDB source signature verification failed."
    }
  while IFS=' ' read -r marker status signing_fingerprint _ _ _ _ _ _ _ _ primary_fingerprint _; do
    [[ "${marker}" == '[GNUPG:]' && "${status}" == VALIDSIG ]] || continue
    signing_fingerprint="${signing_fingerprint^^}"
    primary_fingerprint="${primary_fingerprint^^}"
    [[ "${signing_fingerprint}" == "${source_fingerprint}" || "${primary_fingerprint}" == "${source_fingerprint}" ]] &&
      signature_verified=true
  done <<<"${status_line}"
  rm -rf -- "${gpg_home}"
  [[ "${signature_verified}" == true ]] ||
    die "MariaDB source signature fingerprint verification failed."
}
verify_source_checksum() {
  local archive="$1"
  [[ -z "${source_sha256}" ]] ||
    printf '%s  %s\n' "${source_sha256}" "${archive}" | sha256sum --check --status
}
download_verified() {
  local destination="$1" signature_destination="$2"
  local cache_dir="/var/cache/oneinstack/downloads"
  local cache_file signature_cache temporary_cache temporary_signature
  select_source
  if [[ "${install_mode}" == offline ]]; then
    local offline_source="${offline_package_path}/artifacts/${host_arch}/${source_archive}"
    local offline_signature="${offline_source}.asc"
    verify_source_checksum "${offline_source}" ||
      die "Offline MariaDB source checksum verification failed."
    verify_source_signature "${offline_source}" "${offline_signature}"
    cp -- "${offline_source}" "${destination}"
    cp -- "${offline_signature}" "${signature_destination}"
    return
  fi
  cache_file="${cache_dir}/${source_archive}"
  signature_cache="${cache_file}.asc"
  install -d -m 0750 -- "${cache_dir}"
  if [[ -f "${cache_file}" && -f "${signature_cache}" ]] &&
    verify_source_checksum "${cache_file}" &&
    verify_source_signature "${cache_file}" "${signature_cache}"; then
    cp -- "${cache_file}" "${destination}"
    cp -- "${signature_cache}" "${signature_destination}"
    return
  fi
  temporary_cache="$(mktemp "${cache_file}.tmp.XXXXXX")"
  temporary_signature="$(mktemp "${signature_cache}.tmp.XXXXXX")"
  if ! curl --proto '=https' --tlsv1.2 --fail --location --retry 3 --connect-timeout 20 \
      --output "${temporary_cache}" "${source_url}" ||
    ! curl --proto '=https' --tlsv1.2 --fail --location --retry 3 --connect-timeout 20 \
      --output "${temporary_signature}" "${source_signature_url}"; then
    rm -f -- "${temporary_cache}" "${temporary_signature}"
    die "MariaDB source download failed."
  fi
  verify_source_checksum "${temporary_cache}" || {
    rm -f -- "${temporary_cache}" "${temporary_signature}"
    die "MariaDB source checksum verification failed."
  }
  verify_source_signature "${temporary_cache}" "${temporary_signature}"
  chmod 0640 "${temporary_cache}" "${temporary_signature}"
  mv -f -- "${temporary_cache}" "${cache_file}"
  mv -f -- "${temporary_signature}" "${signature_cache}"
  cp -- "${cache_file}" "${destination}"
  cp -- "${signature_cache}" "${signature_destination}"
}
prepare_rollback() {
  install -d -m 0750 -- "${state_dir}"
  rm -rf -- "${rollback_dir}"
  install -d -m 0750 -- "${rollback_dir}/state"
  : >"${rollback_dir}/transaction-started"
  rm -f -- "${state_dir}/initialized-this-run"
  local unit
  for unit in mariadb.service mysqld.service mysql.service; do
    if systemctl is-enabled --quiet "${unit}" 2>/dev/null; then
      printf '%s\n' "${unit}" >"${rollback_dir}/was-enabled-unit"
      break
    fi
  done
  for unit in mariadb.service mysqld.service mysql.service; do
    if systemctl is-active --quiet "${unit}" 2>/dev/null; then
      printf '%s\n' "${unit}" >"${rollback_dir}/was-active-unit"
      systemctl stop "${unit}"
      break
    fi
  done
  [[ ! -e "${install_dir}" ]] || mv -- "${install_dir}" "${rollback_dir}/install"
  [[ ! -e "${unit_file}" ]] || cp -a -- "${unit_file}" "${rollback_dir}/mariadb.service"
  [[ ! -e "${config_file}" ]] || cp -a -- "${config_file}" "${rollback_dir}/my.cnf"
  [[ ! -e /etc/systemd/system/mysqld.service ]] ||
    cp -a -- /etc/systemd/system/mysqld.service "${rollback_dir}/legacy-mysqld.service"
  for unit in version patch-version install-parameters installed.json; do
    [[ ! -e "${state_dir}/${unit}" ]] || cp -a -- "${state_dir}/${unit}" "${rollback_dir}/state/${unit}"
  done
}
restore_rollback() {
  if [[ ! -f "${rollback_dir}/transaction-started" ]]; then
    echo "MariaDB rollback skipped because installation did not create a rollback point."
    return 0
  fi
  service_stop
  systemctl disable mariadb.service 2>/dev/null || true
  [[ ! -e "${install_dir}" ]] || rm -rf -- "${install_dir}"
  [[ ! -e "${rollback_dir}/install" ]] || mv -- "${rollback_dir}/install" "${install_dir}"
  if [[ -e "${rollback_dir}/mariadb.service" ]]; then
    cp -a -- "${rollback_dir}/mariadb.service" "${unit_file}"
  else
    rm -f -- "${unit_file}"
  fi
  if [[ -e "${rollback_dir}/my.cnf" ]]; then
    install -d -m 0755 -- "$(dirname -- "${config_file}")"
    cp -a -- "${rollback_dir}/my.cnf" "${config_file}"
  else
    rm -f -- "${config_file}"
  fi
  if [[ -e "${rollback_dir}/legacy-mysqld.service" ]]; then
    cp -a -- "${rollback_dir}/legacy-mysqld.service" /etc/systemd/system/mysqld.service
  fi
  for unit in version patch-version install-parameters installed.json; do
    rm -f -- "${state_dir}/${unit}"
    [[ ! -e "${rollback_dir}/state/${unit}" ]] ||
      cp -a -- "${rollback_dir}/state/${unit}" "${state_dir}/${unit}"
  done
  if [[ -e "${state_dir}/initialized-this-run" && -d "${data_dir}" ]]; then
    mv -- "${data_dir}" "${state_dir}/failed-data-$(date -u +%Y%m%dT%H%M%SZ)"
    rm -f -- "${state_dir}/initialized-this-run"
  fi
  systemctl daemon-reload
  if [[ -s "${rollback_dir}/was-enabled-unit" ]]; then
    systemctl enable "$(<"${rollback_dir}/was-enabled-unit")"
  fi
  if [[ -s "${rollback_dir}/was-active-unit" ]]; then
    systemctl enable --now "$(<"${rollback_dir}/was-active-unit")"
  fi
  rm -rf -- "${rollback_dir}"
}
commit_legacy_service() {
  legacy_managed_installation_present || return 0
  systemctl disable mysqld.service 2>/dev/null || true
  rm -f -- /etc/systemd/system/mysqld.service /etc/init.d/mysqld
  systemctl daemon-reload
}
