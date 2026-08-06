#!/usr/bin/env bash
set -Eeuo pipefail
umask 027

component_id="mysql"
software_version="${SOFTWARE_VERSION:-8.0}"
patch_version="8.0.45"
install_dir="${INSTALL_DIR:-/usr/local/mysql}"
data_dir="${DATA_DIR:-/data/mysql}"
mysql_port="${MYSQL_PORT:-3306}"
bind_address="${MYSQL_BIND_ADDRESS:-127.0.0.1}"
mysql_password="${MYSQL_PASSWORD:-}"
source_url="${SOURCE_URL:-https://cdn.mysql.com/Downloads/MySQL-8.0/mysql-8.0.45-linux-glibc2.28-x86_64.tar.xz}"
source_sha256="${SOURCE_SHA256:-c09137539ab42590c8682d498716e6b97a45d83d2188339fa2e980dd54b6e0af}"
state_root="${ONEINSTACK_COMPONENT_STATE:-/var/lib/oneinstack/components}"
state_dir="${state_root}/${component_id}"
rollback_dir="${state_dir}/rollback"
unit_file="/etc/systemd/system/mysql.service"
config_file="/etc/my.cnf"

die() { echo "ERROR: $*" >&2; exit 1; }
emit_progress() {
  local percent="$1" code="$2" message="$3" fd="${ONEINSTACK_PROGRESS_FD:-}"
  [[ "${fd}" =~ ^[0-9]+$ ]] || return 0
  message="${message//\\/\\\\}"; message="${message//\"/\\\"}"; message="${message//$'\n'/ }"
  printf '{"type":"progress","percent":%s,"code":"%s","message":"%s"}\n' \
    "${percent}" "${code}" "${message}" >&"${fd}" || true
}
require_root() { [[ "$(id -u)" -eq 0 ]] || die "This action must run as root."; }
validate_path() {
  local value="$1" label="$2"
  [[ "${value}" == /* && "$(realpath -m -- "${value}")" == "${value}" ]] || die "${label} must be a normalized absolute path."
  case "${value}" in /|/usr|/usr/local|/etc|/var|/data|/home|/root) die "${label} is too broad: ${value}" ;; esac
}
validate_inputs() {
  [[ "${software_version}" == "8.0" ]] || die "Unsupported MySQL version: ${software_version}"
  [[ "${mysql_port}" =~ ^[0-9]+$ && "${mysql_port}" -ge 1 && "${mysql_port}" -le 65535 ]] || die "Invalid MYSQL_PORT."
  [[ "${bind_address}" =~ ^[0-9a-fA-F:.]+$ ]] || die "MYSQL_BIND_ADDRESS must be an IP address."
  [[ -z "${mysql_password}" || "${mysql_password}" =~ ^[A-Za-z0-9_@%+=:,.!#?-]{12,128}$ ]] || die "MYSQL_PASSWORD must be 12-128 safe characters."
  [[ "${source_url}" == https://* ]] || die "SOURCE_URL must use HTTPS."
  [[ "${source_sha256}" =~ ^[0-9a-f]{64}$ ]] || die "SOURCE_SHA256 must be a lowercase SHA-256 digest."
  validate_path "${install_dir}" INSTALL_DIR; validate_path "${data_dir}" DATA_DIR
  validate_path "${state_root}" ONEINSTACK_COMPONENT_STATE
}
check_host() {
  source /etc/os-release
  [[ "${ID:-}" == "ubuntu" ]] || die "Only Ubuntu is supported."
  case "${VERSION_ID:-}" in 22.04|24.04) ;; *) die "Unsupported Ubuntu release: ${VERSION_ID:-unknown}" ;; esac
  [[ "$(dpkg --print-architecture)" == "amd64" ]] || die "Only amd64 is supported."
}
install_dependencies() {
  export DEBIAN_FRONTEND=noninteractive
  apt-get update
  local aio_package="libaio1"
  apt-cache show libaio1 >/dev/null 2>&1 || aio_package="libaio1t64"
  apt-get install -y --no-install-recommends ca-certificates curl "${aio_package}" libnuma1 libtinfo6 xz-utils
}
ensure_account() {
  getent group mysql >/dev/null || groupadd --system mysql
  id mysql >/dev/null 2>&1 || useradd --system --gid mysql --home-dir "${data_dir}" --shell /usr/sbin/nologin mysql
}
download_verified() {
  local destination="$1"
  curl --proto '=https' --tlsv1.2 --fail --location --retry 3 --connect-timeout 20 --output "${destination}" "${source_url}"
  printf '%s  %s\n' "${source_sha256}" "${destination}" | sha256sum --check --status || die "MySQL archive checksum verification failed."
}
prepare_rollback() {
  install -d -m 0750 -- "${state_dir}"
  rm -rf -- "${rollback_dir}"; install -d -m 0750 -- "${rollback_dir}"
  if systemctl is-active --quiet mysql 2>/dev/null; then : >"${rollback_dir}/was-active"; systemctl stop mysql; fi
  [[ ! -e "${install_dir}" ]] || mv -- "${install_dir}" "${rollback_dir}/install"
  [[ ! -e "${unit_file}" ]] || cp -a -- "${unit_file}" "${rollback_dir}/mysql.service"
  [[ ! -e "${config_file}" ]] || cp -a -- "${config_file}" "${rollback_dir}/my.cnf"
}
restore_rollback() {
  systemctl stop mysql 2>/dev/null || true
  [[ ! -e "${install_dir}" ]] || rm -rf -- "${install_dir}"
  [[ ! -e "${rollback_dir}/install" ]] || mv -- "${rollback_dir}/install" "${install_dir}"
  if [[ -e "${rollback_dir}/mysql.service" ]]; then cp -a -- "${rollback_dir}/mysql.service" "${unit_file}"; else rm -f -- "${unit_file}"; fi
  if [[ -e "${rollback_dir}/my.cnf" ]]; then cp -a -- "${rollback_dir}/my.cnf" "${config_file}"; else rm -f -- "${config_file}"; fi
  if [[ -e "${state_dir}/initialized-this-run" && -d "${data_dir}" ]]; then
    mv -- "${data_dir}" "${state_dir}/failed-data-$(date -u +%Y%m%dT%H%M%SZ)"
    rm -f -- "${state_dir}/initialized-this-run"
  fi
  systemctl daemon-reload
  [[ ! -e "${rollback_dir}/was-active" ]] || systemctl start mysql
}
