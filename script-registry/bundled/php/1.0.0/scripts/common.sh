#!/usr/bin/env bash
set -Eeuo pipefail
umask 027

component_id="php"
software_version="${SOFTWARE_VERSION:-8.3}"
install_dir="${INSTALL_DIR:-/usr/local/php}"
run_user="${RUN_USER:-www}"
run_group="${RUN_GROUP:-www}"
memory_limit="${PHP_MEMORY_LIMIT:-256M}"
state_root="${ONEINSTACK_COMPONENT_STATE:-/var/lib/oneinstack/components}"
state_dir="${state_root}/${component_id}"
rollback_dir="${state_dir}/rollback"
unit_file="/etc/systemd/system/php-fpm.service"

case "${software_version}" in
  8.1)
    patch_version="8.1.34"
    source_url="https://www.php.net/distributions/php-8.1.34.tar.xz"
    source_sha256="ffa9e0982e82eeaea848f57687b425ed173aa278fe563001310ae2638db5c251"
    ;;
  8.2)
    patch_version="8.2.30"
    source_url="https://www.php.net/distributions/php-8.2.30.tar.xz"
    source_sha256="bc90523e17af4db46157e75d0c9ef0b9d0030b0514e62c26ba7b513b8c4eb015"
    ;;
  8.3)
    patch_version="8.3.30"
    source_url="https://www.php.net/distributions/php-8.3.30.tar.xz"
    source_sha256="67f084d36852daab6809561a7c8023d130ca07fc6af8fb040684dd1414934d48"
    ;;
  *) patch_version="" ;;
esac

die() { echo "ERROR: $*" >&2; exit 1; }
emit_progress() {
  local percent="$1" code="$2" message="$3" fd="${ONEINSTACK_PROGRESS_FD:-}"
  [[ "${fd}" =~ ^[0-9]+$ ]] || return 0
  message="${message//\\/\\\\}"; message="${message//\"/\\\"}"; message="${message//$'\n'/ }"
  printf '{"type":"progress","percent":%s,"code":"%s","message":"%s"}\n' \
    "${percent}" "${code}" "${message}" >&"${fd}" || true
}
require_root() { [[ "$(id -u)" -eq 0 ]] || die "This action must run as root."; }
validate_identifier() { [[ "$1" =~ ^[a-z_][a-z0-9_-]{0,30}$ ]] || die "Invalid account identifier: $1"; }
validate_path() {
  local value="$1" label="$2"
  [[ "${value}" == /* && "$(realpath -m -- "${value}")" == "${value}" ]] || die "${label} must be a normalized absolute path."
  case "${value}" in /|/usr|/usr/local|/etc|/var|/data|/home|/root) die "${label} is too broad: ${value}" ;; esac
}
validate_inputs() {
  [[ -n "${patch_version}" ]] || die "Unsupported PHP version: ${software_version}"
  [[ "${memory_limit}" =~ ^[0-9]{2,5}[MG]$ ]] || die "PHP_MEMORY_LIMIT must look like 256M or 1G."
  validate_identifier "${run_user}"; validate_identifier "${run_group}"
  validate_path "${install_dir}" INSTALL_DIR; validate_path "${state_root}" ONEINSTACK_COMPONENT_STATE
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
  apt-get install -y --no-install-recommends build-essential ca-certificates curl pkg-config xz-utils \
    libxml2-dev libssl-dev libcurl4-openssl-dev libjpeg-dev libpng-dev libwebp-dev libfreetype6-dev \
    libonig-dev libzip-dev libsqlite3-dev libreadline-dev libsodium-dev libxslt1-dev libicu-dev libargon2-dev
}
ensure_account() {
  getent group "${run_group}" >/dev/null || groupadd --system "${run_group}"
  id "${run_user}" >/dev/null 2>&1 || useradd --system --gid "${run_group}" --home-dir /nonexistent --shell /usr/sbin/nologin "${run_user}"
}
download_verified() {
  local destination="$1"
  curl --proto '=https' --tlsv1.2 --fail --location --retry 3 --connect-timeout 20 --output "${destination}" "${source_url}"
  printf '%s  %s\n' "${source_sha256}" "${destination}" | sha256sum --check --status || die "PHP source checksum verification failed."
}
prepare_rollback() {
  install -d -m 0750 -- "${state_dir}"
  rm -rf -- "${rollback_dir}"; install -d -m 0750 -- "${rollback_dir}"
  if systemctl is-active --quiet php-fpm 2>/dev/null; then : >"${rollback_dir}/was-active"; systemctl stop php-fpm; fi
  [[ ! -e "${install_dir}" ]] || mv -- "${install_dir}" "${rollback_dir}/install"
  [[ ! -e "${unit_file}" ]] || cp -a -- "${unit_file}" "${rollback_dir}/php-fpm.service"
}
restore_rollback() {
  systemctl stop php-fpm 2>/dev/null || true
  [[ ! -e "${install_dir}" ]] || rm -rf -- "${install_dir}"
  [[ ! -e "${rollback_dir}/install" ]] || mv -- "${rollback_dir}/install" "${install_dir}"
  if [[ -e "${rollback_dir}/php-fpm.service" ]]; then cp -a -- "${rollback_dir}/php-fpm.service" "${unit_file}"; else rm -f -- "${unit_file}"; fi
  systemctl daemon-reload
  [[ ! -e "${rollback_dir}/was-active" ]] || systemctl start php-fpm
}
