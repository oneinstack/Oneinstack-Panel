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
external_migration_dir="${rollback_dir}/external"

die() { echo "ERROR: $*" >&2; exit 1; }
require_command() { command -v "$1" >/dev/null 2>&1 || die "Required command is missing: $1"; }
emit_progress() {
  local percent="$1" code="$2" message="$3" fd="${ONEINSTACK_PROGRESS_FD:-}"
  [[ "${fd}" =~ ^[0-9]+$ ]] || return 0
  message="${message//\\/\\\\}"; message="${message//\"/\\\"}"; message="${message//$'\n'/ }"
  printf '{"type":"progress","percent":%s,"code":"%s","message":"%s"}\n' \
    "${percent}" "${code}" "${message}" 1>&"${fd}" 2>/dev/null || true
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
  if ! apt-cache policy libaio1 2>/dev/null |
    awk '/Candidate:/ {found=1; if ($2 != "(none)") available=1} END {exit !(found && available)}'; then
    aio_package="libaio1t64"
  fi
  apt-get install -y --no-install-recommends ca-certificates curl "${aio_package}" libnuma1 libtinfo6 xz-utils
  if [[ "${aio_package}" == "libaio1t64" ]] &&
    ! ldconfig -p 2>/dev/null | grep -q 'libaio\.so\.1 '; then
    local aio_target aio_dir
    aio_target="$(dpkg-query -L libaio1t64 |
      awk '/\/libaio\.so\.1t64\.[0-9.]+$/ {print; exit}')"
    [[ -n "${aio_target}" && -f "${aio_target}" ]] ||
      die "libaio1t64 compatibility library was not found."
    aio_dir="$(dirname -- "${aio_target}")"
    local compatibility_path="${aio_dir}/libaio.so.1"
    if [[ -L "${compatibility_path}" ]]; then
      [[ "$(realpath -m -- "${compatibility_path}")" == "$(realpath -m -- "${aio_target}")" ]] ||
        die "Existing libaio.so.1 compatibility link points to an unexpected library."
    elif [[ -e "${compatibility_path}" ]]; then
      die "Refusing to replace an existing libaio.so.1 file."
    else
      ln -s -- "$(basename -- "${aio_target}")" "${compatibility_path}"
    fi
    ldconfig
    [[ -r "${compatibility_path}" ]] ||
      die "libaio.so.1 compatibility library is not readable."
  fi
}
ensure_account() {
  getent group mysql >/dev/null || groupadd --system mysql
  id mysql >/dev/null 2>&1 || useradd --system --gid mysql --home-dir "${data_dir}" --shell /usr/sbin/nologin mysql
}
normalize_runtime_permissions() {
  ensure_account
  install -d -o mysql -g mysql -m 0750 -- "${data_dir}"
  chown -R mysql:mysql "${data_dir}"
  chmod 0750 "${data_dir}"
  chmod 0755 "${install_dir}" "${install_dir}/bin"
  emit_progress 58 permissions.runtime.applied "MySQL runtime ownership and directory permissions applied"
}
verify_runtime_permissions() {
  require_command runuser
  runuser -u mysql -- test -x "${install_dir}/bin/mysqld" ||
    die "MySQL runtime user cannot execute the managed server binary."
  runuser -u mysql -- test -r "${data_dir}" ||
    die "MySQL runtime user cannot read the managed data directory."
  runuser -u mysql -- test -w "${data_dir}" ||
    die "MySQL runtime user cannot write the managed data directory."
  emit_progress 10 permissions.runtime.verified "MySQL runtime access verified as the mysql user"
}
external_mysql_detected() {
  [[ ! -f "${state_dir}/version" ]] &&
    { command -v mysqld >/dev/null 2>&1 || [[ -d /var/lib/mysql ]] ||
      dpkg-query -W -f='${Status}' mysql-server 2>/dev/null | grep -Fq 'install ok installed'; }
}
snapshot_external_mysql() {
  external_mysql_detected || return 0
  if command -v mariadbd >/dev/null 2>&1 ||
    dpkg-query -W -f='${Status}' mariadb-server 2>/dev/null | grep -Fq 'install ok installed'; then
    die "MariaDB data cannot be migrated automatically to MySQL 8.0."
  fi
  command -v mysqld >/dev/null 2>&1 || die "External MySQL server binary cannot be identified."
  mysqld --version 2>/dev/null | grep -Fq 'Ver 8.0' ||
    die "Only an external MySQL 8.0 installation can be migrated automatically."
  install -d -m 0700 -- "${external_migration_dir}"
  local external_data="/var/lib/mysql" package
  external_data="$(mysqld --verbose --help 2>/dev/null | awk '$1 == "datadir" {print $2; exit}')"
  [[ -n "${external_data}" ]] || external_data="/var/lib/mysql"
  external_data="$(realpath -m -- "${external_data}")"
  validate_path "${external_data}" EXTERNAL_DATA_DIR
  printf '%s\n' "${external_data}" >"${external_migration_dir}/data-path"
  for package in mysql-server mysql-server-8.0 mysql-community-server mysql-community-client; do
    dpkg-query -W -f='${Status}' "${package}" 2>/dev/null | grep -Fq 'install ok installed' || continue
    dpkg-query -W -f='${binary:Package}\t${Version}\n' "${package}" >>"${external_migration_dir}/package-versions"
  done
  [[ -f /etc/mysql/my.cnf ]] && cp -a -- /etc/mysql "${external_migration_dir}/config"
  systemctl is-active --quiet mysql 2>/dev/null && : >"${external_migration_dir}/was-active" || true
  systemctl is-enabled --quiet mysql 2>/dev/null && : >"${external_migration_dir}/was-enabled" || true
  systemctl stop mysql mysqld 2>/dev/null || true
  emit_progress 25 migration.snapshot.created "External MySQL 8.0 package, configuration, and data-path snapshot created"
}
migrate_external_mysql_data() {
  [[ -r "${external_migration_dir}/data-path" ]] || return 0
  local external_data
  read -r external_data <"${external_migration_dir}/data-path"
  [[ "${external_data}" != "${data_dir}" ]] || return 0
  [[ -d "${external_data}/mysql" ]] || die "External MySQL data dictionary is missing."
  [[ ! -e "${data_dir}/mysql" ]] || die "Target MySQL data directory already contains a database."
  emit_progress 35 migration.data.copying "Copying external MySQL data into the managed data directory"
  install -d -o mysql -g mysql -m 0750 -- "${data_dir}"
  cp -a -- "${external_data}/." "${data_dir}/"
  chown -R mysql:mysql "${data_dir}"
}
commit_external_mysql() {
  [[ -r "${external_migration_dir}/data-path" ]] || return 0
  local packages=() package version external_data
  if [[ -s "${external_migration_dir}/package-versions" ]]; then
    while IFS=$'\t' read -r package version; do packages+=("${package}"); done <"${external_migration_dir}/package-versions"
    ((${#packages[@]} == 0)) || DEBIAN_FRONTEND=noninteractive apt-get remove -y "${packages[@]}"
  fi
  read -r external_data <"${external_migration_dir}/data-path"
  if [[ "${external_data}" != "${data_dir}" ]]; then rm -rf -- "${external_data}"; fi
  rm -rf -- /etc/mysql
  systemctl daemon-reload
  systemctl enable --now mysql
  emit_progress 88 migration.commit.completed "External MySQL package and data replacement committed"
}
download_verified() {
  local destination="$1"
  local cache_dir="/var/cache/oneinstack/downloads"
  local cache_file="${cache_dir}/mysql-${patch_version}-linux-amd64.tar.xz"
  install -d -m 0750 -- "${cache_dir}"
  if [[ -f "${cache_file}" ]] &&
    printf '%s  %s\n' "${source_sha256}" "${cache_file}" |
      sha256sum --check --status; then
    cp -- "${cache_file}" "${destination}"
    return
  fi
  local temporary_cache
  temporary_cache="$(mktemp "${cache_file}.tmp.XXXXXX")"
  trap 'rm -f -- "${temporary_cache}"' RETURN
  curl --proto '=https' --tlsv1.2 --fail --location --retry 3 \
    --connect-timeout 20 --output "${temporary_cache}" "${source_url}"
  printf '%s  %s\n' "${source_sha256}" "${temporary_cache}" |
    sha256sum --check --status ||
    die "MySQL archive checksum verification failed."
  chmod 0640 "${temporary_cache}"
  mv -f -- "${temporary_cache}" "${cache_file}"
  trap - RETURN
  cp -- "${cache_file}" "${destination}"
}
prepare_rollback() {
  install -d -m 0750 -- "${state_dir}"
  rm -rf -- "${rollback_dir}"; install -d -m 0750 -- "${rollback_dir}"
  : >"${rollback_dir}/transaction-started"
  rm -f -- "${state_dir}/initialized-this-run"
  if systemctl is-active --quiet mysql 2>/dev/null; then : >"${rollback_dir}/was-active"; systemctl stop mysql; fi
  [[ ! -e "${install_dir}" ]] || mv -- "${install_dir}" "${rollback_dir}/install"
  [[ ! -e "${unit_file}" ]] || cp -a -- "${unit_file}" "${rollback_dir}/mysql.service"
  [[ ! -e "${config_file}" ]] || cp -a -- "${config_file}" "${rollback_dir}/my.cnf"
  snapshot_external_mysql
}
restore_rollback() {
  if [[ ! -f "${rollback_dir}/transaction-started" ]]; then
    echo "MySQL rollback skipped because installation did not create a rollback point."
    return 0
  fi
  systemctl stop mysql 2>/dev/null || true
  [[ ! -e "${install_dir}" ]] || rm -rf -- "${install_dir}"
  [[ ! -e "${rollback_dir}/install" ]] || mv -- "${rollback_dir}/install" "${install_dir}"
  if [[ -e "${rollback_dir}/mysql.service" ]]; then cp -a -- "${rollback_dir}/mysql.service" "${unit_file}"; else rm -f -- "${unit_file}"; fi
  if [[ -e "${rollback_dir}/my.cnf" ]]; then cp -a -- "${rollback_dir}/my.cnf" "${config_file}"; else rm -f -- "${config_file}"; fi
  if [[ -s "${external_migration_dir}/package-versions" ]]; then
    local specifications=() package version
    while IFS=$'\t' read -r package version; do specifications+=("${package}=${version}"); done <"${external_migration_dir}/package-versions"
    ((${#specifications[@]} == 0)) || DEBIAN_FRONTEND=noninteractive apt-get install -y "${specifications[@]}"
  fi
  [[ ! -d "${external_migration_dir}/config" ]] || { rm -rf -- /etc/mysql; cp -a -- "${external_migration_dir}/config" /etc/mysql; }
  if [[ -r "${external_migration_dir}/data-path" ]]; then
    local external_data
    read -r external_data <"${external_migration_dir}/data-path"
    if [[ "${external_data}" != "${data_dir}" && ! -d "${external_data}/mysql" && -d "${data_dir}/mysql" ]]; then
      install -d -m 0750 -- "${external_data}"
      cp -a -- "${data_dir}/." "${external_data}/"
      chown -R mysql:mysql "${external_data}"
    fi
  fi
  if [[ -e "${state_dir}/initialized-this-run" && -d "${data_dir}" ]]; then
    mv -- "${data_dir}" "${state_dir}/failed-data-$(date -u +%Y%m%dT%H%M%SZ)"
    rm -f -- "${state_dir}/initialized-this-run"
  fi
  systemctl daemon-reload
  [[ ! -e "${rollback_dir}/was-active" ]] || systemctl start mysql
  rm -rf -- "${rollback_dir}"
}
