#!/usr/bin/env bash
set -Eeuo pipefail
umask 027

component_id="redis"
software_version="${SOFTWARE_VERSION:-7.4.8}"
install_dir="${INSTALL_DIR:-/usr/local/redis}"
data_dir="${DATA_DIR:-/data/redis}"
redis_port="${REDIS_PORT:-6379}"
redis_bind="${REDIS_BIND:-127.0.0.1 ::1}"
redis_password="${REDIS_PASSWORD:-}"
source_url="${SOURCE_URL:-https://download.redis.io/releases/redis-7.4.8.tar.gz}"
source_sha256="${SOURCE_SHA256:-f6773cb7d63be236c59c2917a82f1f08e47b77d89b2f0c9f53becb22b8ea4172}"
state_root="${ONEINSTACK_COMPONENT_STATE:-/var/lib/oneinstack/components}"
state_dir="${state_root}/${component_id}"
rollback_dir="${state_dir}/rollback"
unit_file="/etc/systemd/system/redis.service"
external_migration_dir="${rollback_dir}/external"

die() { echo "ERROR: $*" >&2; exit 1; }
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
  [[ "${software_version}" == "7.4.8" ]] || die "Unsupported Redis version: ${software_version}"
  [[ "${redis_port}" =~ ^[0-9]+$ && "${redis_port}" -ge 1 && "${redis_port}" -le 65535 ]] || die "Invalid REDIS_PORT."
  [[ "${redis_bind}" =~ ^[0-9a-fA-F:.[:space:]]+$ ]] || die "REDIS_BIND contains invalid characters."
  [[ -z "${redis_password}" || "${redis_password}" =~ ^[A-Za-z0-9_@%+=:,.!#?-]{8,128}$ ]] || die "REDIS_PASSWORD must be 8-128 safe characters."
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
  apt-get install -y --no-install-recommends build-essential ca-certificates curl libssl-dev pkg-config
}
ensure_account() {
  getent group redis >/dev/null || groupadd --system redis
  id redis >/dev/null 2>&1 || useradd --system --gid redis --home-dir "${data_dir}" --shell /usr/sbin/nologin redis
}
normalize_runtime_permissions() {
  ensure_account
  install -d -o redis -g redis -m 0750 -- "${data_dir}"
  chown -R redis:redis "${data_dir}"
  chmod 0750 "${data_dir}"
  chmod 0755 "${install_dir}" "${install_dir}/bin"
  chown root:redis "${install_dir}/etc/redis.conf"
  chmod 0640 "${install_dir}/etc/redis.conf"
  emit_progress 58 permissions.runtime.applied "Redis runtime ownership and configuration permissions applied"
}
verify_runtime_permissions() {
  require_command runuser
  runuser -u redis -- test -x "${install_dir}/bin/redis-server" ||
    die "Redis runtime user cannot execute the managed server binary."
  runuser -u redis -- test -r "${install_dir}/etc/redis.conf" ||
    die "Redis runtime user cannot read the managed configuration."
  runuser -u redis -- test -w "${data_dir}" ||
    die "Redis runtime user cannot write the managed data directory."
  emit_progress 10 permissions.runtime.verified "Redis runtime access verified as the redis user"
}
external_redis_detected() {
  [[ ! -f "${state_dir}/version" ]] &&
    { command -v redis-server >/dev/null 2>&1 || [[ -d /etc/redis ]] || [[ -d /var/lib/redis ]]; }
}
snapshot_external_redis() {
  external_redis_detected || return 0
  command -v redis-server >/dev/null 2>&1 || die "External Redis binary cannot be identified."
  local external_version external_major external_data="/var/lib/redis" package
  external_version="$(redis-server --version | sed -n 's/.*v=\([0-9][0-9.]*\).*/\1/p')"
  external_major="${external_version%%.*}"
  [[ "${external_major}" =~ ^[0-9]+$ && "${external_major}" -le 7 ]] ||
    die "External Redis data format is newer than the managed Redis version."
  if [[ -r /etc/redis/redis.conf ]]; then
    external_data="$(awk '$1 == "dir" {print $2; exit}' /etc/redis/redis.conf)"
    [[ -n "${external_data}" ]] || external_data="/var/lib/redis"
  fi
  external_data="$(realpath -m -- "${external_data}")"
  validate_path "${external_data}" EXTERNAL_DATA_DIR
  install -d -m 0700 -- "${external_migration_dir}"
  printf '%s\n' "${external_data}" >"${external_migration_dir}/data-path"
  [[ -d /etc/redis ]] && cp -a -- /etc/redis "${external_migration_dir}/config"
  for package in redis-server redis-tools redis; do
    dpkg-query -W -f='${Status}' "${package}" 2>/dev/null | grep -Fq 'install ok installed' || continue
    dpkg-query -W -f='${binary:Package}\t${Version}\n' "${package}" >>"${external_migration_dir}/package-versions"
  done
  systemctl is-active --quiet redis-server 2>/dev/null && : >"${external_migration_dir}/redis-server-active" || true
  systemctl is-active --quiet redis 2>/dev/null && : >"${external_migration_dir}/redis-active" || true
  systemctl stop redis redis-server 2>/dev/null || true
  emit_progress 25 migration.snapshot.created "External Redis package, configuration, and data-path snapshot created"
}
migrate_external_redis_data() {
  [[ -r "${external_migration_dir}/data-path" ]] || return 0
  local external_data
  read -r external_data <"${external_migration_dir}/data-path"
  [[ "${external_data}" != "${data_dir}" ]] || return 0
  [[ ! -e "${data_dir}/dump.rdb" && ! -d "${data_dir}/appendonlydir" ]] ||
    die "Target Redis data directory already contains persistent data."
  emit_progress 35 migration.data.copying "Copying external Redis persistence files"
  cp -a -- "${external_data}/." "${data_dir}/"
  chown -R redis:redis "${data_dir}"
}
commit_external_redis() {
  [[ -r "${external_migration_dir}/data-path" ]] || return 0
  local packages=() package version external_data
  while IFS=$'\t' read -r package version; do [[ -z "${package}" ]] || packages+=("${package}"); done <"${external_migration_dir}/package-versions"
  ((${#packages[@]} == 0)) || DEBIAN_FRONTEND=noninteractive apt-get remove -y "${packages[@]}"
  read -r external_data <"${external_migration_dir}/data-path"
  [[ "${external_data}" == "${data_dir}" ]] || rm -rf -- "${external_data}"
  rm -rf -- /etc/redis
  systemctl daemon-reload
  systemctl enable --now redis
  emit_progress 88 migration.commit.completed "External Redis package and data replacement committed"
}
download_verified() {
  local destination="$1"
  curl --proto '=https' --tlsv1.2 --fail --location --retry 3 --connect-timeout 20 --output "${destination}" "${source_url}"
  printf '%s  %s\n' "${source_sha256}" "${destination}" | sha256sum --check --status || die "Redis source checksum verification failed."
}
prepare_rollback() {
  install -d -m 0750 -- "${state_dir}"
  rm -rf -- "${rollback_dir}"; install -d -m 0750 -- "${rollback_dir}"
  if systemctl is-active --quiet redis 2>/dev/null; then : >"${rollback_dir}/was-active"; systemctl stop redis; fi
  [[ ! -e "${install_dir}" ]] || mv -- "${install_dir}" "${rollback_dir}/install"
  [[ ! -e "${unit_file}" ]] || cp -a -- "${unit_file}" "${rollback_dir}/redis.service"
  snapshot_external_redis
}
restore_rollback() {
  systemctl stop redis 2>/dev/null || true
  [[ ! -e "${install_dir}" ]] || rm -rf -- "${install_dir}"
  [[ ! -e "${rollback_dir}/install" ]] || mv -- "${rollback_dir}/install" "${install_dir}"
  if [[ -s "${external_migration_dir}/package-versions" ]]; then
    local specifications=() package version
    while IFS=$'\t' read -r package version; do specifications+=("${package}=${version}"); done <"${external_migration_dir}/package-versions"
    ((${#specifications[@]} == 0)) || DEBIAN_FRONTEND=noninteractive apt-get install -y "${specifications[@]}"
  fi
  [[ ! -d "${external_migration_dir}/config" ]] || { rm -rf -- /etc/redis; cp -a -- "${external_migration_dir}/config" /etc/redis; }
  if [[ -r "${external_migration_dir}/data-path" ]]; then
    local external_data
    read -r external_data <"${external_migration_dir}/data-path"
    if [[ "${external_data}" != "${data_dir}" && ! -e "${external_data}" ]]; then
      install -d -m 0750 -- "${external_data}"
      cp -a -- "${data_dir}/." "${external_data}/"
      chown -R redis:redis "${external_data}"
    fi
  fi
  if [[ -e "${rollback_dir}/redis.service" ]]; then cp -a -- "${rollback_dir}/redis.service" "${unit_file}"; else rm -f -- "${unit_file}"; fi
  systemctl daemon-reload
  [[ ! -e "${rollback_dir}/was-active" ]] || systemctl start redis
  [[ ! -f "${external_migration_dir}/redis-server-active" ]] || systemctl start redis-server
}
