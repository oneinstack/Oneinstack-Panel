#!/usr/bin/env bash
set -Eeuo pipefail
umask 027

component_id="phpmyadmin"
component_name="phpMyAdmin"
package_version="1.0.16"
component_kind="phpmyadmin"
software_version="${SOFTWARE_VERSION:-}"
install_dir="/data/wwwroot/phpMyAdmin"
legacy_install_dirs=(
  "/data/wwwroot/default/phpMyAdmin"
  "/data/wwwroot/default/phpmyadmin"
)
runtime_install_dir() {
  local candidate
  for candidate in "${install_dir}" "${legacy_install_dirs[@]}"; do
    [[ -e "${candidate}" ]] || continue
    printf '%s\n' "${candidate}"
    return 0
  done
  return 1
}
data_dir=""
service_name=""
systemctl_property_value() {
  local unit="$1" property="$2" output
  [[ -n "${unit}" && -n "${property}" ]] || return 1
  output="$(systemctl show "${unit}" -p "${property}" 2>/dev/null)" || return 1
  case "${output}" in
    "${property}"=*) printf '%s\n' "${output#*=}" ;;
    *) return 1 ;;
  esac
}


state_root="${ONEINSTACK_COMPONENT_STATE:-/var/lib/oneinstack/components}"
web_server_migration_root="${ONEINSTACK_WEB_SERVER_MIGRATION_ROOT:-/var/lib/oneinstack/web-server-migration}"
state_dir="${state_root}/${component_id}"
preserve_data="${PRESERVE_DATA:-true}"
database_password=""

upstream_commit="42d59b33765ad57c455b83bc3d4eb09ed367754a"
upstream_url="https://github.com/oneinstack/oneinstack/archive/${upstream_commit}.tar.gz"
upstream_sha256="65a9164f7d9b6037e0771b28cbd04baec0207c28f556dd78b773853898b53dfa"
upstream_cache="/var/cache/oneinstack/upstream/${upstream_commit}.tar.gz"
install_mode="${ONEINSTACK_INSTALL_MODE:-center}"
offline_package_path="${ONEINSTACK_OFFLINE_PACKAGE_PATH:-}"
upstream_archive=""
work_dir=""

