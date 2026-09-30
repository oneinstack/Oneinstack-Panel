#!/usr/bin/env bash
set -Eeuo pipefail
umask 027

component_id="adminer"
component_name="Adminer"
package_version="1.0.7"
software_version="${SOFTWARE_VERSION:-6.1.0}"
source_url="https://github.com/vrana/adminer/releases/download/v6.1.0/adminer-6.1.0.php"
source_sha256="95bf24b510b41904446f720f4f1212c9e28b1d523f3df44259480d7a12ea181e"
artifact_name="adminer-6.1.0.php"
install_dir="/usr/local/adminer"
state_root="${ONEINSTACK_COMPONENT_STATE:-/var/lib/oneinstack/components}"
state_dir="${state_root}/${component_id}"
runtime_state="${state_dir}/runtime-config"
installed_state="${state_dir}/installed.json"
managed_php_path_acl_file="${state_dir}/managed-php-path-acl"
rollback_dir="${state_dir}/rollback"
php_path_acl_transaction_file=""
install_candidate="/usr/local/.adminer.candidate"
install_backup="/usr/local/.adminer.rollback"
legacy_route_path="/data/wwwroot/adminer"
install_mode="${ONEINSTACK_INSTALL_MODE:-center}"
offline_package_path="${ONEINSTACK_OFFLINE_PACKAGE_PATH:-}"
offline_bundle_id="${ONEINSTACK_OFFLINE_BUNDLE_ID:-}"
offline_bundle_digest="${ONEINSTACK_OFFLINE_BUNDLE_DIGEST:-}"
public_path="${ADMINER_PUBLIC_PATH:-/adminer/}"
access_policy="${ADMINER_ACCESS_POLICY:-public}"
allowed_cidrs="${ADMINER_ALLOWED_CIDRS:-}"
default_driver="${ADMINER_DEFAULT_DRIVER:-mysql}"
default_server="${ADMINER_DEFAULT_SERVER:-127.0.0.1}"

detected_os_id=""
detected_os_version=""
detected_os_release_version=""
detected_architecture=""
package_manager=""
php_binary=""
php_version=""
php_service=""
php_socket=""
php_run_user=""
php_run_group=""
web_component=""
web_service=""
web_binary=""
web_config=""
web_site_config=""
web_document_root=""
web_port=""
web_host=""
route_path=""

