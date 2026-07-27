#!/usr/bin/env bash
set -Eeuo pipefail
umask 027

component_id="nginx"
software_version="${SOFTWARE_VERSION:-1.28.2}"
install_dir="${INSTALL_DIR:-/usr/local/nginx}"
web_root="${WEB_ROOT:-/data/wwwroot}"
log_dir="${LOG_DIR:-/data/wwwlogs}"
run_user="${RUN_USER:-www}"
run_group="${RUN_GROUP:-www}"
source_url="${SOURCE_URL:-https://nginx.org/download/nginx-1.28.2.tar.gz}"
source_sha256="${SOURCE_SHA256:-20e5e0f2c917acfb51120eec2fba9a4ba4e1e10fd28465067cc87a7d81a829a3}"
state_root="${ONEINSTACK_COMPONENT_STATE:-/var/lib/oneinstack/components}"
state_dir="${state_root}/${component_id}"
rollback_dir="${state_dir}/rollback"
unit_file="/etc/systemd/system/nginx.service"
nginx_binary="${install_dir}/sbin/nginx"
nginx_pid_file="${install_dir}/logs/nginx.pid"

die() { echo "ERROR: $*" >&2; exit 1; }
emit_progress() {
  local percent="$1" code="$2" message="$3" fd="${ONEINSTACK_PROGRESS_FD:-}"
  [[ "${fd}" =~ ^[0-9]+$ ]] || return 0
  message="${message//\\/\\\\}"; message="${message//\"/\\\"}"; message="${message//$'\n'/ }"
  printf '{"type":"progress","percent":%s,"code":"%s","message":"%s"}\n' \
    "${percent}" "${code}" "${message}" >&"${fd}" 2>/dev/null || true
}
require_root() { [[ "$(id -u)" -eq 0 ]] || die "This action must run as root."; }
require_command() { command -v "$1" >/dev/null 2>&1 || die "Required command not found: $1"; }
validate_identifier() { [[ "$1" =~ ^[a-z_][a-z0-9_-]{0,30}$ ]] || die "Invalid account identifier: $1"; }
validate_path() {
  local value="$1" label="$2"
  [[ "${value}" == /* && "$(realpath -m -- "${value}")" == "${value}" ]] || die "${label} must be a normalized absolute path."
  case "${value}" in /|/usr|/usr/local|/etc|/var|/data|/home|/root) die "${label} is too broad: ${value}" ;; esac
}
validate_inputs() {
  [[ "${software_version}" == "1.28.2" ]] || die "Unsupported Nginx version: ${software_version}"
  [[ "${source_url}" == https://* ]] || die "SOURCE_URL must use HTTPS."
  [[ "${source_sha256}" =~ ^[0-9a-f]{64}$ ]] || die "SOURCE_SHA256 must be a lowercase SHA-256 digest."
  validate_identifier "${run_user}"; validate_identifier "${run_group}"
  validate_path "${install_dir}" INSTALL_DIR; validate_path "${web_root}" WEB_ROOT
  validate_path "${log_dir}" LOG_DIR; validate_path "${state_root}" ONEINSTACK_COMPONENT_STATE
}
check_host() {
  [[ -r /etc/os-release ]] || die "/etc/os-release is unavailable."
  source /etc/os-release
  case "${ID:-}:${VERSION_ID:-}" in
    ubuntu:22.04|ubuntu:24.04|debian:12) ;;
    *) die "Unsupported Linux release: ${ID:-unknown} ${VERSION_ID:-unknown}" ;;
  esac
  case "$(dpkg --print-architecture)" in
    amd64|arm64) ;;
    *) die "Only amd64 and arm64 are supported by this package." ;;
  esac
}
install_dependencies() {
  export DEBIAN_FRONTEND=noninteractive
  source /etc/os-release
  local sources_file apt_arch mirror
  sources_file="$(mktemp /tmp/oneinstack-nginx-apt.XXXXXX.list)"
  apt_arch="$(dpkg --print-architecture)"
  trap 'rm -f -- "${sources_file}"' RETURN
  if [[ "${ID}" == "debian" ]]; then
    cat >"${sources_file}" <<EOF
deb https://deb.debian.org/debian ${VERSION_CODENAME} main
deb https://deb.debian.org/debian ${VERSION_CODENAME}-updates main
deb https://security.debian.org/debian-security ${VERSION_CODENAME}-security main
EOF
  else
    mirror="https://archive.ubuntu.com/ubuntu"
    [[ "${apt_arch}" == "amd64" ]] || mirror="https://ports.ubuntu.com/ubuntu-ports"
    cat >"${sources_file}" <<EOF
deb ${mirror} ${VERSION_CODENAME} main universe
deb ${mirror} ${VERSION_CODENAME}-updates main universe
deb ${mirror} ${VERSION_CODENAME}-security main universe
EOF
  fi
  local apt_options=(
    -o "Dir::Etc::sourcelist=${sources_file}"
    -o "Dir::Etc::sourceparts=-"
    -o "Acquire::Retries=5"
    -o "Acquire::https::Timeout=30"
  )
  apt-get "${apt_options[@]}" update
  if ! apt-get "${apt_options[@]}" install -y --no-install-recommends \
    build-essential ca-certificates curl libpcre2-dev libssl-dev zlib1g-dev; then
    echo "Dependency download was interrupted; refreshing indexes and retrying missing archives." >&2
    apt-get "${apt_options[@]}" update
    apt-get "${apt_options[@]}" install -y --fix-missing --no-install-recommends \
      build-essential ca-certificates curl libpcre2-dev libssl-dev zlib1g-dev
  fi
}
ensure_account() {
  getent group "${run_group}" >/dev/null || groupadd --system "${run_group}"
  id "${run_user}" >/dev/null 2>&1 || useradd --system --gid "${run_group}" --home-dir /nonexistent --shell /usr/sbin/nologin "${run_user}"
}
download_verified() {
  local destination="$1"
  curl --proto '=https' --tlsv1.2 --fail --location --retry 3 --connect-timeout 20 --output "${destination}" "${source_url}"
  printf '%s  %s\n' "${source_sha256}" "${destination}" | sha256sum --check --status || die "Nginx source checksum verification failed."
}
systemd_available() {
  command -v systemctl >/dev/null 2>&1 &&
    systemctl show-environment >/dev/null 2>&1
}
nginx_is_running() {
  if systemd_available; then
    systemctl is-active --quiet nginx.service
    return
  fi
  local pid=""
  [[ -r "${nginx_pid_file}" ]] && read -r pid <"${nginx_pid_file}"
  [[ "${pid}" =~ ^[0-9]+$ ]] && kill -0 "${pid}" 2>/dev/null
}
start_nginx() {
  if systemd_available; then
    systemctl start nginx.service
  else
    "${nginx_binary}"
  fi
}
stop_nginx() {
  if systemd_available; then
    systemctl stop nginx.service
    return
  fi
  nginx_is_running || return 0
  "${nginx_binary}" -s quit
  local attempt
  for attempt in {1..30}; do
    nginx_is_running || return 0
    sleep 1
  done
  die "Nginx did not stop within 30 seconds."
}
reload_nginx() {
  if systemd_available; then
    systemctl reload nginx.service
  else
    "${nginx_binary}" -s reload
  fi
}
prepare_rollback() {
  install -d -m 0750 -- "${state_dir}"
  rm -rf -- "${rollback_dir}"; install -d -m 0750 -- "${rollback_dir}"
  if nginx_is_running; then : >"${rollback_dir}/was-active"; stop_nginx; fi
  [[ ! -e "${install_dir}" ]] || mv -- "${install_dir}" "${rollback_dir}/install"
  [[ ! -e "${unit_file}" ]] || cp -a -- "${unit_file}" "${rollback_dir}/nginx.service"
}
restore_rollback() {
  stop_nginx 2>/dev/null || true
  [[ ! -e "${install_dir}" ]] || rm -rf -- "${install_dir}"
  [[ ! -e "${rollback_dir}/install" ]] || mv -- "${rollback_dir}/install" "${install_dir}"
  if [[ -e "${rollback_dir}/nginx.service" ]]; then cp -a -- "${rollback_dir}/nginx.service" "${unit_file}"; else rm -f -- "${unit_file}"; fi
  systemd_available && systemctl daemon-reload
  [[ ! -e "${rollback_dir}/was-active" ]] || start_nginx
}