die() { printf 'ERROR: %s\n' "$*" >&2; exit 1; }
emit_progress() {
  local percent="$1" code="$2" message="$3" fd="${ONEINSTACK_PROGRESS_FD:-}"
  [[ "${fd}" =~ ^[0-9]+$ ]] || return 0
  message="${message//\\/\\\\}"; message="${message//\"/\\\"}"; message="${message//$'\n'/ }"
  { printf '{"type":"progress","percent":%s,"code":"%s","message":"%s"}\n' \
      "${percent}" "${code}" "${message}" >&"${fd}"; } 2>/dev/null || true
}
require_root() { [[ "$(id -u)" -eq 0 ]] || die "This action must run as root."; }
require_command() { command -v "$1" >/dev/null 2>&1 || die "Required command not found: $1"; }
validate_managed_path() {
  local value="$1" label="$2"
  [[ -n "${value}" && "${value}" == /* && "$(realpath -m -- "${value}")" == "${value}" ]] ||
    die "${label} must be a normalized absolute path."
  case "${value}" in
    /|/usr|/usr/local|/etc|/var|/data|/home|/root) die "${label} is too broad: ${value}" ;;
  esac
}
option_for_version() {
  case "${software_version}" in
    "4.4.15.10") printf '%s\n' "1" ;;
    "5.2.3") printf '%s\n' "1" ;;
    *) die "Unsupported ${component_name} software version: ${software_version}" ;;
  esac
}
validate_inputs() {
  [[ -n "${software_version}" ]] || die "SOFTWARE_VERSION is required."
  option_for_version >/dev/null
  case "${install_mode}" in
    center) ;;
    offline)
	  [[ "${component_id}" == "phpmyadmin" ]] ||
	    die "OFFLINE_MODE_UNSUPPORTED: ${component_name} does not provide a complete offline bundle lifecycle."
      [[ -n "${offline_package_path}" && "${offline_package_path}" == /* &&
        "$(realpath -m -- "${offline_package_path}")" == "${offline_package_path}" ]] ||
        die "ONEINSTACK_OFFLINE_PACKAGE_PATH must be a normalized absolute path."
      [[ -d "${offline_package_path}" ]] || die "Offline component bundle does not exist: ${offline_package_path}"
      [[ -f "${offline_package_path}/manifest.yaml" ]] ||
        die "PHPMA_OFFLINE_BUNDLE_MISSING: manifest.yaml is missing."
      [[ -f "${offline_package_path}/files.sha256" ]] ||
        die "PHPMA_OFFLINE_BUNDLE_MISSING: files.sha256 is missing."
      grep -Eq '^[[:space:]]+id:[[:space:]]+phpmyadmin[[:space:]]*$' \
        "${offline_package_path}/manifest.yaml" ||
        die "PHPMA_OFFLINE_BUNDLE_INVALID: manifest component is not phpmyadmin."
      grep -Eq "^[[:space:]]+version:[[:space:]]+${package_version}[[:space:]]*$" \
        "${offline_package_path}/manifest.yaml" ||
        die "PHPMA_OFFLINE_BUNDLE_INVALID: manifest package version does not match."
      ;;
    *) die "ONEINSTACK_INSTALL_MODE must be center or offline." ;;
  esac
  validate_managed_path "${state_root}" ONEINSTACK_COMPONENT_STATE
  if declare -F web_server_component >/dev/null 2>&1 && web_server_component; then
    validate_managed_path "${web_server_migration_root}" ONEINSTACK_WEB_SERVER_MIGRATION_ROOT
  fi
  if [[ "${install_dir}" != "/usr/lib/jvm" ]]; then
    validate_managed_path "${install_dir}" install_dir
  fi
  if [[ -n "${data_dir}" ]]; then validate_managed_path "${data_dir}" data_dir; fi
  [[ "${preserve_data}" == "true" || "${preserve_data}" == "false" ]] ||
    die "PRESERVE_DATA must be true or false."
}
validate_database_password() {
  [[ "${component_kind}" != "db-option" ]] && return 0
  [[ ${#database_password} -ge 12 ]] || die "Database password must contain at least 12 characters."
  [[ "${database_password}" =~ ^[A-Za-z0-9._@%+=!#?-]+$ ]] ||
    die "Database password contains unsupported characters."
}

phpmyadmin_fpm_socket="${PHP_FPM_SOCKET:-}"
phpmyadmin_fpm_service=""
phpmyadmin_php_binary=""
phpmyadmin_php_version=""
phpmyadmin_web_server_kind=""
phpmyadmin_web_server_unit=""
phpmyadmin_web_server_binary=""
phpmyadmin_web_server_config=""
phpmyadmin_web_server_managed=false
phpmyadmin_web_root=""
phpmyadmin_web_alias=""
phpmyadmin_web_lower_alias=""
phpmyadmin_web_host="127.0.0.1"
phpmyadmin_web_port="80"
phpmyadmin_web_scheme="http"
phpmyadmin_web_user=""
phpmyadmin_json_escape() {
  local value="$1"
  value="${value//\\/\\\\}"
  value="${value//\"/\\\"}"
  value="${value//$'\n'/ }"
  printf '%s' "${value}"
}
phpmyadmin_die_code() {
  local code="$1"
  shift
  printf 'ERROR_CODE=%s\n' "${code}" >&2
  die "$*"
}
phpmyadmin_safe_cli_diagnostic() {
  local value="$1"
  value="${value//$'\r'/ }"
  value="${value//$'\n'/ }"
  value="${value//$'\t'/ }"
  value="$(printf '%s' "${value}" | sed -E 's#(/[-A-Za-z0-9._+@%=]+)+#<path>#g' || true)"
  printf '%s' "${value:0:384}"
}
phpmyadmin_php_binary_path() {
  local candidate managed_install_dir
  managed_install_dir="$(phpmyadmin_php_runtime_parameter install-dir || true)"
  if [[ "${managed_install_dir}" == /* ]]; then
    candidate="${managed_install_dir}/bin/php"
    if [[ -x "${candidate}" ]]; then
      printf '%s\n' "${candidate}"
      return 0
    fi
  fi
  for candidate in /usr/local/php/bin/php /usr/bin/php; do
    [[ -x "${candidate}" ]] || continue
    printf '%s\n' "${candidate}"
    return 0
  done
  return 1
}
phpmyadmin_php_runtime_parameter() {
  local key="$1" runtime_parameters="${state_root}/php/runtime-params"
  [[ -r "${runtime_parameters}" ]] || return 1
  awk -F= -v key="${key}" '
    $1 == key { sub(/^[^=]*=/, ""); print; exit }
  ' "${runtime_parameters}"
}
phpmyadmin_php_runtime_version() {
  local diagnostic error_file output status
  error_file="$(mktemp)"
  if output="$("${phpmyadmin_php_binary}" -r 'echo PHP_VERSION;' 2>"${error_file}")"; then
    if [[ -n "${output}" ]]; then
      rm -f -- "${error_file}"
      printf '%s\n' "${output}"
      return 0
    fi
    diagnostic="$(phpmyadmin_safe_cli_diagnostic "$(<"${error_file}")")"
    rm -f -- "${error_file}"
    [[ -n "${diagnostic}" ]] || diagnostic="PHP CLI returned no version output"
    phpmyadmin_die_code PHPMA_PHP_RUNTIME_UNAVAILABLE \
      "PHP CLI cannot report its runtime version: ${diagnostic}"
  else
    status=$?
    diagnostic="$(phpmyadmin_safe_cli_diagnostic "$(<"${error_file}")")"
    rm -f -- "${error_file}"
    [[ -n "${diagnostic}" ]] || diagnostic="no diagnostic output"
    phpmyadmin_die_code PHPMA_PHP_RUNTIME_UNAVAILABLE \
      "PHP CLI cannot run (exit status ${status}): ${diagnostic}"
  fi
}
phpmyadmin_require_php_runtime() {
  phpmyadmin_php_binary="$(phpmyadmin_php_binary_path || true)"
  [[ -n "${phpmyadmin_php_binary}" ]] ||
    phpmyadmin_die_code PHPMA_PHP_MISSING "phpMyAdmin requires PHP to be installed first."
  phpmyadmin_php_version="$(phpmyadmin_php_runtime_version)"
}
phpmyadmin_fpm_service_name() {
  local candidate managed_service
  managed_service="$(phpmyadmin_php_runtime_parameter service-name || true)"
  if [[ "${managed_service}" =~ ^php-fpm(-[0-9]+\.[0-9]+)?$ ]] &&
    systemctl is-active --quiet "${managed_service}.service" 2>/dev/null; then
    printf '%s\n' "${managed_service}.service"
    return 0
  fi
  for candidate in php-fpm.service php-fpm-8.5.service php-fpm-8.4.service php-fpm-8.3.service \
    php-fpm-8.2.service php-fpm-8.1.service php-fpm-7.4.service php-fpm-7.3.service \
    php-fpm-7.2.service php-fpm-7.1.service php-fpm-7.0.service php-fpm-5.6.service \
    php-fpm-5.4.service php-fpm-5.3.service php8.5-fpm.service php8.4-fpm.service php8.3-fpm.service \
    php8.2-fpm.service php8.1-fpm.service php7.4-fpm.service php7.3-fpm.service \
    php7.2-fpm.service php7.1-fpm.service php7.0-fpm.service php5.6-fpm.service; do
    systemctl is-active --quiet "${candidate}" 2>/dev/null || continue
    printf '%s\n' "${candidate}"
    return 0
  done
  return 1
}
phpmyadmin_fpm_socket_path() {
  local candidate managed_socket php_version php_line
  if [[ -n "${phpmyadmin_fpm_socket}" && -S "${phpmyadmin_fpm_socket}" ]]; then
    printf '%s\n' "${phpmyadmin_fpm_socket}"
    return 0
  fi
  managed_socket="$(phpmyadmin_php_runtime_parameter socket-path || true)"
  if [[ "${managed_socket}" == /* && -S "${managed_socket}" ]]; then
    printf '%s\n' "${managed_socket}"
    return 0
  fi
  if [[ -x "${phpmyadmin_php_binary}" ]]; then
    php_version="$(${phpmyadmin_php_binary} -r 'echo PHP_VERSION;' 2>/dev/null || true)"
    php_line="${php_version%.*}"
    candidate="/dev/shm/php-${php_line}.sock"
    if [[ "${php_line}" =~ ^[0-9]+\.[0-9]+$ && -S "${candidate}" ]]; then
      printf '%s\n' "${candidate}"
      return 0
    fi
  fi
  for candidate in /dev/shm/php-cgi.sock /dev/shm/php-*.sock /run/php/php-fpm.sock /var/run/php-fpm.sock \
    /usr/local/php/var/run/php-fpm.sock /run/php/php*-fpm.sock /var/run/php/php*-fpm.sock; do
    [[ -S "${candidate}" ]] || continue
    printf '%s\n' "${candidate}"
    return 0
  done
  return 1
}
phpmyadmin_web_user_name() {
  local candidate
  for candidate in www www-data apache caddy; do
    id "${candidate}" >/dev/null 2>&1 || continue
    printf '%s\n' "${candidate}"
    return 0
  done
  return 1
}
phpmyadmin_fpm_ready() {
  phpmyadmin_php_binary="$(phpmyadmin_php_binary_path || true)"
  phpmyadmin_fpm_service="$(phpmyadmin_fpm_service_name || true)"
  phpmyadmin_fpm_socket="$(phpmyadmin_fpm_socket_path || true)"
  [[ -n "${phpmyadmin_php_binary}" && -n "${phpmyadmin_fpm_service}" &&
    -n "${phpmyadmin_fpm_socket}" && -S "${phpmyadmin_fpm_socket}" ]]
}
phpmyadmin_require_fpm_runtime() {
  phpmyadmin_fpm_service="$(phpmyadmin_fpm_service_name || true)"
  [[ -n "${phpmyadmin_fpm_service}" ]] ||
    phpmyadmin_die_code PHPMA_FPM_NOT_RUNNING \
      "No active compatible PHP-FPM service was found; start PHP-FPM and retry."
  phpmyadmin_fpm_socket="$(phpmyadmin_fpm_socket_path || true)"
  [[ -n "${phpmyadmin_fpm_socket}" && -S "${phpmyadmin_fpm_socket}" ]] ||
    phpmyadmin_die_code PHPMA_FPM_SOCKET_UNAVAILABLE \
      "PHP-FPM is active, but its configured Unix socket is unavailable."
}
phpmyadmin_detect_web_server() {
  local managed=() external=() kind unit candidate
  for kind in nginx openresty tengine apache caddy; do
    case "${kind}" in
      nginx) candidate="oneinstack-nginx.service|nginx.service" ;;
      openresty) candidate="oneinstack-openresty.service|openresty.service" ;;
      tengine) candidate="oneinstack-tengine.service|tengine.service" ;;
      apache) candidate="oneinstack-httpd.service|httpd.service|apache2.service|apache.service" ;;
      caddy) candidate="oneinstack-caddy.service|caddy.service" ;;
    esac
    IFS='|' read -r -a units <<<"${candidate}"
    for unit in "${units[@]}"; do
      systemctl is-active --quiet "${unit}" 2>/dev/null || continue
      if [[ "${unit}" == oneinstack-* ]]; then
        managed+=("${kind}|${unit}")
      else
        external+=("${kind}|${unit}")
      fi
    done
  done
  if ((${#managed[@]} > 1)); then
    die "PHPMA_WEB_SERVER_AMBIGUOUS: multiple managed Web Servers are active: ${managed[*]}"
  fi
  if ((${#managed[@]} > 0 && ${#external[@]} > 0)); then
    die "PHPMA_WEB_SERVER_AMBIGUOUS: managed and system Web Servers are active: ${managed[*]} ${external[*]}"
  fi
  if ((${#managed[@]} == 1)); then
    IFS='|' read -r phpmyadmin_web_server_kind phpmyadmin_web_server_unit <<<"${managed[0]}"
    phpmyadmin_web_server_managed=true
  elif ((${#external[@]} > 1)); then
    die "PHPMA_WEB_SERVER_AMBIGUOUS: multiple system Web Servers are active: ${external[*]}"
  elif ((${#external[@]} == 1)); then
    IFS='|' read -r phpmyadmin_web_server_kind phpmyadmin_web_server_unit <<<"${external[0]}"
    phpmyadmin_web_server_managed=false
  else
    die "PHPMA_WEB_SERVER_NOT_FOUND: no supported active Web Server was found."
  fi
  case "${phpmyadmin_web_server_kind}" in
    nginx|tengine)
      phpmyadmin_web_server_binary="/usr/local/${phpmyadmin_web_server_kind}/sbin/nginx"
      phpmyadmin_web_server_config="${phpmyadmin_web_server_binary%/sbin/nginx}/conf/nginx.conf"
      ;;
    openresty)
      phpmyadmin_web_server_binary="/usr/local/openresty/nginx/sbin/nginx"
      phpmyadmin_web_server_config="/usr/local/openresty/nginx/conf/nginx.conf"
      ;;
    apache)
      phpmyadmin_web_server_binary="/usr/local/apache/bin/httpd"
      phpmyadmin_web_server_config="/usr/local/apache/conf/httpd.conf"
      ;;
    caddy)
      phpmyadmin_web_server_binary="/usr/local/caddy/bin/caddy"
      phpmyadmin_web_server_config="/usr/local/caddy/conf/Caddyfile"
      ;;
  esac
  if [[ "${phpmyadmin_web_server_managed}" != true ]]; then
    case "${phpmyadmin_web_server_kind}" in
      nginx|tengine|openresty)
        phpmyadmin_web_server_binary="$(command -v nginx || true)"
        phpmyadmin_web_server_config="/etc/nginx/nginx.conf"
        ;;
      apache)
        phpmyadmin_web_server_binary="$(command -v httpd || command -v apache2 || true)"
        phpmyadmin_web_server_config="/etc/httpd/conf/httpd.conf"
        ;;
      caddy)
        phpmyadmin_web_server_binary="$(command -v caddy || true)"
        phpmyadmin_web_server_config="/etc/caddy/Caddyfile"
        ;;
    esac
  fi
  [[ -x "${phpmyadmin_web_server_binary}" && -f "${phpmyadmin_web_server_config}" ]] ||
    die "PHPMA_WEB_SERVER_CONFIG_UNAVAILABLE: active ${phpmyadmin_web_server_kind} configuration could not be located."
}
phpmyadmin_nginx_roots_from_text() {
  awk '{for (i = 1; i <= NF; i++) if ($i == "root") {value = $(i + 1); gsub(/[;"]/, "", value); print value}}'
}
phpmyadmin_web_server_root() {
  local root config_root roots effective_roots
  config_root="$(dirname -- "${phpmyadmin_web_server_config}")"
  case "${phpmyadmin_web_server_kind}" in
    nginx|openresty|tengine)
      roots="$(grep -R -h -E '^[[:space:]]*root[[:space:]]+[^;]+' "${config_root}" 2>/dev/null |
        phpmyadmin_nginx_roots_from_text || true)"
      if [[ -x "${phpmyadmin_web_server_binary}" ]]; then
        effective_roots="$(${phpmyadmin_web_server_binary} -T -c "${phpmyadmin_web_server_config}" 2>&1 |
          phpmyadmin_nginx_roots_from_text || true)"
        [[ -n "${effective_roots}" ]] && roots="${roots}"$'\n'"${effective_roots}"
      fi
      ;;
    apache)
      roots="$(grep -R -h -E '^[[:space:]]*DocumentRoot[[:space:]]+' "${config_root}" 2>/dev/null |
        awk '{print $2}' | sed 's/^"//; s/"$//' || true)" ;;
    caddy)
      roots="$(grep -R -h -E '^[[:space:]]*root[[:space:]]+\*?[[:space:]]+' "${config_root}" 2>/dev/null |
        awk '{value = $NF; gsub(/[{}]/, "", value); print value}' || true)" ;;
  esac
  root="$(printf '%s\n' "${roots}" | awk '$0 == "/data/wwwroot" || $0 == "/data/wwwroot/default" {print; exit}')"
  [[ -n "${root}" ]] || root="$(printf '%s\n' "${roots}" | head -n 1)"
  root="${root%/}"
  [[ -n "${root}" && "${root}" == /* ]] || return 1
  printf '%s\n' "${root}"
}
phpmyadmin_web_server_host_port() {
  local value port host
  case "${phpmyadmin_web_server_kind}" in
    nginx|openresty|tengine)
      value="$(grep -R -h -E '^[[:space:]]*listen[[:space:]]+' "$(dirname -- "${phpmyadmin_web_server_config}")" 2>/dev/null |
        sed -n 's/^[[:space:]]*listen[[:space:]]\+\([^;[:space:]]*\).*/\1/p' | head -n 1 || true)"
      host="$(grep -R -h -E '^[[:space:]]*server_name[[:space:]]+' "$(dirname -- "${phpmyadmin_web_server_config}")" 2>/dev/null |
        awk '{for (i=2; i<=NF; i++) {gsub(";", "", $i); if ($i != "_") {print $i; exit}}}' | head -n 1 || true)" ;;
    apache)
      value="$(grep -R -h -E '^[[:space:]]*Listen[[:space:]]+' "$(dirname -- "${phpmyadmin_web_server_config}")" 2>/dev/null |
        awk 'NR == 1 {gsub(";", "", $2); print $2}' | head -n 1 || true)"
      host="$(grep -R -h -E '^[[:space:]]*ServerName[[:space:]]+' "$(dirname -- "${phpmyadmin_web_server_config}")" 2>/dev/null |
        awk 'NR == 1 {print $2}' | head -n 1 || true)" ;;
    caddy)
      value="$(grep -R -h -E '^[^[:space:]#{}]+:[0-9]+|^:[0-9]+' "$(dirname -- "${phpmyadmin_web_server_config}")" 2>/dev/null |
        sed -n 's/.*:\([0-9][0-9]*\).*/\1/p' | head -n 1 || true)"
      host="$(grep -R -h -E '^[^[:space:]#{}]+[[:space:]]*\{' "$(dirname -- "${phpmyadmin_web_server_config}")" 2>/dev/null |
        awk 'NR == 1 {gsub(/[{}]/, "", $1); sub(/^https?:\\/\\//, "", $1); sub(/:[0-9]+$/, "", $1); print $1}' || true)" ;;
  esac
  port="${value##*:}"
  [[ "${port}" =~ ^[0-9]+$ ]] || port="80"
  [[ "${port}" -ge 1 && "${port}" -le 65535 ]] || port="80"
  [[ "${port}" == 443 ]] && phpmyadmin_web_scheme="https"
  [[ "${host}" =~ ^[A-Za-z0-9._:-]+$ ]] && phpmyadmin_web_host="${host}"
  phpmyadmin_web_port="${port}"
}
phpmyadmin_web_server_version() {
  local output=""
  case "${phpmyadmin_web_server_kind}" in
    nginx|tengine|openresty|apache) output="$(${phpmyadmin_web_server_binary} -v 2>&1 || true)" ;;
    caddy) output="$(${phpmyadmin_web_server_binary} version 2>&1 || true)" ;;
  esac
  printf '%s\n' "${output}" | grep -Eo '[0-9]+\.[0-9]+(\.[0-9]+)?([.-][0-9]+)?' | head -n 1
}
phpmyadmin_route_ready() {
  phpmyadmin_detect_web_server >/dev/null 2>&1 || return 1
  [[ "${phpmyadmin_web_server_managed}" == true ]] || return 1
  phpmyadmin_web_root="$(phpmyadmin_web_server_root || true)"
  case "${phpmyadmin_web_root}" in
    /data/wwwroot) [[ -d "${install_dir}" ]] || return 1 ;;
    /data/wwwroot/default)
      [[ -L "/data/wwwroot/default/phpMyAdmin" &&
        "$(readlink -- /data/wwwroot/default/phpMyAdmin 2>/dev/null || true)" == "${install_dir}" ]] || return 1
      ;;
    *) return 1 ;;
  esac
}
phpmyadmin_configure_web_route() {
  local alias lower_alias candidate
  phpmyadmin_detect_web_server
  [[ "${phpmyadmin_web_server_managed}" == true ]] ||
    die "PHPMA_WEB_SERVER_UNMANAGED: ${phpmyadmin_web_server_kind} is not managed by OneinStack."
  phpmyadmin_web_root="$(phpmyadmin_web_server_root || true)"
  case "${phpmyadmin_web_root}" in
    /data/wwwroot)
      alias=""
      lower_alias="/data/wwwroot/phpmyadmin"
      [[ ! -e "${lower_alias}" || -L "${lower_alias}" ]] ||
        die "PHPMA_PATH_CONFLICT: existing non-managed path ${lower_alias} blocks the public route."
      [[ ! -L "${lower_alias}" ]] ||
        [[ "$(readlink -- "${lower_alias}" 2>/dev/null || true)" == "${install_dir}" ]] ||
        die "PHPMA_PATH_CONFLICT: existing symlink ${lower_alias} points elsewhere."
      [[ -L "${lower_alias}" ]] || ln -s -- "${install_dir}" "${lower_alias}"
      ;;
    /data/wwwroot/default)
      alias="/data/wwwroot/default/phpMyAdmin"
      lower_alias="/data/wwwroot/default/phpmyadmin"
      for candidate in "${alias}" "${lower_alias}"; do
        [[ ! -e "${candidate}" || -L "${candidate}" ]] ||
          die "PHPMA_PATH_CONFLICT: existing non-managed path ${candidate} blocks the public route."
        [[ ! -L "${candidate}" ]] ||
          [[ "$(readlink -- "${candidate}" 2>/dev/null || true)" == "${install_dir}" ]] ||
          die "PHPMA_PATH_CONFLICT: existing symlink ${candidate} points elsewhere."
        [[ -L "${candidate}" ]] || ln -s -- "${install_dir}" "${candidate}"
      done
      ;;
    *) die "PHPMA_ROUTE_NOT_CONFIGURED: unsupported managed Web Server document root ${phpmyadmin_web_root:-unknown}." ;;
  esac
  phpmyadmin_web_alias="${alias}"
  phpmyadmin_web_lower_alias="${lower_alias}"
  phpmyadmin_web_server_host_port
  emit_progress 82 web.route.configured "phpMyAdmin route configured for ${phpmyadmin_web_server_kind}"
}
phpmyadmin_http_ready() {
  local status body_file url result
  phpmyadmin_detect_web_server >/dev/null 2>&1 || return 1
  phpmyadmin_web_server_host_port
  body_file="$(mktemp)"
  url="${phpmyadmin_web_scheme}://127.0.0.1:${phpmyadmin_web_port}/phpMyAdmin/index.php"
  status="$(curl --silent --show-error --output "${body_file}" --write-out '%{http_code}' \
    --max-time 10 --insecure --header "Host: ${phpmyadmin_web_host}" -- "${url}" 2>/dev/null || true)"
  [[ "${status}" =~ ^2[0-9]{2}$ ]] && grep -Eiq 'phpmyadmin|phpMyAdmin' "${body_file}"
  result=$?
  rm -f -- "${body_file}"
  return "${result}"
}
phpmyadmin_write_state() {
  local actual web_version php_version
  actual="$(component_version)"
  phpmyadmin_detect_web_server >/dev/null 2>&1 || true
  phpmyadmin_web_root="$(phpmyadmin_web_server_root 2>/dev/null || true)"
  phpmyadmin_web_server_host_port 2>/dev/null || true
  web_version="$(phpmyadmin_web_server_version 2>/dev/null || true)"
  php_version="$(${phpmyadmin_php_binary:-php} -r 'echo PHP_VERSION;' 2>/dev/null || true)"
  install -d -m 0750 -- "${state_dir}"
  printf '{"component":"%s","packageVersion":"%s","softwareVersion":"%s","runtimeVersion":"%s","upstreamCommit":"%s","installDir":"%s","publicPath":"/phpMyAdmin/","webServer":"%s","webServerVersion":"%s","serviceName":"%s","configPath":"%s","documentRoot":"%s","host":"%s","port":"%s","phpVersion":"%s","phpFpmService":"%s","phpFpmSocket":"%s"}\n' \
    "${component_id}" "${package_version}" "$(phpmyadmin_json_escape "${software_version}")" \
    "$(phpmyadmin_json_escape "${actual}")" "${upstream_commit}" \
    "$(phpmyadmin_json_escape "${install_dir}")" "${phpmyadmin_web_server_kind}" \
    "$(phpmyadmin_json_escape "${web_version}")" "${phpmyadmin_web_server_unit}" \
    "$(phpmyadmin_json_escape "${phpmyadmin_web_server_config}")" \
    "$(phpmyadmin_json_escape "${phpmyadmin_web_root}")" "${phpmyadmin_web_host}" \
    "${phpmyadmin_web_port}" "$(phpmyadmin_json_escape "${php_version}")" \
    "${phpmyadmin_fpm_service}" "${phpmyadmin_fpm_socket}" >"${state_dir}/installed.json"
  chmod 0640 "${state_dir}/installed.json"
}