log() { printf '[%s] [%s] %s\n' "$(date -u '+%Y-%m-%dT%H:%M:%SZ')" "${component_name}" "$*" >&2; }
log_error() { printf '[%s] [%s] ERROR: %s\n' "$(date -u '+%Y-%m-%dT%H:%M:%SZ')" "${component_name}" "$*" >&2; }
die_code() { local code="$1"; shift; printf 'ERROR_CODE=%s\nERROR: %s\n' "${code}" "$*" >&2; exit 1; }
emit_progress() {
  local fd="${ONEINSTACK_PROGRESS_FD:-}" message="${3//\\/\\\\}"
  message="${message//\"/\\\"}"
  [[ "${fd}" =~ ^[0-9]+$ ]] || return 0
  (printf '{"type":"progress","percent":%s,"code":"%s","message":"%s"}\n' "$1" "$2" "${message}" >&"${fd}") 2>/dev/null || true
}
require_root() { [[ "$(id -u)" -eq 0 ]] || die_code ROOT_REQUIRED "This action must run as root."; }
require_command() { command -v "$1" >/dev/null 2>&1 || die_code REQUIRED_COMMAND_MISSING "Required command not found: $1"; }
validate_path() {
  local value="$1" label="$2"
  [[ "${value}" == /* && "$(realpath -m -- "${value}")" == "${value}" ]] ||
    die_code INVALID_PATH "${label} must be a normalized absolute path."
  case "${value}" in /|/usr|/usr/local|/etc|/var|/data|/home|/root) die_code INVALID_PATH "${label} is too broad." ;; esac
}
read_parameter() {
  local file="$1" key="$2"
  [[ -r "${file}" ]] || return 0
  sed -nE "s|^${key}=(.*)$|\\1|p" "${file}" | head -n1
}
normalize_architecture() {
  case "$1" in
    x86_64|amd64) printf 'amd64\n' ;;
    aarch64|arm64) printf 'arm64\n' ;;
    *) return 1 ;;
  esac
}
normalize_platform_version() {
  local os_id="$1" release_version="$2"
  case "${os_id}" in
    debian|rhel|rocky|almalinux|ol|centos|fedora|amzn|sles) printf '%s\n' "${release_version%%.*}" ;;
    *) printf '%s\n' "${release_version}" ;;
  esac
}
detect_platform() {
  [[ -r /etc/os-release ]] || die_code UNSUPPORTED_PLATFORM "Cannot read /etc/os-release."
  # shellcheck disable=SC1091
  source /etc/os-release
  detected_os_id="${ID,,}"
  detected_os_release_version="${VERSION_ID//\"/}"
  detected_os_version="$(normalize_platform_version "${detected_os_id}" "${detected_os_release_version}")"
  detected_architecture="$(normalize_architecture "$(uname -m)")" ||
    die_code UNSUPPORTED_ARCHITECTURE "Only amd64 and arm64 are supported."
  case "${detected_os_id}:${detected_os_version}" in
    ubuntu:22.04|ubuntu:24.04|ubuntu:26.04|debian:11|debian:12|debian:13) package_manager=apt ;;
    rhel:8|rhel:9|rhel:10|rocky:8|rocky:9|rocky:10|almalinux:8|almalinux:9|almalinux:10|ol:8|ol:9|ol:10|centos:8|centos:9|centos:10|fedora:40|fedora:41|fedora:42|fedora:43|fedora:44|amzn:2023) package_manager=dnf ;;
    centos:7) package_manager=yum ;;
    sles:15|sles:16|opensuse-leap:15.6|opensuse-leap:16.0|opensuse:15.6|opensuse:16.0) package_manager=zypper ;;
    *) die_code UNSUPPORTED_PLATFORM "Adminer ${package_version} does not support ${detected_os_id} ${detected_os_release_version}." ;;
  esac
}
validate_public_path() {
  [[ "$1" =~ ^/[A-Za-z0-9][A-Za-z0-9._-]{0,63}/$ ]] ||
    die_code ADMINER_PUBLIC_PATH_INVALID "Adminer public path must be one safe URL segment such as /adminer/."
}
validate_cidrs() {
  local value="$1" item address prefix
  [[ -n "${value}" ]] || return 0
  while IFS= read -r item; do
    [[ -n "${item}" ]] || die_code ADMINER_ALLOWED_CIDRS_INVALID "Adminer CIDR list contains an empty item."
    [[ "${item}" == */* ]] || die_code ADMINER_ALLOWED_CIDRS_INVALID "Invalid IPv4/IPv6 CIDR: ${item}."
    address="${item%/*}"; prefix="${item##*/}"
    [[ "${prefix}" =~ ^[0-9]+$ ]] || die_code ADMINER_ALLOWED_CIDRS_INVALID "Invalid IPv4/IPv6 CIDR: ${item}."
    if [[ "${address}" == *:* ]]; then
      [[ "${address}" =~ ^[0-9A-Fa-f:]+$ && "${prefix}" -le 128 ]] || die_code ADMINER_ALLOWED_CIDRS_INVALID "Invalid IPv6 CIDR: ${item}."
    else
      [[ "${address}" =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$ && "${prefix}" -le 32 ]] || die_code ADMINER_ALLOWED_CIDRS_INVALID "Invalid IPv4 CIDR: ${item}."
    fi
  done < <(printf '%s' "${value}" | tr ',' '\n' | sed 's/^[[:space:]]*//;s/[[:space:]]*$//')
}
validate_cidrs_with_php() {
  local item address
  [[ -n "${allowed_cidrs}" ]] || return 0
  while IFS= read -r item; do
    address="${item%/*}"
    # shellcheck disable=SC2016
    "${php_binary}" -r 'exit(@inet_pton($argv[1]) === false ? 1 : 0);' "${address}" ||
      die_code ADMINER_ALLOWED_CIDRS_INVALID "Invalid IPv4/IPv6 CIDR: ${item}."
  done < <(printf '%s' "${allowed_cidrs}" | tr ',' '\n' | sed 's/^[[:space:]]*//;s/[[:space:]]*$//')
}
validate_default_server() {
  local value="$1"
  [[ -n "${value}" && "${#value}" -le 255 && ! "${value}" =~ [[:space:]] && ! "${value}" =~ [@/?#] && "${value}" != *"://"* ]] ||
    die_code ADMINER_DEFAULT_SERVER_INVALID "Default database server must be a host or IP with an optional port; credentials and URLs are forbidden."
  [[ ! "${value}" =~ [[:cntrl:]] ]] || die_code ADMINER_DEFAULT_SERVER_INVALID "Default database server contains control characters."
  [[ "${value}" =~ ^([A-Za-z0-9][A-Za-z0-9.-]{0,252}|\[[0-9A-Fa-f:]+\]|[0-9A-Fa-f:]+)(:[0-9]{1,5})?$ ]] ||
    die_code ADMINER_DEFAULT_SERVER_INVALID "Default database server format is invalid."
  local port=""
  if [[ "${value}" =~ \]:([0-9]+)$ ]]; then port="${BASH_REMATCH[1]}"; elif [[ "${value}" =~ ^[^:]+:([0-9]+)$ ]]; then port="${BASH_REMATCH[1]}"; fi
  [[ -z "${port}" || ("${port}" -ge 1 && "${port}" -le 65535) ]] ||
    die_code ADMINER_DEFAULT_SERVER_INVALID "Default database server port must be between 1 and 65535."
}
validate_configuration_values() {
  validate_public_path "${public_path}"
  case "${access_policy}" in public|local|allowlist) ;; *) die_code ADMINER_ACCESS_POLICY_INVALID "Access policy must be public, local, or allowlist." ;; esac
  [[ "${access_policy}" != allowlist || -n "${allowed_cidrs}" ]] ||
    die_code ADMINER_ALLOWED_CIDRS_REQUIRED "Allowed CIDRs are required for the allowlist access policy."
  validate_cidrs "${allowed_cidrs}"
  case "${default_driver}" in mysql|pgsql) ;; *) die_code ADMINER_DEFAULT_DRIVER_INVALID "Default driver must be mysql or pgsql." ;; esac
  validate_default_server "${default_server}"
}
validate_common() {
  require_command realpath
  require_command sha256sum
  require_command install
  validate_path "${state_root}" ONEINSTACK_COMPONENT_STATE
  [[ "${software_version}" == 6.1.0 ]] || die_code ADMINER_VERSION_UNSUPPORTED "Only Adminer 6.1.0 is published by this component package."
  case "${install_mode}" in
    center)
      [[ -z "${offline_package_path}" && -z "${offline_bundle_id}" && -z "${offline_bundle_digest}" ]] ||
        die_code OFFLINE_MODE_INVALID "Offline Bundle metadata cannot be used in Center mode."
      ;;
    offline)
      [[ -n "${offline_package_path}" ]] || die_code OFFLINE_BUNDLE_MISSING "Offline mode requires ONEINSTACK_OFFLINE_PACKAGE_PATH."
      validate_path "${offline_package_path}" ONEINSTACK_OFFLINE_PACKAGE_PATH
      [[ "${offline_bundle_digest}" =~ ^[0-9a-f]{64}$ && "${offline_bundle_id}" == "sha256:${offline_bundle_digest}" ]] ||
        die_code OFFLINE_BUNDLE_IDENTITY_MISMATCH "Panel Bundle identity and SHA-256 are required and must match."
      ;;
    *) die_code INSTALL_MODE_INVALID "ONEINSTACK_INSTALL_MODE must be center or offline." ;;
  esac
  validate_configuration_values
  detect_platform
}
install_online_dependencies() {
  local need_acl=false
  local -a packages=(ca-certificates curl)
  [[ "${install_mode}" == center ]] || return 0
  if [[ -n "${php_run_user}" && -n "${web_document_root}" ]] &&
    ! run_as_php_fpm test -x "${web_document_root}"; then
    need_acl=true
    packages+=(acl)
  fi
  if command -v curl >/dev/null 2>&1 &&
    [[ -r /etc/ssl/certs/ca-certificates.crt || -r /etc/pki/tls/certs/ca-bundle.crt || -r /etc/ssl/ca-bundle.pem ]] &&
    { [[ "${need_acl}" == false ]] || { command -v getfacl >/dev/null 2>&1 && command -v setfacl >/dev/null 2>&1; }; }; then
    return 0
  fi
  case "${package_manager}" in
    apt) DEBIAN_FRONTEND=noninteractive apt-get update; DEBIAN_FRONTEND=noninteractive apt-get install -y "${packages[@]}" ;;
    dnf) dnf install -y "${packages[@]}" ;;
    yum) yum install -y "${packages[@]}" ;;
    zypper) zypper --non-interactive refresh; zypper --non-interactive install "${packages[@]}" ;;
  esac
}
managed_component_present() {
  local component="$1" directory="${state_root}/$1"
  [[ -f "${directory}/installed.json" || -f "${directory}/installed" || -f "${directory}/managed" || -f "${directory}/runtime-params" || -f "${directory}/install-parameters" ]]
}
detect_managed_php() {
  managed_component_present php || die_code PHP_MANAGED_REQUIRED "A OneinStack-managed PHP-FPM installation is required."
  local params="${state_root}/php/runtime-params" install_path candidate pool_config
  install_path="$(read_parameter "${params}" 'install-dir')"
  php_service="$(read_parameter "${params}" 'service-name')"
  php_socket="$(read_parameter "${params}" 'socket-path')"
  php_run_user="$(read_parameter "${params}" 'run-user')"
  php_run_group="$(read_parameter "${params}" 'run-group')"
  for candidate in "${install_path:+${install_path}/bin/php}" /usr/local/php/bin/php; do
    [[ -n "${candidate}" && -x "${candidate}" ]] && { php_binary="${candidate}"; break; }
  done
  [[ -n "${php_binary}" ]] || die_code PHP_RUNTIME_MISSING "Managed PHP CLI was not found."
  if [[ -z "${php_service}" ]]; then
    for candidate in oneinstack-php-fpm php-fpm; do systemctl list-unit-files "${candidate}.service" --no-legend 2>/dev/null | grep -q . && { php_service="${candidate}"; break; }; done
  fi
  if [[ -z "${php_run_user}" || -z "${php_run_group}" ]]; then
    for pool_config in "${install_path:+${install_path}/etc/php-fpm.d/99-oneinstack.conf}" /usr/local/php/etc/php-fpm.d/99-oneinstack.conf; do
      [[ -n "${pool_config}" && -r "${pool_config}" ]] || continue
      [[ -n "${php_run_user}" ]] || php_run_user="$(sed -nE 's/^[[:space:]]*user[[:space:]]*=[[:space:]]*([^[:space:];]+).*$/\1/p' "${pool_config}" | head -n1)"
      [[ -n "${php_run_group}" ]] || php_run_group="$(sed -nE 's/^[[:space:]]*group[[:space:]]*=[[:space:]]*([^[:space:];]+).*$/\1/p' "${pool_config}" | head -n1)"
      [[ -n "${php_run_user}" && -n "${php_run_group}" ]] && break
    done
  fi
  [[ -n "${php_service}" ]] || die_code PHP_FPM_MISSING "Managed PHP-FPM service identity is missing."
  [[ "${php_run_user}" =~ ^[a-z_][a-z0-9_-]{0,30}$ ]] ||
    die_code PHP_FPM_ACCOUNT_MISSING "Managed PHP-FPM runtime user identity is missing or invalid."
  [[ "${php_run_group}" =~ ^[a-z_][a-z0-9_-]{0,30}$ ]] ||
    die_code PHP_FPM_ACCOUNT_MISSING "Managed PHP-FPM runtime group identity is missing or invalid."
  id "${php_run_user}" >/dev/null 2>&1 ||
    die_code PHP_FPM_ACCOUNT_MISSING "Managed PHP-FPM runtime user ${php_run_user} does not exist."
  getent group "${php_run_group}" >/dev/null 2>&1 ||
    die_code PHP_FPM_ACCOUNT_MISSING "Managed PHP-FPM runtime group ${php_run_group} does not exist."
  systemctl is-active --quiet "${php_service}.service" || die_code PHP_FPM_STOPPED "Managed PHP-FPM service ${php_service}.service is not running."
  if [[ -z "${php_socket}" ]]; then
    for candidate in /dev/shm/php-cgi.sock /run/php/php-fpm.sock /run/php-fpm/www.sock; do [[ -S "${candidate}" ]] && { php_socket="${candidate}"; break; }; done
  fi
  [[ -n "${php_socket}" && -S "${php_socket}" ]] || die_code PHP_FPM_SOCKET_MISSING "Managed PHP-FPM socket is missing."
  php_version="$("${php_binary}" -r 'echo PHP_VERSION;' 2>/dev/null)" || die_code PHP_RUNTIME_INVALID "Cannot read the managed PHP version."
  "${php_binary}" -r 'exit(version_compare(PHP_VERSION,"5.3.0",">=")?0:1);' || die_code PHP_VERSION_UNSUPPORTED "Adminer requires PHP 5.3 or newer."
  validate_cidrs_with_php
  "${php_binary}" -r 'exit(extension_loaded("session")?0:1);' || die_code PHP_SESSION_EXTENSION_MISSING "Managed PHP must provide the session extension."
  if [[ "${default_driver}" == mysql ]]; then
    "${php_binary}" -r 'exit(extension_loaded("mysqli")||extension_loaded("pdo_mysql")?0:1);' ||
      die_code PHP_DRIVER_EXTENSION_MISSING "The mysql default driver requires mysqli or pdo_mysql."
  else
    "${php_binary}" -r 'exit(extension_loaded("pgsql")||extension_loaded("pdo_pgsql")?0:1);' ||
      die_code PHP_DRIVER_EXTENSION_MISSING "The pgsql default driver requires pgsql or pdo_pgsql."
  fi
}
web_component_details() {
  case "$1" in
    nginx) printf '%s\t%s\t%s\t%s\n' oneinstack-nginx /usr/local/nginx/sbin/nginx /usr/local/nginx/conf/nginx.conf /usr/local/nginx/conf/conf.d/default.conf ;;
    openresty) printf '%s\t%s\t%s\t%s\n' oneinstack-openresty /usr/local/openresty/nginx/sbin/nginx /usr/local/openresty/nginx/conf/nginx.conf /usr/local/openresty/nginx/conf/conf.d/default.conf ;;
    tengine) printf '%s\t%s\t%s\t%s\n' oneinstack-tengine /usr/local/tengine/sbin/tengine /usr/local/tengine/conf/tengine.conf /usr/local/tengine/conf/conf.d/default.conf ;;
    apache) printf '%s\t%s\t%s\t%s\n' oneinstack-httpd /usr/local/apache/bin/httpd /usr/local/apache/conf/httpd.conf /usr/local/apache/conf/oneinstack/oneinstack.conf ;;
    caddy) printf '%s\t%s\t%s\t%s\n' oneinstack-caddy /usr/local/caddy/bin/caddy /usr/local/caddy/conf/Caddyfile /usr/local/caddy/conf/oneinstack-default.caddy ;;
  esac
}
resolve_web_runtime_paths() {
  # Tengine packages before the dedicated runtime layout used nginx-compatible
  # binary and configuration names. Prefer the current managed paths, while
  # retaining read-only compatibility with an already-managed legacy runtime.
  if [[ "${web_component}" == tengine ]]; then
    if [[ ! -x "${web_binary}" && -x /usr/local/tengine/sbin/nginx ]]; then
      web_binary=/usr/local/tengine/sbin/nginx
    fi
    if [[ ! -r "${web_config}" && -r /usr/local/tengine/conf/nginx.conf ]]; then
      web_config=/usr/local/tengine/conf/nginx.conf
    fi
  fi
  [[ -r "${web_site_config}" ]] || web_site_config=""
}
configured_document_root() {
  local configured="$1"
  configured="${configured%/}"
  if [[ "${configured}" == */default ]]; then
    printf '%s\n' "${configured}"
  else
    printf '%s/default\n' "${configured}"
  fi
}
detect_document_root() {
  local params="${state_root}/${web_component}/install-parameters" configured="" detected=""
  configured="$(read_parameter "${params}" 'WEB_ROOT')"
  [[ -n "${configured}" ]] || configured="$(read_parameter "${params}" 'web-root')"
  if [[ "${web_component}" == apache ]]; then
    # The managed Apache vhost is included from oneinstack/oneinstack.conf. The
    # first DocumentRoot in httpd.conf is Apache's upstream default and is not
    # necessarily the root serving the managed 127.0.0.1 virtual host.
    if [[ -n "${web_site_config}" ]]; then
      detected="$(sed -nE 's|^[[:space:]]*DocumentRoot[[:space:]]+"?([^"[:space:]]+)"?.*$|\1|p' "${web_site_config}" 2>/dev/null | head -n1)"
    fi
  elif [[ "${web_component}" == caddy ]]; then
    if [[ -n "${web_site_config}" ]]; then
      detected="$(sed -nE 's|^[[:space:]]*root[[:space:]]+\*?[[:space:]]+([^[:space:]{}]+).*$|\1|p' "${web_site_config}" 2>/dev/null | head -n1)"
    fi
  else
    if [[ -n "${web_site_config}" ]]; then
      detected="$(sed -nE 's|^[[:space:]]*root[[:space:]]+([^;[:space:]]+);.*$|\1|p' "${web_site_config}" 2>/dev/null | head -n1)"
    fi
  fi
  if [[ -z "${detected}" && -n "${configured}" ]]; then
    detected="$(configured_document_root "${configured}")"
  fi
  if [[ -z "${detected}" ]]; then
    case "${web_component}" in
      apache) detected="$(sed -nE 's|^[[:space:]]*DocumentRoot[[:space:]]+"?([^"[:space:]]+)"?.*$|\1|p' "${web_config}" 2>/dev/null | head -n1)" ;;
      caddy) detected="$(sed -nE 's|^[[:space:]]*root[[:space:]]+\*?[[:space:]]+([^[:space:]{}]+).*$|\1|p' "${web_config}" 2>/dev/null | head -n1)" ;;
      *) detected="$(sed -nE 's|^[[:space:]]*root[[:space:]]+([^;[:space:]]+);.*$|\1|p' "${web_config}" 2>/dev/null | head -n1)" ;;
    esac
  fi
  [[ -n "${detected}" ]] || die_code WEB_SERVER_DOCUMENT_ROOT_MISSING "Cannot determine the managed ${web_component} document root."
  detected="$(realpath -m -- "${detected}")"
  validate_path "${detected}" WEB_SERVER_DOCUMENT_ROOT
  [[ -d "${detected}" ]] || die_code WEB_SERVER_DOCUMENT_ROOT_MISSING "Managed Web Server document root does not exist: ${detected}."
  web_document_root="${detected}"
}
configured_web_port() {
  local params="${state_root}/${web_component}/install-parameters" configured=""
  case "${web_component}" in
    nginx) configured="$(read_parameter "${params}" 'NGINX_PORT')" ;;
    openresty) configured="$(read_parameter "${params}" 'OPENRESTY_PORT')" ;;
    tengine) configured="$(read_parameter "${params}" 'TENGINE_PORT')" ;;
    apache)
      configured="$(read_parameter "${params}" 'PORT')"
      [[ -n "${configured}" ]] || configured="$(read_parameter "${params}" 'APACHE_PORT')"
      ;;
    caddy)
      configured="$(read_parameter "${params}" 'port')"
      [[ -n "${configured}" ]] || configured="$(read_parameter "${params}" 'CADDY_PORT')"
      ;;
  esac
  [[ "${configured}" =~ ^[0-9]+$ && "${configured}" -ge 1 && "${configured}" -le 65535 ]] || return 0
  printf '%s\n' "${configured}"
}
detect_web_endpoint() {
  local listen="" host="" configured_port=""
  configured_port="$(configured_web_port)"
  case "${web_component}" in
    apache)
      if [[ -n "${web_site_config}" ]]; then
        listen="$(sed -nE 's|^[[:space:]]*Listen[[:space:]]+([^[:space:]]+).*$|\1|p' "${web_site_config}" 2>/dev/null | head -n1)"
        host="$(sed -nE 's|^[[:space:]]*ServerName[[:space:]]+([^[:space:]]+).*$|\1|p' "${web_site_config}" 2>/dev/null | head -n1)"
      fi
      [[ -n "${listen}" ]] || listen="${configured_port}"
      [[ -n "${listen}" ]] || listen="$(sed -nE 's|^[[:space:]]*Listen[[:space:]]+([^[:space:]]+).*$|\1|p' "${web_config}" 2>/dev/null | head -n1)"
      [[ -n "${host}" ]] || host="$(sed -nE 's|^[[:space:]]*ServerName[[:space:]]+([^[:space:]]+).*$|\1|p' "${web_config}" 2>/dev/null | head -n1)"
      ;;
    caddy)
      if [[ -n "${web_site_config}" ]]; then
        listen="$(sed -nE 's|^[[:space:]]*(http://)?:([0-9]+)[[:space:]]*\{?.*$|\2|p' "${web_site_config}" 2>/dev/null | head -n1)"
      fi
      [[ -n "${listen}" ]] || listen="${configured_port}"
      host="127.0.0.1"
      ;;
    *)
      if [[ -n "${web_site_config}" ]]; then
        listen="$(sed -nE 's|^[[:space:]]*listen[[:space:]]+([^;[:space:]]+).*$|\1|p' "${web_site_config}" 2>/dev/null | head -n1)"
        host="$(sed -nE 's|^[[:space:]]*server_name[[:space:]]+([^;[:space:]]+).*$|\1|p' "${web_site_config}" 2>/dev/null | head -n1)"
      fi
      [[ -n "${listen}" ]] || listen="${configured_port}"
      [[ -n "${listen}" ]] || listen="$(sed -nE 's|^[[:space:]]*listen[[:space:]]+([^;[:space:]]+).*$|\1|p' "${web_config}" 2>/dev/null | head -n1)"
      [[ -n "${host}" ]] || host="$(sed -nE 's|^[[:space:]]*server_name[[:space:]]+([^;[:space:]]+).*$|\1|p' "${web_config}" 2>/dev/null | head -n1)"
      ;;
  esac
  listen="${listen##*:}"
  [[ "${listen}" =~ ^[0-9]+$ && "${listen}" -ge 1 && "${listen}" -le 65535 ]] ||
    die_code WEB_SERVER_ENDPOINT_MISSING "Cannot determine the managed ${web_component} HTTP listener port from its active configuration or saved parameters."
  case "${host}" in ""|_|localhost|\$host) host=127.0.0.1 ;; esac
  if [[ "${host}" =~ ^([^:]+):[0-9]+$ ]]; then host="${BASH_REMATCH[1]}"; fi
  web_port="${listen}"
  web_host="${host}"
}
detect_managed_web_server() {
  local component managed_list active_list inactive_list
  local -a managed_components=() active_components=() inactive_components=()
  for component in nginx openresty tengine apache caddy; do
    managed_component_present "${component}" || continue
    managed_components+=("${component}")
    IFS=$'\t' read -r web_service web_binary web_config web_site_config < <(web_component_details "${component}")
    if systemctl is-active --quiet "${web_service}.service"; then
      active_components+=("${component}")
    else
      inactive_components+=("${component}")
    fi
  done
  managed_list="$(IFS=,; printf '%s' "${managed_components[*]-}")"
  active_list="$(IFS=,; printf '%s' "${active_components[*]-}")"
  inactive_list="$(IFS=,; printf '%s' "${inactive_components[*]-}")"
  [[ "${#managed_components[@]}" -gt 0 ]] || die_code WEB_SERVER_MANAGED_REQUIRED "A OneinStack-managed Nginx, OpenResty, Tengine, Apache, or Caddy installation is required."
  [[ "${#active_components[@]}" -gt 0 ]] || die_code WEB_SERVER_STOPPED "No managed Web Server is running; managed candidates: ${managed_list}."
  [[ "${#active_components[@]}" -eq 1 ]] || die_code WEB_SERVER_AMBIGUOUS "Multiple managed Web Servers are running: ${active_list}. Stop all but one before installing Adminer."
  web_component="${active_components[0]}"
  if [[ "${#inactive_components[@]}" -gt 0 ]]; then
    log "WEB_SERVER_INACTIVE_MANAGED_IGNORED: selected active ${web_component}; inactive managed state preserved: ${inactive_list}."
  fi
  IFS=$'\t' read -r web_service web_binary web_config web_site_config < <(web_component_details "${web_component}")
  resolve_web_runtime_paths
  [[ -x "${web_binary}" && -r "${web_config}" ]] || die_code WEB_SERVER_RUNTIME_MISSING "Managed ${web_component} binary or configuration is missing."
  detect_document_root
  detect_web_endpoint
  route_path="${web_document_root}/${public_path#/}"
  route_path="${route_path%/}"
}
run_as_php_fpm() {
  if command -v runuser >/dev/null 2>&1; then
    runuser -u "${php_run_user}" -- "$@"
  else
    su -s /bin/sh -c "$(printf '%q ' "$@")" "${php_run_user}"
  fi
}
record_managed_php_path_acl() {
  local acl_path="$1" record
  record="${php_run_user}"$'\t'"${acl_path}"
  install -d -m 0750 -- "${state_dir}"
  if [[ ! -f "${managed_php_path_acl_file}" ]] ||
    ! grep -Fqx -- "${record}" "${managed_php_path_acl_file}"; then
    printf '%s\n' "${record}" >>"${managed_php_path_acl_file}"
    chmod 0600 "${managed_php_path_acl_file}"
  fi
  if [[ -n "${php_path_acl_transaction_file}" ]] &&
    { [[ ! -f "${php_path_acl_transaction_file}" ]] ||
      ! grep -Fqx -- "${record}" "${php_path_acl_transaction_file}"; }; then
    printf '%s\n' "${record}" >>"${php_path_acl_transaction_file}"
    chmod 0600 "${php_path_acl_transaction_file}"
  fi
}
ensure_php_fpm_path_traversal() {
  local managed_path="$1" current_path index current_acl access_mask existing_user_permissions
  local -a path_chain=()
  current_path="${managed_path%/}"
  while [[ "${current_path}" != / ]]; do
    path_chain+=("${current_path}")
    current_path="$(dirname -- "${current_path}")"
  done
  for ((index=${#path_chain[@]}; index > 0; index--)); do
    current_path="${path_chain[index - 1]}"
    [[ -d "${current_path}" && ! -L "${current_path}" ]] ||
      die_code PHP_FPM_PATH_PERMISSION_DENIED "PHP-FPM path contains a missing or symbolic-link directory: ${current_path}."
    if run_as_php_fpm test -x "${current_path}"; then
      continue
    fi
    if ! command -v getfacl >/dev/null 2>&1 || ! command -v setfacl >/dev/null 2>&1; then
      if [[ "${install_mode}" == offline ]]; then
        die_code OFFLINE_BUNDLE_DEPENDENCY_MISSING "PHP-FPM user ${php_run_user} cannot traverse ${current_path}, and ACL utilities are unavailable in the offline runtime."
      fi
      die_code ADMINER_ACL_TOOL_MISSING "PHP-FPM user ${php_run_user} cannot traverse ${current_path}, and ACL utilities are unavailable."
    fi
    current_acl="$(getfacl -cp -- "${current_path}")" ||
      die_code PHP_FPM_PATH_PERMISSION_DENIED "Cannot inspect the ACL for ${current_path}."
    existing_user_permissions="$(awk -F: -v user="${php_run_user}" '$1 == "user" && $2 == user {print $3; exit}' <<<"${current_acl}")"
    [[ -z "${existing_user_permissions}" ]] ||
      die_code PHP_FPM_PATH_PERMISSION_DENIED "Existing ACL entry for PHP-FPM user ${php_run_user} on ${current_path} does not permit traversal and was preserved."
    access_mask="$(awk -F: '$1 == "mask" && $2 == "" {print $3; exit}' <<<"${current_acl}")"
    if [[ -n "${access_mask}" ]]; then
      [[ "${access_mask}" == *x* ]] ||
        die_code PHP_FPM_PATH_PERMISSION_DENIED "Existing ACL mask on ${current_path} denies traversal; refusing to broaden unrelated ACL access."
      setfacl --no-mask -m "u:${php_run_user}:--x" -- "${current_path}" ||
        die_code PHP_FPM_PATH_PERMISSION_DENIED "Cannot grant PHP-FPM user ${php_run_user} traverse access to ${current_path}."
    else
      setfacl -m "u:${php_run_user}:--x" -- "${current_path}" ||
        die_code PHP_FPM_PATH_PERMISSION_DENIED "Cannot grant PHP-FPM user ${php_run_user} traverse access to ${current_path}."
    fi
    record_managed_php_path_acl "${current_path}"
    run_as_php_fpm test -x "${current_path}" ||
      die_code PHP_FPM_PATH_PERMISSION_DENIED "PHP-FPM user ${php_run_user} still cannot traverse ${current_path} after applying the managed ACL."
  done
}
ensure_php_fpm_route_access() {
  ensure_php_fpm_path_traversal "${web_document_root}"
  ensure_php_fpm_path_traversal "${install_dir}"
  run_as_php_fpm test -r "${route_path}/index.php" ||
    die_code PHP_FPM_PATH_PERMISSION_DENIED "PHP-FPM user ${php_run_user} cannot read the Adminer entrypoint at ${route_path}/index.php."
}
verify_php_fpm_route_access() {
  if run_as_php_fpm test -x "${web_document_root}" &&
    run_as_php_fpm test -x "${install_dir}" &&
    run_as_php_fpm test -r "${route_path}/index.php"; then
    return 0
  fi
  die_code PHP_FPM_PATH_PERMISSION_DENIED "PHP-FPM user ${php_run_user} cannot traverse the managed roots or read the Adminer entrypoint."
}
remove_managed_php_path_acl_entries() {
  local records_file="$1" acl_user acl_path current_permissions
  [[ -f "${records_file}" ]] || return 0
  if ! command -v getfacl >/dev/null 2>&1 || ! command -v setfacl >/dev/null 2>&1; then
    log "ADMINER_ACL_RESTORE_SKIPPED: ACL utilities are unavailable; managed PHP-FPM path ACL entries were preserved."
    return 0
  fi
  while IFS=$'\t' read -r acl_user acl_path; do
    [[ "${acl_user}" =~ ^[a-z_][a-z0-9_-]{0,30}$ && "${acl_path}" == /* && "${acl_path}" != / && -d "${acl_path}" ]] || continue
    current_permissions="$(getfacl -cp -- "${acl_path}" 2>/dev/null | awk -F: -v user="${acl_user}" '$1 == "user" && $2 == user {print $3; exit}' || true)"
    if [[ "${current_permissions}" == --x ]]; then
      setfacl -x "u:${acl_user}" -- "${acl_path}" ||
        log "ADMINER_ACL_RESTORE_FAILED: unable to remove the managed ACL entry for ${acl_user} from ${acl_path}."
    elif [[ -n "${current_permissions}" ]]; then
      log "ADMINER_ACL_CHANGED_EXTERNALLY: ACL entry for ${acl_user} on ${acl_path} was changed externally and was preserved."
    fi
  done <"${records_file}"
}
restore_php_path_acl_transaction() {
  local records_file="$1" temporary record
  [[ -f "${records_file}" ]] || return 0
  remove_managed_php_path_acl_entries "${records_file}"
  if [[ -f "${managed_php_path_acl_file}" ]]; then
    temporary="$(mktemp "${state_dir}/.managed-php-path-acl.XXXXXX")"
    while IFS= read -r record; do
      grep -Fqx -- "${record}" "${records_file}" || printf '%s\n' "${record}" >>"${temporary}"
    done <"${managed_php_path_acl_file}"
    if [[ -s "${temporary}" ]]; then
      chmod 0600 "${temporary}"
      mv -f -- "${temporary}" "${managed_php_path_acl_file}"
    else
      rm -f -- "${temporary}" "${managed_php_path_acl_file}"
    fi
  fi
  rm -f -- "${records_file}"
}
check_unmanaged_web_servers() {
  local unit
  for unit in nginx openresty tengine apache apache2 httpd caddy; do
    systemctl is-active --quiet "${unit}.service" 2>/dev/null || continue
    die_code WEB_SERVER_UNMANAGED "An unmanaged Web Server service is active: ${unit}.service."
  done
}
check_route_conflict() {
  [[ ! -e "${route_path}" && ! -L "${route_path}" ]] && return 0
  managed_public_route "${route_path}" && return 0
  if safe_empty_route_residue "${route_path}"; then
    log "Detected an empty root-owned non-mount Adminer route residue at ${route_path}; it will be removed only after the new HTTP route passes verification and restored on failure."
    return 0
  fi
  die_code ADMINER_PUBLIC_PATH_CONFLICT "The public path ${public_path} conflicts with an existing unowned route at ${route_path}."
}
safe_empty_route_residue() {
  local path="$1" first_entry=""
  [[ -d "${path}" && ! -L "${path}" ]] || return 1
  command -v mountpoint >/dev/null 2>&1 || return 1
  mountpoint -q -- "${path}" && return 1
  [[ "$(stat -c '%U:%G:%a' -- "${path}" 2>/dev/null || true)" == root:root:755 ]] || return 1
  first_entry="$(find "${path}" -mindepth 1 -maxdepth 1 -print -quit 2>/dev/null)" || return 1
  [[ -z "${first_entry}" ]]
}
check_legacy_route_conflict() {
  [[ "${legacy_route_path}" == "${route_path}" || ( ! -e "${legacy_route_path}" && ! -L "${legacy_route_path}" ) ]] && return 0
  if safe_empty_route_residue "${legacy_route_path}"; then
    log "Detected an empty root-owned non-mount legacy Adminer directory at ${legacy_route_path}; it will be removed only after the new HTTP route passes verification and restored on failure."
    return 0
  fi
  [[ -f "${state_dir}/installed" || -f "${installed_state}" ]] ||
    die_code ADMINER_PUBLIC_PATH_CONFLICT "The legacy Adminer path exists but is not owned by OneinStack: ${legacy_route_path}."
}
web_server_config_test() {
  case "${web_component}" in
    nginx) "${web_binary}" -t -c "${web_config}" >/dev/null ;;
    tengine) "${web_binary}" -t -p /usr/local/tengine/ -c "${web_config}" >/dev/null ;;
    openresty) "${web_binary}" -t -p /usr/local/openresty/nginx -c conf/nginx.conf >/dev/null ;;
    apache) "${web_binary}" -t -f "${web_config}" >/dev/null ;;
    caddy) "${web_binary}" validate --config "${web_config}" >/dev/null ;;
  esac
}
validate_runtime_prerequisites() {
  detect_managed_php
  detect_managed_web_server
  check_unmanaged_web_servers
  if [[ -e "${install_dir}" && ! -f "${installed_state}" && ! -f "${state_dir}/installed" ]]; then
    die_code ADMINER_INSTALL_PATH_CONFLICT "The fixed Adminer installation directory exists but is not owned by OneinStack: ${install_dir}."
  fi
  check_route_conflict
  check_legacy_route_conflict
  web_server_config_test || die_code WEB_SERVER_CONFIG_INVALID "Managed ${web_component} configuration validation failed."
}
validate_artifact() {
  local file="$1" actual
  [[ -f "${file}" && ! -L "${file}" ]] || die_code ADMINER_ARTIFACT_MISSING "Adminer artifact is missing."
  actual="$(sha256sum "${file}" | awk '{print $1}')"
  [[ "${actual}" == "${source_sha256}" ]] || die_code ADMINER_ARTIFACT_DIGEST_MISMATCH "Adminer artifact SHA-256 does not match the signed manifest."
  head -c 5 "${file}" | grep -q '^<?php' || die_code ADMINER_ARTIFACT_INVALID "Adminer artifact is not a PHP file."
  "${php_binary}" -l "${file}" >/dev/null || die_code ADMINER_ARTIFACT_INVALID "Adminer artifact failed PHP syntax validation."
}
validate_offline_bundle() {
  local bundle="${offline_package_path}" info="${offline_package_path}/bundle-info" inventory="${offline_package_path}/files.sha256"
  [[ -d "${bundle}" && ! -L "${bundle}" && -f "${bundle}/manifest.yaml" && -f "${info}" && -f "${inventory}" ]] ||
    die_code OFFLINE_BUNDLE_INVALID "Offline Bundle is missing manifest.yaml, bundle-info, or files.sha256."
  if find "${bundle}" -mindepth 1 \( -type l -o \( ! -type f ! -type d \) \) -print -quit | grep -q .; then
    die_code OFFLINE_BUNDLE_INVALID "Offline Bundle contains a symlink or special file."
  fi
  local key value bundle_component="" bundle_package="" bundle_software="" bundle_os="" bundle_os_version="" bundle_arch="" bundle_id=""
  while IFS='=' read -r key value; do
    case "${key}" in
      component) bundle_component="${value}" ;; package-version) bundle_package="${value}" ;; software-version) bundle_software="${value}" ;;
      os-id) bundle_os="${value}" ;; os-version) bundle_os_version="${value}" ;; architecture) bundle_arch="${value}" ;;
      bundle-id) bundle_id="${value}" ;;
    esac
  done <"${info}"
  [[ "${bundle_component}" == "${component_id}" && "${bundle_package}" == "${package_version}" && "${bundle_software}" == "${software_version}" ]] ||
    die_code OFFLINE_BUNDLE_IDENTITY_MISMATCH "Offline Bundle component/package/software identity does not match."
  [[ "${bundle_os}" == "${detected_os_id}" && "${bundle_os_version}" == "${detected_os_version}" && "${bundle_arch}" == "${detected_architecture}" ]] ||
    die_code OFFLINE_BUNDLE_PLATFORM_MISMATCH "Offline Bundle platform does not match this host."
  [[ -n "${bundle_id}" && "${bundle_id}" =~ ^[A-Za-z0-9._-]{1,160}$ ]] ||
    die_code OFFLINE_BUNDLE_IDENTITY_MISMATCH "Offline Bundle ID is missing or invalid."
  local listed actual
  listed="$(awk '{print $2}' "${inventory}" | sed 's|^\*\?||' | LC_ALL=C sort)"
  actual="$(cd "${bundle}" && find . -type f ! -name files.sha256 -print | LC_ALL=C sort)"
  [[ "${listed}" == "${actual}" ]] || die_code OFFLINE_BUNDLE_INVALID "Offline Bundle file inventory is not exact."
  (cd "${bundle}" && sha256sum --check --strict files.sha256 >/dev/null) ||
    die_code OFFLINE_BUNDLE_DIGEST_MISMATCH "Offline Bundle contains a file with an invalid digest."
  if ! run_as_php_fpm test -x "${web_document_root}" &&
    { ! command -v getfacl >/dev/null 2>&1 || ! command -v setfacl >/dev/null 2>&1; }; then
    die_code OFFLINE_BUNDLE_DEPENDENCY_MISSING "PHP-FPM user ${php_run_user} cannot traverse ${web_document_root}, and ACL utilities are unavailable in the offline runtime."
  fi
}
obtain_artifact() {
  local target
  if [[ "${install_mode}" == offline ]]; then
    validate_offline_bundle
    target="${offline_package_path}/artifacts/${detected_architecture}/${artifact_name}"
    validate_artifact "${target}"
    printf '%s\n' "${target}"
    return
  fi
  install_online_dependencies
  require_command curl
  install -d -m 0750 -- "${state_root}/downloads"
  target="${state_root}/downloads/${artifact_name}"
  if [[ ! -f "${target}" ]] || [[ "$(sha256sum "${target}" | awk '{print $1}')" != "${source_sha256}" ]]; then
    rm -f -- "${target}.part"
    curl --proto '=https' --tlsv1.2 --fail --location --retry 3 --connect-timeout 20 --output "${target}.part" "${source_url}" || {
      rm -f -- "${target}.part"
      die_code ADMINER_DOWNLOAD_FAILED "Failed to download the pinned Adminer 6.1.0 artifact."
    }
    mv -f -- "${target}.part" "${target}"
  fi
  validate_artifact "${target}"
  printf '%s\n' "${target}"
}
render_runtime_config() {
  local target="$1"
  cat >"${target}" <<EOF
<?php
return array(
    'publicPath' => '${public_path}',
    'accessPolicy' => '${access_policy}',
    'allowedCidrs' => '${allowed_cidrs}',
    'defaultDriver' => '${default_driver}',
    'defaultServer' => '${default_server}',
);
EOF
  chmod 0644 "${target}"
}
render_plugin_config() {
  local target="$1"
  cat >"${target}" <<'PHP'
<?php
class OneinStackAdminerPlugin extends Adminer\Plugin {
    public function verifyVersion() { return false; }
}
return array(new OneinStackAdminerPlugin());
PHP
  chmod 0644 "${target}"
}
render_entrypoint() {
  local target="$1"
  cat >"${target}" <<'PHP'
<?php
$config = require __DIR__ . '/oneinstack-config.php';
function oneinstack_ip_in_cidr($ip, $cidr) {
    $parts = explode('/', trim($cidr), 2);
    if (count($parts) !== 2) return false;
    $address = @inet_pton($ip);
    $network = @inet_pton($parts[0]);
    if ($address === false || $network === false || strlen($address) !== strlen($network)) return false;
    $bits = (int) $parts[1];
    if ($bits < 0 || $bits > strlen($address) * 8) return false;
    $bytes = (int) floor($bits / 8);
    $remainder = $bits % 8;
    if ($bytes && substr($address, 0, $bytes) !== substr($network, 0, $bytes)) return false;
    if (!$remainder) return true;
    $mask = (0xff << (8 - $remainder)) & 0xff;
    return (ord($address[$bytes]) & $mask) === (ord($network[$bytes]) & $mask);
}
$remote = isset($_SERVER['REMOTE_ADDR']) ? $_SERVER['REMOTE_ADDR'] : '';
$loopback = ($remote === '127.0.0.1' || $remote === '::1');
$allowed = ($config['accessPolicy'] === 'public' || $loopback);
if (!$allowed && $config['accessPolicy'] === 'allowlist') {
    foreach (explode(',', $config['allowedCidrs']) as $cidr) {
        if (oneinstack_ip_in_cidr($remote, $cidr)) { $allowed = true; break; }
    }
}
if (!$allowed) {
    header('HTTP/1.1 403 Forbidden');
    header('Content-Type: text/plain; charset=utf-8');
    echo "Adminer access denied by OneinStack policy.\n";
    exit;
}
$driverKey = ($config['defaultDriver'] === 'pgsql') ? 'pgsql' : 'server';
if (!isset($_GET[$driverKey]) && !isset($_POST['auth'])) $_GET[$driverKey] = $config['defaultServer'];
chdir(__DIR__);
require __DIR__ . '/adminer-6.1.0.php';
PHP
  chmod 0644 "${target}"
}
copy_preserved_files() {
  local source="$1" target="$2" item base
  [[ -d "${source}" ]] || return 0
  while IFS= read -r -d '' item; do
    base="${item##*/}"
    case "${base}" in .oneinstack-adminer-route|index.php|adminer.php|adminer-[0-9]*.php|adminer-plugins.php|oneinstack-config.php) continue ;; esac
    cp -a -- "${item}" "${target}/"
  done < <(find "${source}" -mindepth 1 -maxdepth 1 -print0)
}
managed_public_route() {
  local path="$1" marker index
  marker="${path}/.oneinstack-adminer-route"
  index="${path}/index.php"
  if [[ -L "${path}" ]]; then
    [[ "$(realpath -m -- "${path}")" == "${install_dir}" ]]
    return
  fi
  [[ -d "${path}" && ! -L "${path}" && -f "${marker}" && ! -L "${marker}" && -f "${index}" && ! -L "${index}" ]] || return 1
  [[ "$(stat -c '%U:%G:%a' -- "${path}" 2>/dev/null || true)" == root:root:755 ]] || return 1
  [[ "$(stat -c '%U:%G:%a' -- "${marker}" 2>/dev/null || true)" == root:root:644 ]] || return 1
  [[ "$(stat -c '%U:%G:%a' -- "${index}" 2>/dev/null || true)" == root:root:644 ]] || return 1
  [[ "$(cat -- "${marker}" 2>/dev/null || true)" == "${install_dir}" ]] || return 1
  [[ "$(cat -- "${index}" 2>/dev/null || true)" == $'<?php\nrequire '\''/usr/local/adminer/index.php'\'';' ]]
}
render_public_bridge() {
  local target="$1"
  install -d -m 0755 -- "${target}"
  cat >"${target}/index.php" <<'PHP'
<?php
require '/usr/local/adminer/index.php';
PHP
  chmod 0644 "${target}/index.php"
  printf '%s\n' "${install_dir}" >"${target}/.oneinstack-adminer-route"
  chmod 0644 "${target}/.oneinstack-adminer-route"
}
create_public_route() {
  local temporary_route
  if [[ "${web_component}" == caddy ]]; then
    temporary_route="${web_document_root}/.adminer-route-${BASHPID}"
    rm -rf -- "${temporary_route}"
    render_public_bridge "${temporary_route}"
    mv -- "${temporary_route}" "${route_path}"
  else
    temporary_route="${web_document_root}/.adminer-link-${BASHPID}"
    ln -s -- "${install_dir}" "${temporary_route}"
    mv -Tf -- "${temporary_route}" "${route_path}"
  fi
}
remove_managed_public_route() {
  local path="$1"
  managed_public_route "${path}" || return 1
  if [[ -L "${path}" ]]; then
    rm -f -- "${path}"
  else
    rm -rf -- "${path}"
  fi
}
prepare_install_transaction() {
  rm -rf -- "${rollback_dir}" "${install_candidate}" "${install_backup}"
  install -d -m 0700 -- "${rollback_dir}"
  php_path_acl_transaction_file="${rollback_dir}/managed-php-path-acl.added"
  printf '%s\n' "${route_path}" >"${rollback_dir}/route-path"
  [[ ! -f "${runtime_state}" ]] || cp -a -- "${runtime_state}" "${rollback_dir}/runtime-config"
  [[ ! -f "${installed_state}" ]] || cp -a -- "${installed_state}" "${rollback_dir}/installed.json"
  if [[ -d "${install_dir}" && ! -L "${install_dir}" ]]; then mv -- "${install_dir}" "${install_backup}"; fi
  if [[ -L "${route_path}" ]]; then
    readlink "${route_path}" >"${rollback_dir}/route-target"
    rm -f -- "${route_path}"
  elif [[ -d "${route_path}" ]]; then
    printf '%s\n' "${route_path}.oneinstack-adminer-rollback" >"${rollback_dir}/legacy-route-backup"
    rm -rf -- "${route_path}.oneinstack-adminer-rollback"
    mv -- "${route_path}" "${route_path}.oneinstack-adminer-rollback"
  fi
  if [[ "${legacy_route_path}" != "${route_path}" && ( -e "${legacy_route_path}" || -L "${legacy_route_path}" ) ]]; then
    printf '%s\n' "${legacy_route_path}" >"${rollback_dir}/legacy-route-path"
    if [[ -L "${legacy_route_path}" ]]; then
      readlink "${legacy_route_path}" >"${rollback_dir}/legacy-route-target"
      rm -f -- "${legacy_route_path}"
    elif [[ -d "${legacy_route_path}" ]]; then
      printf '%s\n' "${legacy_route_path}.oneinstack-adminer-rollback" >"${rollback_dir}/legacy-route-extra-backup"
      rm -rf -- "${legacy_route_path}.oneinstack-adminer-rollback"
      mv -- "${legacy_route_path}" "${legacy_route_path}.oneinstack-adminer-rollback"
    fi
  fi
}
restore_transaction() {
  local previous_route_target="" legacy_backup="" legacy_path=""
  if [[ -f "${rollback_dir}/route-path" ]]; then route_path="$(cat "${rollback_dir}/route-path")"; fi
  remove_managed_public_route "${route_path}" 2>/dev/null || true
  rm -rf -- "${install_dir}" "${install_candidate}" 2>/dev/null || true
  [[ ! -d "${install_backup}" ]] || mv -- "${install_backup}" "${install_dir}"
  if [[ -f "${rollback_dir}/route-target" ]]; then
    previous_route_target="$(cat "${rollback_dir}/route-target")"
    ln -s -- "${previous_route_target}" "${route_path}"
  elif [[ -f "${rollback_dir}/legacy-route-backup" ]]; then
    legacy_backup="$(cat "${rollback_dir}/legacy-route-backup")"
    [[ ! -d "${legacy_backup}" ]] || mv -- "${legacy_backup}" "${route_path}"
  fi
  if [[ -f "${rollback_dir}/legacy-route-path" ]]; then
    legacy_path="$(cat "${rollback_dir}/legacy-route-path")"
    if [[ -f "${rollback_dir}/legacy-route-target" ]]; then
      ln -s -- "$(cat "${rollback_dir}/legacy-route-target")" "${legacy_path}"
    elif [[ -f "${rollback_dir}/legacy-route-extra-backup" ]]; then
      legacy_backup="$(cat "${rollback_dir}/legacy-route-extra-backup")"
      [[ ! -d "${legacy_backup}" ]] || mv -- "${legacy_backup}" "${legacy_path}"
    fi
  fi
  [[ ! -f "${rollback_dir}/runtime-config" ]] || cp -a -- "${rollback_dir}/runtime-config" "${runtime_state}"
  [[ ! -f "${rollback_dir}/installed.json" ]] || cp -a -- "${rollback_dir}/installed.json" "${installed_state}"
  restore_php_path_acl_transaction "${rollback_dir}/managed-php-path-acl.added"
}
create_managed_installation() {
  local artifact="$1"
  install -d -m 0755 -- "${install_candidate}"
  copy_preserved_files "${install_backup}" "${install_candidate}"
  if [[ -f "${rollback_dir}/legacy-route-backup" ]]; then copy_preserved_files "$(cat "${rollback_dir}/legacy-route-backup")" "${install_candidate}"; fi
  if [[ -f "${rollback_dir}/legacy-route-extra-backup" ]]; then copy_preserved_files "$(cat "${rollback_dir}/legacy-route-extra-backup")" "${install_candidate}"; fi
  install -m 0644 -- "${artifact}" "${install_candidate}/${artifact_name}"
  render_runtime_config "${install_candidate}/oneinstack-config.php"
  render_plugin_config "${install_candidate}/adminer-plugins.php"
  render_entrypoint "${install_candidate}/index.php"
  "${php_binary}" -l "${install_candidate}/index.php" >/dev/null
  "${php_binary}" -l "${install_candidate}/adminer-plugins.php" >/dev/null
  "${php_binary}" -l "${install_candidate}/oneinstack-config.php" >/dev/null
  mv -- "${install_candidate}" "${install_dir}"
  create_public_route
}
http_probe_path() {
  local scheme="$1" path="$2"
  # shellcheck disable=SC2016
  "${php_binary}" -r '
    $scheme=$argv[1]; $port=(int)$argv[2]; $host=$argv[3]; $path=$argv[4];
    $transport=$scheme === "https" ? "tls" : "tcp";
    $context=stream_context_create(array("ssl"=>array("verify_peer"=>false,"verify_peer_name"=>false)));
    $socket=@stream_socket_client($transport."://127.0.0.1:".$port,$errno,$error,8,STREAM_CLIENT_CONNECT,$context);
    if(!$socket){echo "connect_failed errno=".(int)$errno; exit(2);}
    fwrite($socket,"GET ".$path." HTTP/1.1\r\nHost: ".$host."\r\nConnection: close\r\n\r\n");
    stream_set_timeout($socket,10); $response=stream_get_contents($socket); fclose($socket);
    if(!preg_match("~^HTTP/[0-9.]+[[:space:]]+([0-9]{3})(?:[[:space:]]|\\r)~",$response,$match)){
      echo "invalid_http_response"; exit(3);
    }
    if($match[1] !== "200"){echo "status=".$match[1]; exit(3);}
    if(stripos($response,"Adminer") === false){echo "status=200 content=unexpected"; exit(4);}
    echo "status=200 content=adminer";
  ' "${scheme}" "${web_port}" "${web_host}" "${path}"
}
http_probe() {
  local scheme=http probe_result probe_status direct_result direct_status direct_path
  [[ "${web_port}" == 443 ]] && scheme=https
  if probe_result="$(http_probe_path "${scheme}" "${public_path}")"; then
    return 0
  else
    probe_status=$?
  fi
  direct_path="${public_path}index.php"
  if direct_result="$(http_probe_path "${scheme}" "${direct_path}")"; then
    direct_status=0
  else
    direct_status=$?
  fi
  probe_result="${probe_result//$'\n'/;}"
  direct_result="${direct_result//$'\n'/;}"
  die_code ADMINER_HTTP_PROBE_FAILED \
    "Local HTTP probe failed (${probe_result:-probe_exit=${probe_status}}); directIndex=${direct_result:-probe_exit=${direct_status}}, packageVersion=${package_version}, webServer=${web_component}, config=${web_config}, documentRoot=${web_document_root}, route=${route_path}, endpoint=${scheme}://127.0.0.1:${web_port}${public_path}, host=${web_host}."
}
verify_managed_installation() {
  validate_artifact "${install_dir}/${artifact_name}"
  managed_public_route "${route_path}" ||
    die_code ADMINER_ROUTE_INVALID "Managed public route is missing or points to the wrong directory."
  verify_php_fpm_route_access
  web_server_config_test || die_code WEB_SERVER_CONFIG_INVALID "Managed ${web_component} configuration validation failed."
  http_probe
}
config_revision() {
  printf 'publicPath=%s\naccessPolicy=%s\nallowedCidrs=%s\ndefaultDriver=%s\ndefaultServer=%s\n' \
    "${public_path}" "${access_policy}" "${allowed_cidrs}" "${default_driver}" "${default_server}" | sha256sum | awk '{print $1}'
}
write_runtime_state() {
  local candidate
  install -d -m 0750 -- "${state_dir}"
  candidate="$(mktemp "${state_dir}/.runtime-config.XXXXXX")"
  {
    printf 'public-path=%s\naccess-policy=%s\nallowed-cidrs=%s\ndefault-driver=%s\ndefault-server=%s\n' "${public_path}" "${access_policy}" "${allowed_cidrs}" "${default_driver}" "${default_server}"
    printf 'artifact-sha256=%s\ninstall-dir=%s\nweb-server=%s\nweb-service=%s\nweb-config=%s\ndocument-root=%s\nweb-port=%s\nweb-host=%s\n' \
      "${source_sha256}" "${install_dir}" "${web_component}" "${web_service}" "${web_config}" "${web_document_root}" "${web_port}" "${web_host}"
    printf 'php-version=%s\nphp-service=%s\nphp-socket=%s\n' "${php_version}" "${php_service}" "${php_socket}"
  } >"${candidate}"
  chmod 0600 "${candidate}"
  mv -f -- "${candidate}" "${runtime_state}"
}
json_escape() { local value="$1"; value="${value//\\/\\\\}"; value="${value//\"/\\\"}"; value="${value//$'\n'/\\n}"; printf '%s' "${value}"; }
write_installed_state() {
  local candidate
  candidate="$(mktemp "${state_dir}/.installed.XXXXXX")"
  printf '{"component":"adminer","packageVersion":"%s","softwareVersion":"%s","artifactSha256":"%s","installMode":"%s","osId":"%s","osVersion":"%s","architecture":"%s","publicPath":"%s","webServer":"%s","phpVersion":"%s","revision":"%s"}\n' \
    "${package_version}" "${software_version}" "${source_sha256}" "${install_mode}" "${detected_os_id}" "${detected_os_version}" "${detected_architecture}" \
    "$(json_escape "${public_path}")" "$(json_escape "${web_component}")" "$(json_escape "${php_version}")" "$(config_revision)" >"${candidate}"
  chmod 0600 "${candidate}"
  mv -f -- "${candidate}" "${installed_state}"
  : >"${state_dir}/installed"
  printf '%s\n' "${software_version}" >"${state_dir}/version"
  printf 'managed\n' >"${state_dir}/ownership"
}
load_runtime_state() {
  [[ -r "${runtime_state}" ]] || die_code ADMINER_STATE_MISSING "Managed Adminer runtime state is missing."
  public_path="$(read_parameter "${runtime_state}" 'public-path')"
  access_policy="$(read_parameter "${runtime_state}" 'access-policy')"
  allowed_cidrs="$(read_parameter "${runtime_state}" 'allowed-cidrs')"
  default_driver="$(read_parameter "${runtime_state}" 'default-driver')"
  default_server="$(read_parameter "${runtime_state}" 'default-server')"
}
load_saved_install_configuration() {
  [[ -r "${runtime_state}" ]] || return 0
  local requested_public_path="${public_path}" requested_access_policy="${access_policy}"
  local requested_allowed_cidrs="${allowed_cidrs}" requested_default_driver="${default_driver}" requested_default_server="${default_server}"
  load_runtime_state
  [[ "${ONEINSTACK_PARAMETER_ADMINER_PUBLIC_PATH_EXPLICIT:-false}" != true ]] || public_path="${requested_public_path}"
  [[ "${ONEINSTACK_PARAMETER_ADMINER_ACCESS_POLICY_EXPLICIT:-false}" != true ]] || access_policy="${requested_access_policy}"
  [[ "${ONEINSTACK_PARAMETER_ADMINER_ALLOWED_CIDRS_EXPLICIT:-false}" != true ]] || allowed_cidrs="${requested_allowed_cidrs}"
  [[ "${ONEINSTACK_PARAMETER_ADMINER_DEFAULT_DRIVER_EXPLICIT:-false}" != true ]] || default_driver="${requested_default_driver}"
  [[ "${ONEINSTACK_PARAMETER_ADMINER_DEFAULT_SERVER_EXPLICIT:-false}" != true ]] || default_server="${requested_default_server}"
}
cleanup_transaction() {
  local legacy_backup=""
  rm -rf -- "${install_backup}"
  if [[ -f "${rollback_dir}/legacy-route-backup" ]]; then
    legacy_backup="$(cat "${rollback_dir}/legacy-route-backup")"
    rm -rf -- "${legacy_backup}"
  fi
  if [[ -f "${rollback_dir}/legacy-route-extra-backup" ]]; then
    legacy_backup="$(cat "${rollback_dir}/legacy-route-extra-backup")"
    rm -rf -- "${legacy_backup}"
  fi
  rm -rf -- "${rollback_dir}"
}
