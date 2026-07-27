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

die() { echo "ERROR: $*" >&2; exit 1; }
emit_progress() {
  local percent="$1" code="$2" message="$3" fd="${ONEINSTACK_PROGRESS_FD:-}"
  [[ "${fd}" =~ ^[0-9]+$ ]] || return 0
  message="${message//\\/\\\\}"; message="${message//\"/\\\"}"; message="${message//$'\n'/ }"
  printf '{"type":"progress","percent":%s,"code":"%s","message":"%s"}\n' \
    "${percent}" "${code}" "${message}" >&"${fd}" 2>/dev/null || true
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
}
restore_rollback() {
  systemctl stop redis 2>/dev/null || true
  [[ ! -e "${install_dir}" ]] || rm -rf -- "${install_dir}"
  [[ ! -e "${rollback_dir}/install" ]] || mv -- "${rollback_dir}/install" "${install_dir}"
  if [[ -e "${rollback_dir}/redis.service" ]]; then cp -a -- "${rollback_dir}/redis.service" "${unit_file}"; else rm -f -- "${unit_file}"; fi
  systemctl daemon-reload
  [[ ! -e "${rollback_dir}/was-active" ]] || systemctl start redis
}
