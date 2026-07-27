#!/usr/bin/env bash
set -Eeuo pipefail
umask 027

component_id="firewalld"
software_version="${SOFTWARE_VERSION:-1.0.0}"
state_root="${ONEINSTACK_COMPONENT_STATE:-/var/lib/oneinstack/components}"
state_dir="${state_root}/${component_id}"
installed_marker="${state_dir}/installed-by-oneinstack"

die() { echo "ERROR: $*" >&2; exit 1; }
emit_progress() {
  local percent="$1" code="$2" message="$3" fd="${ONEINSTACK_PROGRESS_FD:-}"
  [[ "${fd}" =~ ^[0-9]+$ ]] || return 0
  message="${message//\\/\\\\}"; message="${message//\"/\\\"}"; message="${message//$'\n'/ }"
  printf '{"type":"progress","percent":%s,"code":"%s","message":"%s"}\n' \
    "${percent}" "${code}" "${message}" >&"${fd}" 2>/dev/null || true
}
require_root() { [[ "$(id -u)" -eq 0 ]] || die "This action must run as root."; }
require_command() { command -v "$1" >/dev/null 2>&1 || die "Required command is missing: $1"; }
validate_inputs() {
  [[ "${software_version}" == "1.0.0" ]] || die "Unsupported firewalld installer profile: ${software_version}"
  [[ "${state_root}" == /* && "$(realpath -m -- "${state_root}")" == "${state_root}" ]] ||
    die "ONEINSTACK_COMPONENT_STATE must be a normalized absolute path."
  case "${state_root}" in /|/usr|/etc|/var|/home|/root) die "ONEINSTACK_COMPONENT_STATE is too broad." ;; esac
}
detect_package_manager() {
  if command -v apt-get >/dev/null 2>&1; then
    printf 'apt'
  elif command -v dnf >/dev/null 2>&1; then
    printf 'dnf'
  elif command -v yum >/dev/null 2>&1; then
    printf 'yum'
  elif command -v zypper >/dev/null 2>&1; then
    printf 'zypper'
  else
    return 1
  fi
}
check_host() {
  [[ -r /etc/os-release ]] || die "/etc/os-release is unavailable."
  # shellcheck disable=SC1091
  source /etc/os-release
  case "${ID:-}" in
    ubuntu|debian|rhel|centos|rocky|almalinux|fedora|opensuse-leap|sles) ;;
    *) die "Unsupported Linux distribution: ${ID:-unknown}" ;;
  esac
  detect_package_manager >/dev/null || die "No supported package manager was found."
}
firewalld_package_installed() {
  if command -v dpkg-query >/dev/null 2>&1; then
    dpkg-query -W -f='${Status}' firewalld 2>/dev/null | grep -Fq 'install ok installed'
  elif command -v rpm >/dev/null 2>&1; then
    rpm -q firewalld >/dev/null 2>&1
  else
    return 1
  fi
}
protocol_database_ready() {
  command -v getent >/dev/null 2>&1 && getent protocols esp >/dev/null 2>&1
}
install_protocol_database() {
  local manager
  manager="$(detect_package_manager)" || die "No supported package manager was found."
  case "${manager}" in
    apt)
      export DEBIAN_FRONTEND=noninteractive
      apt-get update
      apt-get install -y --no-install-recommends netbase
      ;;
    dnf) dnf install -y setup ;;
    yum) yum install -y setup ;;
    zypper) zypper --non-interactive install --no-recommends netcfg ;;
  esac
}
ensure_protocol_database() {
  if protocol_database_ready; then
    return 0
  fi
  install_protocol_database
  protocol_database_ready ||
    die "The system protocol database is missing the IPsec ESP protocol."
}
firewalld_configuration_valid() {
  command -v firewall-offline-cmd >/dev/null 2>&1 &&
    firewall-offline-cmd --check-config >/dev/null 2>&1
}
install_firewalld_package() {
  local manager policy_created=0
  manager="$(detect_package_manager)" || die "No supported package manager was found."
  case "${manager}" in
    apt)
      export DEBIAN_FRONTEND=noninteractive
      if [[ ! -e /usr/sbin/policy-rc.d ]]; then
        printf '#!/bin/sh\nexit 101\n' >/usr/sbin/policy-rc.d
        chmod 0755 /usr/sbin/policy-rc.d
        policy_created=1
      fi
      trap 'if [[ "${policy_created:-0}" -eq 1 ]]; then rm -f -- /usr/sbin/policy-rc.d; fi' RETURN
      apt-get update
      apt-get install -y --no-install-recommends firewalld netbase
      if [[ "${policy_created}" -eq 1 ]]; then
        rm -f -- /usr/sbin/policy-rc.d
        policy_created=0
      fi
      trap - RETURN
      ;;
    dnf) dnf install -y firewalld setup ;;
    yum) yum install -y firewalld setup ;;
    zypper) zypper --non-interactive install --no-recommends firewalld netcfg ;;
  esac
}
remove_firewalld_package() {
  local manager
  manager="$(detect_package_manager)" || die "No supported package manager was found."
  case "${manager}" in
    apt)
      export DEBIAN_FRONTEND=noninteractive
      apt-get remove -y firewalld
      ;;
    dnf) dnf remove -y firewalld ;;
    yum) yum remove -y firewalld ;;
    zypper) zypper --non-interactive remove firewalld ;;
  esac
}
ensure_firewalld_stopped() {
  if command -v systemctl >/dev/null 2>&1; then
    systemctl stop firewalld >/dev/null 2>&1 || true
    systemctl disable firewalld >/dev/null 2>&1 || true
  fi
}