check_component_prerequisites() {
  if [[ "${component_id}" == "phpmyadmin" ]]; then
    for command_name in awk base64 chown cp find head install ln mktemp readlink rm sed systemctl tr; do
      require_command "${command_name}"
    done
    phpmyadmin_require_php_runtime
    local php_version php_line
    php_version="${phpmyadmin_php_version}"
    php_line="${php_version%.*}"
    case "${software_version}" in
      4.4.15.10) [[ "${php_line}" =~ ^(5\.[3-6]|7\.0)$ ]] ||
        phpmyadmin_die_code PHPMA_PHP_VERSION_UNSUPPORTED \
          "Detected PHP ${php_version}; phpMyAdmin 4.4.15.10 requires PHP 5.3-5.6 or 7.0." ;;
      5.2.3) [[ "${php_line}" =~ ^(7\.[2-4]|8\.[0-5])$ ]] ||
        phpmyadmin_die_code PHPMA_PHP_VERSION_UNSUPPORTED \
          "Detected PHP ${php_version}; phpMyAdmin 5.2.3 requires PHP 7.2-7.4 or 8.0-8.5." ;;
    esac
    phpmyadmin_web_user="$(phpmyadmin_web_user_name || true)"
    [[ -n "${phpmyadmin_web_user}" ]] || die "PHPMA_WEB_USER_MISSING: no supported Web/PHP runtime user exists."
    phpmyadmin_require_fpm_runtime
    phpmyadmin_detect_web_server >/dev/null 2>&1 ||
      die "PHPMA_WEB_SERVER_NOT_FOUND: no supported active Web Server was found."
    [[ "${phpmyadmin_web_server_managed}" == true ]] ||
      die "PHPMA_WEB_SERVER_UNMANAGED: active Web Server is not managed by OneinStack."
    phpmyadmin_web_root="$(phpmyadmin_web_server_root || true)"
    case "${phpmyadmin_web_root}" in
      /data/wwwroot|/data/wwwroot/default) ;;
      *) die "PHPMA_ROUTE_NOT_CONFIGURED: supported Web Server document root must be /data/wwwroot or /data/wwwroot/default; detected ${phpmyadmin_web_root:-unknown}." ;;
    esac
  fi
}
check_host() {
  [[ -r /etc/os-release ]] || die "/etc/os-release is unavailable."
  source /etc/os-release
  if [[ "${component_id}" == "mongodb" ]]; then
    case "${ID:-}:${VERSION_ID:-}" in
      ubuntu:20.04|ubuntu:22.04|ubuntu:24.04|debian:12|rocky:8*|rocky:9*|almalinux:8*|almalinux:9*) ;;
      *) die "MongoDB 8.0.17 is not supported on ${ID:-unknown} ${VERSION_ID:-unknown}." ;;
    esac
  else
    case "${ID:-}:${VERSION_ID:-}" in
      ubuntu:20.04|ubuntu:22.04|ubuntu:24.04|ubuntu:26.04|debian:11|debian:12|debian:13|rhel:8*|rhel:9*|rhel:10*|rocky:8*|rocky:9*|rocky:10*|almalinux:8*|almalinux:9*|almalinux:10*|centos:7*|centos:8*|centos:9*|centos:10*|ol:8*|ol:9*|ol:10*|fedora:*|sles:15*|sles:16*|opensuse-leap:*|opensuse-tumbleweed:*|opensuse:*|amzn:2023) ;;
      ubuntu:20.04|ubuntu:22.04|ubuntu:24.04|debian:11|debian:12|rocky:8*|rocky:9*|almalinux:8*|almalinux:9*) ;;
      *) die "Unsupported Linux release: ${ID:-unknown} ${VERSION_ID:-unknown}" ;;
    esac
  fi
  case "$(uname -m)" in
    x86_64) ;;
    aarch64|arm64)
      [[ "${component_id}" == "phpmyadmin" ]] || die "This development package currently supports amd64 only." ;;
    *) die "Unsupported host architecture: $(uname -m)" ;;
  esac
  if [[ "${component_id}" == "mongodb" ]] &&
    ! grep -m1 -E '^flags[[:space:]]*:' /proc/cpuinfo | grep -qw avx; then
    die "MongoDB 8.0 requires an AVX-capable CPU; this host does not expose AVX."
  fi
}
cleanup_work() {
  [[ -z "${work_dir}" || ! -d "${work_dir}" ]] || rm -rf -- "${work_dir}"
}
verify_offline_file() {
  local file="$1" relative expected
  [[ "${file}" == "${offline_package_path}"/* && -f "${file}" ]] || return 1
  relative="${file#"${offline_package_path}/"}"
  expected="$(awk -v relative="${relative}" '$2 == relative {print $1; exit}' \
    "${offline_package_path}/files.sha256" 2>/dev/null || true)"
  [[ "${expected}" =~ ^[a-fA-F0-9]{64}$ ]] || return 1
  printf '%s  %s\n' "${expected}" "${file}" | sha256sum --check --status
}
offline_phpmyadmin_archive() {
  local archive
  archive="$(find "${offline_package_path}/artifacts/phpmyadmin/${software_version}" \
    -maxdepth 1 -type f \( -name '*.tar.gz' -o -name '*.tgz' \) -print -quit 2>/dev/null || true)"
  [[ -n "${archive}" ]] ||
    die "PHPMA_OFFLINE_ARTIFACT_MISSING: phpMyAdmin ${software_version} archive is missing."
  verify_offline_file "${archive}" ||
    die "PHPMA_CHECKSUM_MISMATCH: ${archive}"
  printf '%s\n' "${archive}"
}
phpmyadmin_configure() {
  local secret
  if [[ ! -f "${install_dir}/config.inc.php" ]]; then
    [[ -f "${install_dir}/config.sample.inc.php" ]] ||
      die "PHPMA_CONFIG_TEMPLATE_MISSING: verified phpMyAdmin release does not contain config.sample.inc.php."
    cp -- "${install_dir}/config.sample.inc.php" "${install_dir}/config.inc.php"
  fi
  install -d -m 0750 -- "${install_dir}/upload" "${install_dir}/save"
  secret="$(head -c 48 /dev/urandom | base64 | tr -dc 'A-Za-z0-9' | head -c 32)"
  [[ ${#secret} -ge 32 ]] || secret="oneinstack-phpmyadmin-change-this-secret"
  sed -i "s@blowfish_secret.*;@blowfish_secret'] = '${secret}';@" "${install_dir}/config.inc.php"
  sed -i "s@UploadDir.*@UploadDir'] = 'upload';@" "${install_dir}/config.inc.php"
  sed -i "s@SaveDir.*@SaveDir'] = 'save';@" "${install_dir}/config.inc.php"
  sed -i "s@host'].*@host'] = '127.0.0.1';@" "${install_dir}/config.inc.php"
}
install_phpmyadmin_archive() {
  local archive="${1:-${upstream_archive}}" source_dir previous_config candidate
  work_dir="$(mktemp -d "/usr/local/src/oneinstack-${component_id}.XXXXXX")"
  tar -xzf "${archive}" -C "${work_dir}"
  source_dir=""
  while IFS= read -r candidate; do
    [[ -f "${candidate}/index.php" && -f "${candidate}/config.sample.inc.php" ]] || continue
    source_dir="${candidate}"
    break
  done < <(find "${work_dir}" -mindepth 1 -maxdepth 3 -type d -print)
  [[ -n "${source_dir}" ]] ||
    die "PHPMA_OFFLINE_ARTIFACT_MISSING: phpMyAdmin root with index.php and config.sample.inc.php is missing from ${archive}."
  previous_config="${state_dir}/migration/install/config.inc.php"
  rm -rf -- "${install_dir}"
  install -d -m 0755 -- "${install_dir}"
  cp -a -- "${source_dir}/." "${install_dir}/"
  if [[ -f "${previous_config}" ]]; then
    cp -a -- "${previous_config}" "${install_dir}/config.inc.php"
  fi
  phpmyadmin_configure
  normalize_runtime_permissions
}
install_phpmyadmin_offline() {
  upstream_archive="$(offline_phpmyadmin_archive)"
  install_phpmyadmin_archive
}
install_phpmyadmin_online() {
  local filename source_url archive partial source_sha256
  case "${software_version}" in
    4.4.15.10)
      filename="phpMyAdmin-4.4.15.10-all-languages.tar.gz"
      source_sha256="c28ba15b3b95b9d179b312f5c9fcd59a0593a315ddc6f7906f98a74508ffd32d"
      ;;
    5.2.3)
      filename="phpMyAdmin-5.2.3-all-languages.tar.gz"
      source_sha256="12ba1c425fa4071abbd4e7668c9ebdeac0b0755a467a6d6d5026122bb47c102b"
      ;;
    *) die "Unsupported phpMyAdmin software version: ${software_version}" ;;
  esac
  archive="/var/cache/oneinstack/phpmyadmin/${software_version}/${filename}"
  source_url="https://files.phpmyadmin.net/phpMyAdmin/${software_version}/${filename}"
  install -d -m 0750 -- "$(dirname -- "${archive}")"
  if [[ -f "${archive}" ]] &&
    ! printf '%s  %s\n' "${source_sha256}" "${archive}" | sha256sum --check --status; then
    rm -f -- "${archive}"
  fi
  if [[ ! -f "${archive}" ]]; then
    partial="${archive}.part"
    rm -f -- "${partial}"
    curl --proto '=https' --tlsv1.2 --fail --location --retry 3 \
      --connect-timeout 20 --output "${partial}" "${source_url}"
    printf '%s  %s\n' "${source_sha256}" "${partial}" |
      sha256sum --check --status || die "phpMyAdmin ${software_version} checksum mismatch."
    mv -- "${partial}" "${archive}"
  fi
  upstream_archive="${archive}"
  install_phpmyadmin_archive
}
download_upstream() {
  install -d -m 0750 -- "$(dirname -- "${upstream_cache}")"
  install -d -m 0755 -- /usr/local/src
  if [[ "${install_mode}" == "offline" ]]; then
    upstream_archive="${offline_package_path}/artifacts/common/oneinstack-${upstream_commit}.tar.gz"
    [[ -f "${upstream_archive}" ]] ||
      die "PHPMA_OFFLINE_ARTIFACT_MISSING: ${upstream_archive}"
    verify_offline_file "${upstream_archive}" ||
      die "PHPMA_CHECKSUM_MISMATCH: ${upstream_archive}"
  else
    upstream_archive="${upstream_cache}"
  fi
  if [[ "${install_mode}" == "offline" ]]; then
    work_dir="$(mktemp -d "/usr/local/src/oneinstack-${component_id}.XXXXXX")"
    tar -xzf "${upstream_archive}" --strip-components=1 -C "${work_dir}"
    [[ -x "${work_dir}/install.sh" ]] || die "OneinStack installer is missing from the offline archive."
    return 0
  fi
  if [[ -f "${upstream_cache}" ]] &&
    ! printf '%s  %s\n' "${upstream_sha256}" "${upstream_cache}" | sha256sum --check --status; then
    rm -f -- "${upstream_cache}"
  fi
  if [[ ! -f "${upstream_cache}" ]]; then
    local partial="${upstream_cache}.part"
    rm -f -- "${partial}"
    curl --proto '=https' --tlsv1.2 --fail --location --retry 3 \
      --connect-timeout 20 --output "${partial}" "${upstream_url}"
    printf '%s  %s\n' "${upstream_sha256}" "${partial}" |
      sha256sum --check --status || die "OneinStack upstream archive checksum mismatch."
    mv -- "${partial}" "${upstream_cache}"
  fi
  upstream_archive="${upstream_cache}"
  work_dir="$(mktemp -d "/usr/local/src/oneinstack-${component_id}.XXXXXX")"
  tar -xzf "${upstream_archive}" --strip-components=1 -C "${work_dir}"
  [[ -x "${work_dir}/install.sh" ]] || die "OneinStack installer is missing from its archive."
}
patch_upstream_java_installers() {
  [[ "${component_id}" == "java" || "${component_id}" == "tomcat" ]] || return 0
  local script
  for script in "${work_dir}"/include/openjdk-{8,11,17}.sh; do
    [[ -f "${script}" ]] || die "OneinStack Java installer is missing: ${script##*/}"
    # Minimal Debian installations do not include sudo. Component actions
    # already require root, so invoking apt-key directly is both sufficient
    # and necessary for the pinned upstream Java 8 repository setup.
    sed -i 's/ | sudo apt-key add -/ | apt-key add -/' "${script}"
  done
  if grep -R -n -E '\|[[:space:]]+sudo[[:space:]]+apt-key' \
    "${work_dir}"/include/openjdk-{8,11,17}.sh >/dev/null; then
    die "OneinStack Java installer still requires sudo after compatibility patching."
  fi
}
patch_upstream_optional_database_service_actions() {
  case "${component_id}" in
    nginx|tengine|openresty|caddy|apache) ;;
    *) return 0 ;;
  esac
  local target
  for target in "${work_dir}/install.sh" "${work_dir}"/include/*.sh; do
    [[ -f "${target}" ]] || continue
    sed -i \
      -e 's@systemctl start mysqld.service@systemctl start mysqld.service 2>/dev/null || true@g' \
      -e 's@systemctl restart mysqld.service@systemctl restart mysqld.service 2>/dev/null || true@g' \
      -e 's@systemctl start mysql.service@systemctl start mysql.service 2>/dev/null || true@g' \
      -e 's@systemctl restart mysql.service@systemctl restart mysql.service 2>/dev/null || true@g' \
      -e 's@service mysqld start@service mysqld start >/dev/null 2>&1 || true@g' \
      -e 's@service mysqld restart@service mysqld restart >/dev/null 2>&1 || true@g' \
      -e 's@service mysql start@service mysql start >/dev/null 2>&1 || true@g' \
      -e 's@service mysql restart@service mysql restart >/dev/null 2>&1 || true@g' \
      "${target}"
  done
}
patch_upstream_caddy_service_actions() {
  [[ "${component_id}" == "caddy" ]] || return 0
  local script="${work_dir}/include/caddy.sh"
  [[ -f "${script}" ]] || die "OneinStack Caddy installer is missing."
  # Caddy may be installed alongside another web server. Center controls
  # service activation, so an occupied HTTP/HTTPS port must not make the
  # package installation itself fail.
  sed -i \
    -e 's@^[[:space:]]*systemctl enable caddy[[:space:]]*$@  : # Center manages the Caddy service state@g' \
    -e 's@^[[:space:]]*systemctl start caddy[[:space:]]*$@  : # Center manages the Caddy service state@g' \
    "${script}"
  if grep -Eq '^[[:space:]]*systemctl (enable|start) caddy[[:space:]]*$' "${script}"; then
    die "OneinStack Caddy service actions were not isolated from package installation."
  fi
}
write_managed_web_service_unit() {
  [[ -n "${service_name}" ]] || return 0
  install -d -m 0755 -- /etc/systemd/system
	case "${component_id}" in
	    nginx|tengine)
      cat >"/etc/systemd/system/${service_name}.service" <<EOF
[Unit]
Description=${component_name}
After=network-online.target
Wants=network-online.target
[Service]
Type=simple
ExecStartPre=${install_dir}/sbin/nginx -t -q -c ${install_dir}/conf/nginx.conf
ExecStart=${install_dir}/sbin/nginx -c ${install_dir}/conf/nginx.conf -g 'daemon off;'
ExecReload=${install_dir}/sbin/nginx -c ${install_dir}/conf/nginx.conf -s reload
KillSignal=SIGQUIT
TimeoutStartSec=30s
TimeoutStopSec=30s
PrivateTmp=true
LimitNOFILE=65535
[Install]
WantedBy=multi-user.target
EOF
      ;;
    openresty)
      cat >"/etc/systemd/system/${service_name}.service" <<EOF
[Unit]
Description=${component_name}
After=network-online.target
Wants=network-online.target
[Service]
Type=simple
ExecStartPre=${install_dir}/nginx/sbin/nginx -t -q -c ${install_dir}/nginx/conf/nginx.conf
ExecStart=${install_dir}/nginx/sbin/nginx -c ${install_dir}/nginx/conf/nginx.conf -g 'daemon off;'
ExecReload=${install_dir}/nginx/sbin/nginx -c ${install_dir}/nginx/conf/nginx.conf -s reload
KillSignal=SIGQUIT
TimeoutStartSec=30s
TimeoutStopSec=30s
PrivateTmp=true
LimitNOFILE=65535
[Install]
WantedBy=multi-user.target
EOF
      ;;
	    apache)
	      cat >"/etc/systemd/system/${service_name}.service" <<EOF
[Unit]
Description=${component_name}
After=network-online.target
Wants=network-online.target
[Service]
Type=simple
ExecStartPre=${install_dir}/bin/httpd -t
ExecStart=${install_dir}/bin/httpd -DFOREGROUND
ExecReload=${install_dir}/bin/httpd -k graceful
ExecStop=/bin/kill -TERM \$MAINPID
TimeoutStartSec=30s
TimeoutStopSec=30s
PrivateTmp=true
LimitNOFILE=65535
[Install]
WantedBy=multi-user.target
EOF
	      ;;
	    caddy)
	      cat >"/etc/systemd/system/${service_name}.service" <<EOF
[Unit]
Description=${component_name}
Documentation=https://caddyserver.com/docs/
After=network.target network-online.target
Wants=network-online.target
[Service]
Type=notify
User=caddy
Group=caddy
ExecStart=${install_dir}/bin/caddy run --environ --config ${install_dir}/conf/Caddyfile
ExecReload=${install_dir}/bin/caddy reload --config ${install_dir}/conf/Caddyfile --force
ExecStop=/bin/kill -TERM \$MAINPID
TimeoutStopSec=5s
LimitNOFILE=1048576
PrivateTmp=true
ProtectSystem=full
AmbientCapabilities=CAP_NET_ADMIN CAP_NET_BIND_SERVICE
[Install]
WantedBy=multi-user.target
EOF
	      ;;
	    *) return 0 ;;
	  esac
	chmod 0644 "/etc/systemd/system/${service_name}.service"
  systemctl daemon-reload
}
prepare_web_service_unit() {
  case "${component_id}" in
    nginx|tengine|openresty|caddy|apache) ;;
    *) return 0 ;;
  esac

  if [[ "${component_id}" == "caddy" ]]; then
    write_managed_web_service_unit
    return 0
  fi

  if [[ -f "/etc/systemd/system/${service_name}.service" ]]; then
    write_managed_web_service_unit
    return 0
  fi
  service_unit_ready && return 0
  migrate_legacy_web_service || true
  if [[ -f "/etc/systemd/system/${service_name}.service" ]]; then
    write_managed_web_service_unit
    return 0
  fi
  service_unit_ready && return 0
  write_managed_web_service_unit
}
prepare_verified_source_override() {
  local filename source_url source_sha256 partial
  case "${component_id}:${software_version}" in
    nginx:1.31.0)
      filename="nginx-1.31.0.tar.gz"
      source_url="https://nginx.org/download/${filename}"
      source_sha256="6d5b00d45393af2e4e7c52a442d2a198f0ccbc7678ed062a46f403edd833ebaa"
      ;;
    apache:2.4.66)
      filename="nghttp2-1.64.0.tar.gz"
      source_url="https://github.com/nghttp2/nghttp2/releases/download/v1.64.0/${filename}"
      source_sha256="20e73f3cf9db3f05988996ac8b3a99ed529f4565ca91a49eb0550498e10621e8"
      ;;
    postgresql:18.1)
      stage_verified_source postgresql-18.1.tar.gz \
        https://ftp.postgresql.org/pub/source/v18.1/postgresql-18.1.tar.gz \
        b0f18c2d6973d2aa023cfc77feda787d7bbe9c31a3977d0f04ac29885fb98ec4
      return 0
      ;;
    mariadb:10.11)
      filename="mariadb-10.11.15-linux-systemd-x86_64.tar.gz"
      stage_verified_source "${filename}" \
        "https://archive.mariadb.org/mariadb-10.11.15/bintar-linux-systemd-x86_64/${filename}" \
        50024a672742cb5957e98b801b0a6f7a39cec22c76ed1bef3193acbccadeded8
      md5sum "${work_dir}/src/${filename}" >"${work_dir}/src/${filename}.md5"
      return 0
      ;;
    *) return 0 ;;
  esac
  install -d -m 0755 -- "${work_dir}/src"
  if [[ -f "${work_dir}/src/${filename}" ]] &&
    printf '%s  %s\n' "${source_sha256}" "${work_dir}/src/${filename}" |
      sha256sum --check --status; then
    return 0
  fi
  partial="${work_dir}/src/${filename}.part"
  rm -f -- "${partial}" "${work_dir}/src/${filename}"
  curl --proto '=https' --tlsv1.2 --fail --location --retry 3 \
    --connect-timeout 20 --output "${partial}" "${source_url}"
  printf '%s  %s\n' "${source_sha256}" "${partial}" |
    sha256sum --check --status || die "${filename} checksum mismatch."
  mv -- "${partial}" "${work_dir}/src/${filename}"
}
stage_verified_source() {
  local filename="$1" source_url="$2" source_sha256="$3"
  local source_cache partial
  source_cache="/var/cache/oneinstack/sources/${component_id}/${filename}"
  install -d -m 0750 -- "$(dirname -- "${source_cache}")"
  install -d -m 0755 -- "${work_dir}/src"
  if [[ -f "${source_cache}" ]] &&
    ! printf '%s  %s\n' "${source_sha256}" "${source_cache}" |
      sha256sum --check --status; then
    rm -f -- "${source_cache}"
  fi
  if [[ ! -f "${source_cache}" ]]; then
    partial="${source_cache}.part"
    rm -f -- "${partial}"
    curl --proto '=https' --tlsv1.2 --fail --location --retry 3 --connect-timeout 20 \
      --output "${partial}" "${source_url}"
    printf '%s  %s\n' "${source_sha256}" "${partial}" |
      sha256sum --check --status || die "${filename} checksum mismatch."
    mv -- "${partial}" "${source_cache}"
  fi
  install -m 0644 -- "${source_cache}" "${work_dir}/src/${filename}"
}
prepare_php_sources() {
  [[ "${component_id}" == "php" ]] || return 0
  stage_verified_source libiconv-1.17.tar.gz \
    https://ftp.gnu.org/pub/gnu/libiconv/libiconv-1.17.tar.gz \
    8f74213b56238c85a50a5329f77e06198771e70dd9a739779f4c02f65d971313
  stage_verified_source curl-8.17.0.tar.gz \
    https://curl.se/download/curl-8.17.0.tar.gz \
    e8e74cdeefe5fb78b3ae6e90cd542babf788fa9480029cfcee6fd9ced42b7910
  stage_verified_source mhash-0.9.9.9.tar.gz \
    'https://sourceforge.net/projects/mhash/files/mhash/0.9.9.9/mhash-0.9.9.9.tar.gz/download?use_mirror=pilotfiber' \
    3dcad09a63b6f1f634e64168dd398e9feb9925560f9b671ce52283a79604d13e
  stage_verified_source libmcrypt-2.5.8.tar.gz \
    'https://sourceforge.net/projects/mcrypt/files/Libmcrypt/2.5.8/libmcrypt-2.5.8.tar.gz/download?use_mirror=pilotfiber' \
    e4eb6c074bbab168ac47b947c195ff8cef9d51a211cdd18ca9c9ef34d27a373e
  stage_verified_source mcrypt-2.6.8.tar.gz \
    'https://sourceforge.net/projects/mcrypt/files/MCrypt/2.6.8/mcrypt-2.6.8.tar.gz/download?use_mirror=pilotfiber' \
    5145aa844e54cca89ddab6fb7dd9e5952811d8d787c4f4bf27eb261e6c182098
  stage_verified_source freetype-2.10.1.tar.gz \
    'https://sourceforge.net/projects/freetype/files/freetype2/2.10.1/freetype-2.10.1.tar.gz/download?use_mirror=pilotfiber' \
    3a60d391fd579440561bf0e7f31af2222bc610ad6ce4d9d7bd2165bca8669110
  case "${software_version}" in
    8.3)
      stage_verified_source php-8.3.29.tar.gz \
        https://www.php.net/distributions/php-8.3.29.tar.gz \
        8565fa8733c640b60da5ab4944bf2d4081f859915b39e29b3af26cf23443ed97
      stage_verified_source argon2-20171227.tar.gz \
        https://github.com/P-H-C/phc-winner-argon2/archive/refs/tags/20171227.tar.gz \
        eaea0172c1f4ee4550d1b6c9ce01aab8d1ab66b4207776aa67991eb5872fdcd8
      stage_verified_source libsodium-1.0.21.tar.gz \
        https://download.libsodium.org/libsodium/releases/libsodium-1.0.21.tar.gz \
        9e4285c7a419e82dedb0be63a72eea357d6943bc3e28e6735bf600dd4883feaf
      stage_verified_source libzip-1.11.4.tar.gz \
        https://github.com/nih-at/libzip/releases/download/v1.11.4/libzip-1.11.4.tar.gz \
        82e9f2f2421f9d7c2466bbc3173cd09595a88ea37db0d559a9d0a2dc60dc722e
      sed -i \
        -e 's@pushd argon2-${argon2_ver}@pushd phc-winner-argon2-${argon2_ver}@' \
        -e 's@rm -rf argon2-${argon2_ver}@rm -rf phc-winner-argon2-${argon2_ver}@' \
        "${work_dir}"/include/php-*.sh
      ;;
  esac
}
prepare_mongodb_sources() {
  [[ "${component_id}" == "mongodb" ]] || return 0
  local distribution mongo_official source_url source_sha256
  local mongo_expected="mongodb-linux-x86_64-8.0.17.tgz"
  local repack_dir extracted_dir
  distribution="$(. /etc/os-release && printf '%s:%s' "${ID:-}" "${VERSION_ID:-}")"
  case "${distribution}" in
    ubuntu:20.04)
      mongo_official="mongodb-linux-x86_64-ubuntu2004-8.0.17.tgz"
      source_sha256="b90f31da88ba94ad20b7a18801a6abd85b7f31bf42677e11b53ec5f82cd5dedf"
      ;;
    ubuntu:22.04)
      mongo_official="mongodb-linux-x86_64-ubuntu2204-8.0.17.tgz"
      source_sha256="4372a8e503a61814c565d4ccbc5f6765787944772e8969c13ba96c99cca11f75"
      ;;
    ubuntu:24.04)
      mongo_official="mongodb-linux-x86_64-ubuntu2404-8.0.17.tgz"
      source_sha256="fe7bccea2ac1eed16867e9ae5a60481455ee30796f285fe67d0d02a6c2abbdda"
      ;;
    debian:12)
      mongo_official="mongodb-linux-x86_64-debian12-8.0.17.tgz"
      source_sha256="b774ed64ec13732d25cd8c937bcd221ceadd0c746371f2279aa63aa492c1d0d0"
      ;;
    rocky:8*|rocky:9*|almalinux:8*|almalinux:9*)
      mongo_official="mongodb-linux-x86_64-rhel8-8.0.17.tgz"
      source_sha256="c8986f6fc001d456bd2160c2e96d24201599627eb5832f49d95673779a2768cf"
      ;;
    *) die "MongoDB 8.0.17 has no verified binary for ${distribution}." ;;
  esac
  source_url="https://fastdl.mongodb.org/linux/${mongo_official}"
  stage_verified_source "${mongo_official}" "${source_url}" "${source_sha256}"
  stage_verified_source mongosh-2.3.1-linux-x64.tgz \
    https://downloads.mongodb.com/compass/mongosh-2.3.1-linux-x64.tgz \
    f1fefacf0b5b1f2fca966200478fee1e278be2619df5e2605cbc0f24dd179a1a
  repack_dir="$(mktemp -d "${work_dir}/src/mongodb-repack.XXXXXX")"
  tar -xzf "${work_dir}/src/${mongo_official}" -C "${repack_dir}"
  extracted_dir="${repack_dir}/${mongo_official%.tgz}"
  [[ -d "${extracted_dir}" ]] || die "Unexpected MongoDB archive layout."
  mv -- "${extracted_dir}" "${repack_dir}/${mongo_expected%.tgz}"
  tar -czf "${work_dir}/src/${mongo_expected}" -C "${repack_dir}" "${mongo_expected%.tgz}"
  md5sum "${work_dir}/src/${mongo_expected}" |
    awk '{print $1 "  '"${mongo_expected}"'"}' >"${work_dir}/src/${mongo_expected}.md5"
  rm -rf -- "${repack_dir}"
}
patch_mongodb_installer() {
  [[ "${component_id}" == "mongodb" ]] || return 0
  local installer="${work_dir}/include/mongodb.sh"
  [[ -f "${installer}" ]] || die "OneinStack MongoDB installer is missing."
  # MongoDB 6.1 removed storage.journal.enabled. The pinned upstream script
  # still writes that option, which makes MongoDB 8.0 reject mongod.conf.
  sed -i '/^[[:space:]]*journal:[[:space:]]*$/,+1d' "${installer}"
  if grep -Eq '^[[:space:]]*journal:[[:space:]]*$' "${installer}"; then
    die "MongoDB installer still contains the removed storage.journal option."
  fi
  # Make the config readable before the upstream script's first start. If
  # this is delayed until after the script returns, MongoDB user creation can
  # be skipped while the upstream script still prints success.
  sed -i \
    's@^[[:space:]]*systemctl start mongod$@  chown root:mongod /etc/mongod.conf\n  chmod 0640 /etc/mongod.conf\n  systemctl start mongod@' \
    "${installer}"
  grep -Fq 'chown root:mongod /etc/mongod.conf' "${installer}" ||
    die "MongoDB installer config permission fix could not be applied."
}
mongodb_service_diagnostics() {
  [[ "${component_id}" == "mongodb" ]] || return 0
  {
    printf 'MongoDB service diagnostics:\n'
    systemctl status "${service_name}.service" --no-pager --full 2>&1 || true
    journalctl -u "${service_name}.service" -n 80 --no-pager 2>&1 || true
    if [[ -f "${data_dir}/mongod.log" ]]; then
      printf '\nLast MongoDB log entries:\n'
      tail -n 80 -- "${data_dir}/mongod.log" 2>&1 || true
    fi
  } >&2
}
prepare_tomcat_runtime() {
  [[ "${component_id}" == "tomcat" ]] || return 0
  local family filename source_url source_sha256 source_cache partial
  if command -v apt-get >/dev/null 2>&1; then
    emit_progress 22 refresh_packages "Refreshing operating-system package metadata"
    apt-get update
    DEBIAN_FRONTEND=noninteractive apt-get install -y -- libssl-dev
  fi
  if [[ ! -d /usr/local/openssl && -d /usr/include/openssl ]]; then
    sed -i 's@^openssl_install_dir=.*@openssl_install_dir=/usr@' "${work_dir}/options.conf"
  fi
  sed -i \
    's@if \[ -e "${apr_install_dir}/lib/libtcnative-1.la" \]; then@if [ -e "${apr_install_dir}/lib/libtcnative-1.la" -o -e "${apr_install_dir}/lib/libtcnative-2.la" ]; then@' \
    "${work_dir}"/include/tomcat-*.sh
  case "${software_version}" in
    7.0.109) family=7; source_sha256=ebfeb051e6da24bce583a4105439bfdafefdc7c5bdd642db2ab07e056211cb31 ;;
    8.5.96) family=8; source_sha256=0307cfb85e58a2ff3d033d464bdc01f03fd5452618b17baf64368b1e6b02e886 ;;
    9.0.113) family=9; source_sha256=790db2b8092b7954dec2afc6af71a7bbb6c67998198516dd6a9f865661b5d2a7 ;;
    10.1.50) family=10; source_sha256=f74f9f1a7ac2cf6eeede2c50f45088d9c3e55f77d5777f9f7033ed3d43ef529c ;;
    11.0.15) family=11; source_sha256=c515a0edb273846b4d7926fa8175aaa46905f45d5e2af588e01783e35a89a69c ;;
    *) die "Unsupported ${component_name} software version: ${software_version}" ;;
  esac
  filename="apache-tomcat-${software_version}.tar.gz"
  source_url="https://archive.apache.org/dist/tomcat/tomcat-${family}/v${software_version}/bin/${filename}"
  source_cache="/var/cache/oneinstack/sources/tomcat/${filename}"
  install -d -m 0750 -- "$(dirname -- "${source_cache}")"
  install -d -m 0755 -- "${work_dir}/src"
  if [[ -f "${source_cache}" ]] &&
    ! printf '%s  %s\n' "${source_sha256}" "${source_cache}" |
      sha256sum --check --status; then
    rm -f -- "${source_cache}"
  fi
  if [[ ! -f "${source_cache}" ]]; then
    partial="${source_cache}.part"
    rm -f -- "${partial}"
    curl --proto '=https' --tlsv1.2 --fail --location --retry 3 --connect-timeout 20 \
      --output "${partial}" "${source_url}"
    printf '%s  %s\n' "${source_sha256}" "${partial}" |
      sha256sum --check --status || die "${filename} checksum mismatch."
    mv -- "${partial}" "${source_cache}"
  fi
  install -m 0644 -- "${source_cache}" "${work_dir}/src/${filename}"
}
java_package_for_version() {
  local distribution
  distribution="$(. /etc/os-release && printf '%s' "${ID:-}")"
  case "${distribution}:${software_version}" in
    ubuntu:8|ubuntu:11|ubuntu:17)
      printf 'apt:openjdk-%s-jdk\n' "${software_version}"
      ;;
    debian:8) printf '%s\n' apt:temurin-8-jdk ;;
    debian:11|debian:17)
      printf 'apt:openjdk-%s-jdk\n' "${software_version}"
      ;;
    rocky:8|almalinux:8) printf '%s\n' rpm:java-1.8.0-openjdk-devel ;;
    rocky:11|almalinux:11) printf '%s\n' rpm:java-11-openjdk-devel ;;
    rocky:17|almalinux:17) printf '%s\n' rpm:java-17-openjdk-devel ;;
    ubuntu:18|debian:18|rocky:18|almalinux:18) printf '%s\n' direct ;;
    *) die "Unsupported ${component_name} software version: ${software_version}" ;;
  esac
}
java_home_for_version() {
  local candidate
  case "${software_version}" in
    8|11|17)
      for candidate in "/usr/lib/jvm/java-${software_version}-openjdk-amd64" \
        "/usr/lib/jvm/java-${software_version}-openjdk" \
        "/usr/lib/jvm/temurin-${software_version}-jdk-amd64" \
        "/usr/lib/jvm/temurin-${software_version}-jdk" \
        /usr/lib/jvm/java-${software_version}-openjdk-* \
        /usr/lib/jvm/temurin-${software_version}-jdk-*; do
        [[ -x "${candidate}/bin/java" ]] && { printf '%s\n' "${candidate}"; return 0; }
      done
      if [[ "${software_version}" == "8" ]]; then
        for candidate in /usr/lib/jvm/java-1.8.0-openjdk-*; do
          [[ -x "${candidate}/bin/java" ]] && { printf '%s\n' "${candidate}"; return 0; }
        done
      fi
      ;;
    18)
      candidate=/usr/lib/jvm/java-18-openjdk-amd64
      [[ -x "${candidate}/bin/java" ]] && { printf '%s\n' "${candidate}"; return 0; }
      ;;
  esac
  return 1
}
prepare_java_runtime() {
  [[ "${component_id}" == "java" ]] || return 0
  local owner manager package archive partial extracted
  owner="$(java_package_for_version)"
  manager="${owner%%:*}"
  package="${owner#*:}"
  install -d -m 0750 -- "${state_dir}"
  if [[ "${manager}" == "direct" ]]; then
    if java_home_for_version >/dev/null 2>&1; then
      printf '%s\n' preexisting >"${state_dir}/java-runtime-owner"
      return 0
    fi
    archive=/var/cache/oneinstack/java/OpenJDK18U-jdk_x64_linux_hotspot_18.0.2.1_1.tar.gz
    install -d -m 0750 -- "$(dirname -- "${archive}")"
    install -d -m 0755 -- /usr/lib/jvm
    if [[ -f "${archive}" ]] &&
      ! printf '%s  %s\n' 7d6beba8cfc0a8347f278f7414351191a95a707d46b6586e9a786f2669af0f8b "${archive}" |
        sha256sum --check --status; then
      rm -f -- "${archive}"
    fi
    if [[ ! -f "${archive}" ]]; then
      partial="${archive}.part"
      rm -f -- "${partial}"
      curl --proto '=https' --tlsv1.2 --fail --location --retry 3 --connect-timeout 20 \
        --output "${partial}" \
        'https://github.com/adoptium/temurin18-binaries/releases/download/jdk-18.0.2.1%2B1/OpenJDK18U-jdk_x64_linux_hotspot_18.0.2.1_1.tar.gz'
      printf '%s  %s\n' 7d6beba8cfc0a8347f278f7414351191a95a707d46b6586e9a786f2669af0f8b "${partial}" |
        sha256sum --check --status || die "Eclipse Temurin Java 18 checksum mismatch."
      mv -- "${partial}" "${archive}"
    fi
    extracted="$(mktemp -d /usr/lib/jvm/.java18.XXXXXX)"
    tar -xzf "${archive}" --strip-components=1 -C "${extracted}"
    [[ -x "${extracted}/bin/java" ]] || die "Eclipse Temurin Java 18 archive is invalid."
    mv -- "${extracted}" /usr/lib/jvm/java-18-openjdk-amd64
    printf '%s\n' direct:/usr/lib/jvm/java-18-openjdk-amd64 >"${state_dir}/java-runtime-owner"
    return 0
  fi
  case "${manager}" in
    apt)
      if dpkg-query -W -f='${Status}' "${package}" 2>/dev/null |
        grep -q 'install ok installed'; then
        printf '%s\n' preexisting >"${state_dir}/java-runtime-owner"
      else
        printf 'apt:%s\n' "${package}" >"${state_dir}/java-runtime-owner"
      fi
      emit_progress 22 refresh_packages "Refreshing operating-system package metadata"
      apt-get update
      ;;
    rpm)
      if rpm -q "${package}" >/dev/null 2>&1; then
        printf '%s\n' preexisting >"${state_dir}/java-runtime-owner"
      else
        printf 'rpm:%s\n' "${package}" >"${state_dir}/java-runtime-owner"
      fi
      ;;
    *) die "Unsupported Java package manager: ${manager}" ;;
  esac
}
configure_java_runtime() {
  [[ "${component_id}" == "java" ]] || return 0
  local java_home binary priority
  java_home="$(java_home_for_version)" || die "Installed Java ${software_version} runtime was not found."
  priority="$((1000 + software_version))"
  for binary in java javac jar javadoc; do
    [[ -x "${java_home}/bin/${binary}" ]] || continue
    update-alternatives --install "/usr/bin/${binary}" "${binary}" "${java_home}/bin/${binary}" "${priority}"
    update-alternatives --set "${binary}" "${java_home}/bin/${binary}"
  done
  cat > /etc/profile.d/openjdk.sh <<EOF
export JAVA_HOME=${java_home}
export CLASSPATH=\$JAVA_HOME/lib/tools.jar:\$JAVA_HOME/lib/dt.jar:\$JAVA_HOME/lib
export PATH=\$JAVA_HOME/bin:\$PATH
EOF
  chmod 0644 /etc/profile.d/openjdk.sh
}
remove_managed_java_runtime() {
  [[ "${component_id}" == "java" ]] || return 0
  local owner package java_home binary
  owner="$(cat "${state_dir}/java-runtime-owner" 2>/dev/null || true)"
  java_home="$(java_home_for_version 2>/dev/null || true)"
  if [[ -n "${java_home}" ]]; then
    for binary in java javac jar javadoc; do
      [[ -x "${java_home}/bin/${binary}" ]] || continue
      update-alternatives --remove "${binary}" "${java_home}/bin/${binary}" 2>/dev/null || true
    done
  fi
  case "${owner}" in
    apt:openjdk-*-jdk|apt:temurin-*-jdk)
      package="${owner#apt:}"
      DEBIAN_FRONTEND=noninteractive apt-get purge -y -- "${package}"
      ;;
    rpm:java-*-openjdk-devel)
      package="${owner#rpm:}"
      if command -v dnf >/dev/null 2>&1; then
        dnf remove -y -- "${package}"
      else
        yum remove -y -- "${package}"
      fi
      ;;
    direct:/usr/lib/jvm/java-18-openjdk-amd64)
      rm -rf -- /usr/lib/jvm/java-18-openjdk-amd64
      ;;
  esac
  rm -f -- /etc/profile.d/openjdk.sh "${state_dir}/java-runtime-owner"
  if [[ -f /etc/profile ]]; then
    sed -i \
      -e '\|^export JAVA_HOME=/usr/lib/jvm/java-18-openjdk-amd64$|d' \
      -e '\|^export CLASSPATH=.*JAVA_HOME.*$|d' \
      -e '\|^export PATH=.*JAVA_HOME.*/bin:|d' \
      /etc/profile
  fi
}
install_arguments() {
  local option
  option="$(option_for_version)"
  case "${component_kind}" in
    nginx-option) printf '%s\n' --nginx_option "${option}" ;;
    apache) printf '%s\n' --apache --apache_mode_option 1 --apache_mpm_option 1 ;;
    php-option) printf '%s\n' --php_option "${option}" ;;
    jdk-option) printf '%s\n' --jdk_option "${option}" ;;
    tomcat-option)
      local jdk_option=1
      case "${software_version}" in
        11.*|10.*|9.*) jdk_option=3 ;;
      esac
      printf '%s\n' --tomcat_option "${option}" --jdk_option "${jdk_option}"
      ;;
    db-option) printf '%s\n' --db_option "${option}" --dbinstallmethod 1 ;;
    nodejs) printf '%s\n' --nodejs ;;
    pureftpd) printf '%s\n' --pureftpd ;;
    phpmyadmin) printf '%s\n' --phpmyadmin ;;
    memcached) printf '%s\n' --memcached ;;
    redis) printf '%s\n' --redis ;;
    *) die "Unsupported installer adapter kind: ${component_kind}" ;;
  esac
}
patch_database_secret_transport() {
  [[ "${component_kind}" == "db-option" ]] || return 0
  export ONEINSTACK_DATABASE_PASSWORD="${database_password}"
  sed -i \
    's@^dbrootpwd=.*@dbrootpwd="${ONEINSTACK_DATABASE_PASSWORD}"@;
     s@^dbpostgrespwd=.*@dbpostgrespwd="${ONEINSTACK_DATABASE_PASSWORD}"@;
     s@^dbmongopwd=.*@dbmongopwd="${ONEINSTACK_DATABASE_PASSWORD}"@' \
    "${work_dir}/install.sh"
}
secure_database_network() {
  case "${component_id}" in
    postgresql)
      if [[ -f "${data_dir}/postgresql.conf" ]]; then
        sed -i "s@^[#[:space:]]*listen_addresses.*@listen_addresses = '127.0.0.1'@" \
          "${data_dir}/postgresql.conf"
        systemctl restart postgresql.service
      fi
      ;;
    mongodb)
      if [[ -f /etc/mongod.conf ]]; then
        sed -i 's@^[[:space:]]*bindIp:.*@  bindIp: 127.0.0.1@' /etc/mongod.conf
        if ! systemctl restart mongod.service; then
          mongodb_service_diagnostics
          die "MongoDB service restart failed."
        fi
      fi
      ;;
  esac
}
tomcat_ready() {
  pgrep -f "${install_dir}/bin/bootstrap.jar" >/dev/null 2>&1 &&
    curl --silent --show-error --output /dev/null --max-time 2 http://127.0.0.1:8080/
}
configure_caddy_runtime() {
  [[ "${component_id}" == "caddy" ]] || return 0
  local caddyfile="${install_dir}/conf/Caddyfile"
  local fpm_socket="${PHP_FPM_SOCKET:-/dev/shm/php-cgi.sock}"

  # Enable PHP-FPM forwarding when the upstream template still carries the
  # commented placeholder, so PHP applications (phpMyAdmin, etc.) are not
  # served as raw file downloads. Never overwrite an active user setting.
  if [[ -f "${caddyfile}" ]] &&
    ! grep -Eq '^[[:space:]]*php_fastcgi([[:space:]]|$)' "${caddyfile}" &&
    grep -Eq '^[[:space:]]*#[[:space:]]*php_fastcgi' "${caddyfile}" &&
    { [[ -x /usr/local/php/sbin/php-fpm ]] || [[ -S "${fpm_socket}" ]]; }; then
    local tab=$'\t'
    sed -i "s@^[[:space:]]*#[[:space:]]*php_fastcgi.*@${tab}php_fastcgi unix/${fpm_socket}@" \
      "${caddyfile}"
    grep -Eq "^[[:space:]]*php_fastcgi[[:space:]]+unix/${fpm_socket}" "${caddyfile}" ||
      die "Caddy PHP-FPM forwarding could not be enabled in ${caddyfile}"
  fi

  # Caddy runs as its own system user while OneinStack web logs live in a
  # shared www-owned directory. Grant the caddy user write access so a
  # Caddyfile log directive cannot make the service fail to start.
  if getent group www >/dev/null 2>&1; then
    usermod -a -G www caddy 2>/dev/null || true
    install -d -m 0770 -o www -g www -- /data/wwwlogs 2>/dev/null || true
    chmod 0770 /data/wwwlogs 2>/dev/null || true
  fi
}
normalize_runtime_permissions() {
  case "${component_id}" in
    phpmyadmin)
      # Keep the public entrypoint inside OneinStack's shared document root.
      # Adminer uses /data/wwwroot/adminer; phpMyAdmin owns a separate
      # case-sensitive directory at /data/wwwroot/phpMyAdmin.
      local legacy_install_dir
      for legacy_install_dir in "${legacy_install_dirs[@]}"; do
        if [[ -e "${install_dir}" && -e "${legacy_install_dir}" ]]; then
          die "phpMyAdmin has duplicate installations: ${install_dir} and ${legacy_install_dir}"
        fi
        if [[ ! -e "${install_dir}" && -d "${legacy_install_dir}" ]]; then
          mv -- "${legacy_install_dir}" "${install_dir}"
          break
        fi
      done
      [[ -d "${install_dir}" && -f "${install_dir}/index.php" ]] ||
        die "phpMyAdmin public entrypoint is missing: ${install_dir}/index.php"
      phpmyadmin_web_user="$(phpmyadmin_web_user_name || true)"
      [[ -n "${phpmyadmin_web_user}" ]] ||
        die "PHPMA_WEB_USER_MISSING: no supported Web/PHP runtime user exists."
      install -d -m 0755 -o "${phpmyadmin_web_user}" -g "${phpmyadmin_web_user}" -- /data/wwwroot
      chown -R "${phpmyadmin_web_user}:${phpmyadmin_web_user}" -- "${install_dir}"
      ;;

    java)
      configure_java_runtime
      ;;
    tomcat)
      chmod 0755 /usr/lib/jvm
      sed -i \
        's@org.apache.coyote.http11.Http11AprProtocol@org.apache.coyote.http11.Http11NioProtocol@g' \
        "${install_dir}/conf/server.xml"
      systemctl daemon-reload
      systemctl reset-failed "${service_name}.service" 2>/dev/null || true
      systemctl restart "${service_name}.service"
      for _ in $(seq 1 30); do
        tomcat_ready && break
        sleep 1
      done
      tomcat_ready || die "Tomcat process started but HTTP port 8080 did not become ready."
      ;;
    caddy)
      getent group caddy >/dev/null 2>&1 || groupadd --system caddy
      if ! id caddy >/dev/null 2>&1; then
        useradd --system --gid caddy --home-dir /var/lib/caddy \
          --create-home --shell /usr/sbin/nologin caddy
      fi
      install -d -o caddy -g caddy -m 0750 -- /var/lib/caddy
      chmod 0755 "${install_dir}" "${install_dir}/bin" "${install_dir}/conf"
      find "${install_dir}/bin" -maxdepth 1 -type f -exec chmod 0755 {} +
      find "${install_dir}/conf" -type d -exec chmod 0755 {} +
      find "${install_dir}/conf" -type f -exec chmod 0644 {} +
      configure_caddy_runtime
      systemctl daemon-reload
      ;;
    mongodb)
      id mongod >/dev/null 2>&1 || die "MongoDB service user mongod is missing."
      [[ -f /etc/mongod.conf ]] || die "MongoDB configuration file /etc/mongod.conf is missing."
      # The upstream installer creates mongod.conf under root's umask, while
      # the systemd unit runs mongod as the mongod user.
      chown root:mongod /etc/mongod.conf
      chmod 0640 /etc/mongod.conf
      install -d -o mongod -g mongod -m 0750 -- "${data_dir}"
      chown -R mongod:mongod -- "${data_dir}"
      systemctl daemon-reload
      ;;
    redis)
      # The package wrapper uses umask 027. OneinStack copies the Redis
      # executables as root without an explicit mode, which can leave them
      # inaccessible to the redis systemd user.
      chmod 0755 "${install_dir}" "${install_dir}/bin"
      find "${install_dir}/bin" -maxdepth 1 -type f -exec chmod 0755 {} +
      install -d -o redis -g redis -m 0750 -- "${data_dir}"
      if grep -Eq '^[[:space:]]*dir[[:space:]]+' "${install_dir}/etc/redis.conf"; then
        sed -i -E "s|^[[:space:]]*dir[[:space:]].*|dir ${data_dir}|" \
          "${install_dir}/etc/redis.conf"
      else
        printf '\ndir %s\n' "${data_dir}" >>"${install_dir}/etc/redis.conf"
      fi
      chown redis:redis "${install_dir}/etc/redis.conf"
      chmod 0640 "${install_dir}/etc/redis.conf"
      systemctl daemon-reload
      systemctl reset-failed "${service_name}.service" 2>/dev/null || true
      systemctl restart "${service_name}.service"
      ;;
  esac
}
preserve_component_data() {
  case "${component_id}" in
    redis)
      [[ "${preserve_data}" == "true" && -d "${install_dir}/var" ]] || return 0
      install -d -o redis -g redis -m 0750 -- "${data_dir}"
      cp -a -- "${install_dir}/var/." "${data_dir}/"
      chown -R redis:redis "${data_dir}"
      ;;
  esac
}
remove_component_symlinks() {
  local binary link target
  case "${component_id}" in
    phpmyadmin)
      for link in /data/wwwroot/default/phpMyAdmin /data/wwwroot/default/phpmyadmin /data/wwwroot/phpmyadmin; do
        [[ -L "${link}" ]] || continue
        target="$(readlink -- "${link}" 2>/dev/null || true)"
        [[ "${target}" == "${install_dir}" ]] && rm -f -- "${link}"
      done
      ;;
    redis)
      for binary in redis-benchmark redis-check-aof redis-check-rdb redis-cli redis-sentinel redis-server; do
        link="/usr/local/bin/${binary}"
        [[ -L "${link}" ]] || continue
        target="$(readlink -- "${link}" 2>/dev/null || true)"
        [[ "${target}" == "${install_dir}/bin/${binary}" ]] && rm -f -- "${link}"
      done
      ;;
  esac
}
component_version() {
  local output=""
  case "${component_id}" in
    nginx) [[ -x "${install_dir}/sbin/nginx" ]] || return 1; output="$("${install_dir}/sbin/nginx" -v 2>&1)" ;;
    tengine) [[ -x "${install_dir}/sbin/nginx" ]] || return 1; output="$("${install_dir}/sbin/nginx" -v 2>&1)" ;;
    openresty) [[ -x "${install_dir}/nginx/sbin/nginx" ]] || return 1; output="$("${install_dir}/nginx/sbin/nginx" -v 2>&1)" ;;
    caddy) [[ -x "${install_dir}/bin/caddy" ]] || return 1; output="$("${install_dir}/bin/caddy" version 2>&1)" ;;
    apache) [[ -x "${install_dir}/bin/httpd" ]] || return 1; output="$("${install_dir}/bin/httpd" -v 2>&1)" ;;
    php) [[ -x "${install_dir}/bin/php" ]] || return 1; output="$("${install_dir}/bin/php" -v 2>&1)" ;;
    java) command -v java >/dev/null 2>&1 || return 1; output="$(java -version 2>&1)" ;;
    tomcat) [[ -x "${install_dir}/bin/version.sh" ]] || return 1; output="$("${install_dir}/bin/version.sh" 2>&1 | awk -F: '/Server number/{print $2; exit}')" ;;
    mysql|mariadb|percona) [[ -x "${install_dir}/bin/mysql" ]] || return 1; output="$("${install_dir}/bin/mysql" --version 2>&1)" ;;
    postgresql) [[ -x "${install_dir}/bin/psql" ]] || return 1; output="$("${install_dir}/bin/psql" --version 2>&1)" ;;
    mongodb) [[ -x "${install_dir}/bin/mongod" ]] || return 1; output="$("${install_dir}/bin/mongod" --version 2>&1)" ;;
    nodejs) [[ -x "${install_dir}/bin/node" ]] || return 1; output="$("${install_dir}/bin/node" --version 2>&1)" ;;
    pureftpd) [[ -x "${install_dir}/sbin/pure-ftpd" ]] || return 1; output="$("${install_dir}/sbin/pure-ftpd" --help 2>&1 | head -n 1)" ;;
    phpmyadmin) [[ -f "${install_dir}/README" ]] || return 1; output="$(awk '/Version/{print $2; exit}' "${install_dir}/README")" ;;
    memcached) [[ -x "${install_dir}/bin/memcached" ]] || return 1; output="$("${install_dir}/bin/memcached" -h 2>&1 | head -n 1)" ;;
    redis) [[ -x "${install_dir}/bin/redis-server" ]] || return 1; output="$("${install_dir}/bin/redis-server" --version 2>&1)" ;;
    *) return 1 ;;
  esac
  if [[ "${component_id}" =~ ^(mysql|mariadb|percona)$ ]]; then
    printf '%s\n' "${output}" | grep -Eo '(Distrib|Ver) [0-9]+(\.[0-9]+){1,3}([.-][0-9]+)?' |
      awk '{print $2}' | head -n 1
    return
  fi
  if [[ "${component_id}" == "caddy" ]]; then
    # Caddy's version output may include build metadata or other numeric
    # values. Only accept a semantic version so timestamps cannot pass as the
    # installed runtime version.
    printf '%s\n' "${output}" | grep -Eo 'v?[0-9]+\.[0-9]+\.[0-9]+([.-][0-9]+)?' |
      awk 'NR == 1 { sub(/^v/, ""); print }'
    return
  fi
  printf '%s\n' "${output}" | grep -Eo '[0-9]+(\.[0-9]+){0,3}([.-][0-9]+)?' | head -n 1
}
version_matches() {
  local actual
  actual="$(component_version 2>/dev/null || true)"
  if [[ "${component_id}" == "java" && "${software_version}" == "8" ]]; then
    [[ "${actual}" == 1.8* || "${actual}" == 8* ]]
    return
  fi
  [[ "${actual}" == "${software_version}"* ]]
}
service_active() {
  local unit
  [[ -z "${service_name}" ]] && return 0
  unit="$(resolve_service_unit active 2>/dev/null || true)"
  [[ -n "${unit}" ]] || unit="${service_name}.service"
  systemctl is-active --quiet "${unit}" || return 1
  if [[ "${component_id}" == "tomcat" ]]; then
    tomcat_ready
  fi
}
disable_service() {
  [[ -z "${service_name}" ]] && return 0
  [[ "${legacy_migrated_active:-false}" == "true" ||
    "${legacy_migrated_enabled:-false}" == "true" ]] && return 0
  stop_service
  systemctl disable "${service_name}.service" 2>/dev/null || true
}
verify_installed() {
  version_matches || return 1
  [[ "${component_id}" == "phpmyadmin" ]] || return 0
  [[ -f "${install_dir}/index.php" ]] || return 1
  phpmyadmin_fpm_ready || return 1
  phpmyadmin_route_ready || return 1
  phpmyadmin_http_ready
}
verify_component() { verify_installed && service_active; }
snapshot_existing_component() {
  local migration_root="${state_dir}/migration"
  local existing_install_dir
  existing_install_dir="$(runtime_install_dir || true)"
  if [[ -f "${migration_root}/new-install" &&
    ! -f "${migration_root}/external-detected" &&
    ! -f "${state_dir}/installed.json" &&
    -z "${existing_install_dir}" ]]; then
    rm -rf -- "${migration_root}"
    emit_progress 10 migration.stale_new_install.cleaned "${component_name} stale first-install state cleaned"
  fi
  if [[ -d "${migration_root}" ]] && [[ -n "$(find "${migration_root}" -mindepth 1 -print -quit)" ]]; then
    die "PHPMA_MIGRATION_PENDING: previous phpMyAdmin installation recovery is incomplete."
  fi
  install -d -m 0700 -- "${migration_root}"
  if [[ -z "${existing_install_dir}" ]]; then
    printf '%s\n' "${install_dir}" >"${migration_root}/install-path"
    : >"${migration_root}/new-install"
    return 0
  fi
  if [[ -n "${service_name}" ]]; then
    systemctl is-active --quiet "${service_name}.service" 2>/dev/null && : >"${migration_root}/was-active" || true
    systemctl is-enabled --quiet "${service_name}.service" 2>/dev/null && : >"${migration_root}/was-enabled" || true
    systemctl stop "${service_name}.service" 2>/dev/null || true
    [[ ! -f "/etc/systemd/system/${service_name}.service" ]] ||
      cp -a -- "/etc/systemd/system/${service_name}.service" "${migration_root}/service.unit"
  fi
  printf '%s\n' "${existing_install_dir}" >"${migration_root}/install-path"
  mv -- "${existing_install_dir}" "${migration_root}/install"
  : >"${migration_root}/external-detected"
  emit_progress 20 migration.snapshot.created "${component_name} existing installation snapshot created"
}
restore_existing_component() {
  local migration_root="${state_dir}/migration"
  if [[ -f "${migration_root}/new-install" ]]; then
    stop_service
    remove_component_symlinks
    rm -rf -- "${install_dir}" "${legacy_install_dirs[@]}"
    rm -rf -- "${migration_root}"
    rm -f -- "${state_dir}/installed.json"
    rmdir "${state_dir}" 2>/dev/null || true
    emit_progress 100 rollback.install.cleaned "${component_name} failed first installation cleaned"
    return 0
  fi
  [[ -f "${migration_root}/external-detected" ]] || return 0
  stop_service
  remove_component_symlinks
  local original_install_dir="${install_dir}"
  if [[ -s "${migration_root}/install-path" ]]; then
    IFS= read -r original_install_dir <"${migration_root}/install-path"
  fi
  rm -rf -- "${install_dir}" "${legacy_install_dirs[@]}"
  [[ ! -d "${migration_root}/install" ]] || mv -- "${migration_root}/install" "${original_install_dir}"
  if [[ -n "${service_name}" && -f "${migration_root}/service.unit" ]]; then
    cp -a -- "${migration_root}/service.unit" "/etc/systemd/system/${service_name}.service"
    systemctl daemon-reload
    [[ -f "${migration_root}/was-enabled" ]] && systemctl enable "${service_name}.service" 2>/dev/null || true
    [[ -f "${migration_root}/was-active" ]] && systemctl start "${service_name}.service" 2>/dev/null || true
  fi
  rm -rf -- "${migration_root}"
  rm -f -- "${state_dir}/installed.json"
  emit_progress 100 rollback.service.restored "${component_name} previous installation restored"
}
commit_existing_component() {
  local migration_root="${state_dir}/migration"
  if [[ -f "${migration_root}/new-install" ]]; then
    rm -rf -- "${migration_root}"
    emit_progress 95 migration.commit.completed "${component_name} new installation committed"
    return 0
  fi
  [[ -f "${migration_root}/external-detected" ]] || return 0
  rm -rf -- "${migration_root}"
  emit_progress 95 migration.commit.completed "${component_name} previous installation replacement committed"
}
write_state() {
  if [[ "${component_id}" == "phpmyadmin" ]]; then
    phpmyadmin_write_state
    return 0
  fi
  local actual
  actual="$(component_version)"
  install -d -m 0750 -- "${state_dir}"
  printf '{"component":"%s","softwareVersion":"%s","runtimeVersion":"%s","upstreamCommit":"%s"}\n' \
    "${component_id}" "${software_version}" "${actual}" "${upstream_commit}" \
    >"${state_dir}/installed.json"
  chmod 0640 "${state_dir}/installed.json"
}
stop_service() {
  [[ -z "${service_name}" ]] || systemctl stop "${service_name}.service" 2>/dev/null || true
}
