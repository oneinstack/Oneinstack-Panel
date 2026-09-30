#!/usr/bin/env bash
# shellcheck disable=SC1091,SC2034,SC2155,SC2317
set -Eeuo pipefail
umask 027

component_id="openresty"
package_version="1.0.13"
software_version="${SOFTWARE_VERSION:-1.31.1.1}"
openresty_port="${OPENRESTY_PORT:-80}"
install_dir="${INSTALL_DIR:-/usr/local/openresty}"
web_root="${WEB_ROOT:-/data/wwwroot}"
log_dir="${LOG_DIR:-/data/wwwlogs}"
run_user="${RUN_USER:-www}"
run_group="${RUN_GROUP:-www}"
php_fpm_socket="${PHP_FPM_SOCKET:-/dev/shm/php-cgi.sock}"
web_vhost_root="${WEB_VHOST_ROOT:-/usr/local/one/vhost}"
install_mode="${ONEINSTACK_INSTALL_MODE:-center}"
offline_package_path="${ONEINSTACK_OFFLINE_PACKAGE_PATH:-}"
source_url=""
source_signature_url="${source_url}.asc"
source_archive=""
source_sha256=""
package_manager=""
script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
component_dir="$(cd -- "${script_dir}/.." && pwd)"
openresty_release_signing_key_file="${component_dir}/keys/openresty/release.key"
openresty_release_signing_fingerprint="25451EB088460026195BD62CB550E09EA0E98066"

state_root="${ONEINSTACK_COMPONENT_STATE:-/var/lib/oneinstack/components}"
state_dir="${state_root}/${component_id}"
web_server_migration_root="${ONEINSTACK_WEB_SERVER_MIGRATION_ROOT:-/var/lib/oneinstack/web-server-migration}"
rollback_dir="${state_dir}/rollback"
managed_path_acl_file="${state_dir}/managed-path-acl"
transaction_path_acl_file="${rollback_dir}/path-acl-added"
path_acl_transaction_file=""
service_name="oneinstack-openresty"
unit_file="/etc/systemd/system/${service_name}.service"
openresty_binary="${install_dir}/nginx/sbin/nginx"
openresty_pid_file="${install_dir}/nginx/logs/nginx.pid"
external_migration_dir="${rollback_dir}/external"
legacy_service_name="nginx"
install_parameters_file="${state_dir}/install-parameters"

die() { echo "ERROR: $*" >&2; exit 1; }
emit_progress() {
  local percent="$1" code="$2" message="$3" fd="${ONEINSTACK_PROGRESS_FD:-}"
  [[ "${fd}" =~ ^[0-9]+$ ]] || return 0
  message="${message//\\/\\\\}"; message="${message//\"/\\\"}"; message="${message//$'\n'/ }"
  printf '{"type":"progress","percent":%s,"code":"%s","message":"%s"}\n' \
    "${percent}" "${code}" "${message}" 1>&"${fd}" 2>/dev/null || true
}
require_root() { [[ "$(id -u)" -eq 0 ]] || die "This action must run as root."; }
require_command() { command -v "$1" >/dev/null 2>&1 || die "Required command not found: $1"; }
validate_identifier() { [[ "$1" =~ ^[a-z_][a-z0-9_-]{0,30}$ ]] || die "Invalid account identifier: $1"; }
normalize_web_root_parameter() {
  local value="${1%/}"
  if [[ "${value}" =~ ^/data/wwwroot(/default)+$ ]]; then
    printf '/data/wwwroot'
  else
    printf '%s' "${value}"
  fi
}
validate_path() {
  local value="$1" label="$2"
  [[ "${value}" == /* && "$(realpath -m -- "${value}")" == "${value}" ]] || die "${label} must be a normalized absolute path."
  [[ "${value}" =~ ^/[A-Za-z0-9._@%+=:,~-]+(/[A-Za-z0-9._@%+=:,~-]+)*$ ]] || die "${label} contains unsupported path characters."
  case "${value}" in /|/usr|/usr/local|/etc|/var|/data|/home|/root) die "${label} is too broad: ${value}" ;; esac
}
validate_inputs() {
  case "${software_version}" in
    1.27.1.2) source_sha256="74f076f7e364b2a99a6c5f9bb531c27610c78985abe956b442b192a2295f7548" ;;
    1.31.1.1) source_sha256="65b78baadd3f0984055de89bf13f4a1932e5bfe9c31932037a134ea2b1a0ce42" ;;
    *) die "Unsupported OpenResty version: ${software_version}; select a Center-published exact release." ;;
  esac
  source_url="https://openresty.org/download/openresty-${software_version}.tar.gz"
  source_signature_url="${source_url}.asc"
  source_archive="openresty-${software_version}.tar.gz"
  [[ "${install_mode}" == "center" || "${install_mode}" == "offline" ]] || die "ONEINSTACK_INSTALL_MODE must be center or offline."
  if [[ "${install_mode}" == "offline" ]]; then
    [[ -n "${offline_package_path}" && "${offline_package_path}" == /* &&
      "$(realpath -m -- "${offline_package_path}")" == "${offline_package_path}" ]] ||
      die "Offline installation requires a normalized absolute Bundle path."
  elif [[ -n "${offline_package_path}" ]]; then
    die "Offline Bundle path cannot be used in Center mode."
  fi
  [[ "${openresty_port}" =~ ^[0-9]+$ && "${openresty_port}" -ge 1 && "${openresty_port}" -le 65535 ]] || die "OPENRESTY_PORT must be a valid TCP port."
  validate_identifier "${run_user}"; validate_identifier "${run_group}"
  validate_path "${install_dir}" INSTALL_DIR; validate_path "${web_root}" WEB_ROOT
  validate_path "${log_dir}" LOG_DIR; validate_path "${php_fpm_socket}" PHP_FPM_SOCKET
  validate_path "${web_vhost_root}" WEB_VHOST_ROOT
  validate_path "${state_root}" ONEINSTACK_COMPONENT_STATE
  validate_path "${web_server_migration_root}" ONEINSTACK_WEB_SERVER_MIGRATION_ROOT
}
managed_openresty_version() {
  if [[ -r "${state_dir}/version" ]]; then
    head -n1 "${state_dir}/version"
    return 0
  fi
  if [[ -r "${state_dir}/installed.json" ]]; then
    sed -nE 's/.*"softwareVersion":"([^"]+)".*/\1/p' "${state_dir}/installed.json" | head -n1
    return 0
  fi
  return 1
}
managed_openresty_state_exists() {
  [[ -r "${state_dir}/version" || -r "${state_dir}/installed.json" ]]
}
managed_openresty_owns_configured_port() {
  managed_openresty_state_exists && openresty_is_running || return 1
  local configured_port
  configured_port="$(configured_default_site_port "${install_dir}/nginx/conf/conf.d/default.conf" || true)"
  [[ -n "${configured_port}" && "${configured_port}" == "${openresty_port}" ]]
}
validate_upgrade_path() {
  local installed_version installed_install_dir=""
  installed_version="$(managed_openresty_version || true)"
  [[ -n "${installed_version}" ]] || return 0
  if [[ -r "${install_parameters_file}" ]]; then
    installed_install_dir="$(sed -n 's/^INSTALL_DIR=//p' "${install_parameters_file}" | head -n1)"
  fi
  [[ -z "${installed_install_dir}" || "${installed_install_dir}" == "${install_dir}" ]] ||
    die "INSTALL_DIR cannot be migrated in place: ${installed_install_dir} -> ${install_dir}."
  case "${installed_version}:${software_version}" in
    1.27.1.2:1.27.1.2|1.27.1.2:1.31.1.1|1.31.1.1:1.31.1.1) ;;
    1.31.1.1:1.27.1.2) die "OpenResty downgrade from 1.31.1.1 to 1.27.1.2 is not supported." ;;
    *) die "Unsupported managed OpenResty version transition: ${installed_version:-unknown} -> ${software_version}." ;;
  esac
}
ensure_install_port_available() {
  require_command ss
  local listener listener_owner
  listener="$(ss -H -ltnp "sport = :${openresty_port}" 2>/dev/null || true)"
  [[ -n "${listener}" ]] || return 0
  if managed_openresty_owns_configured_port; then
    return 0
  fi
  listener_owner="$(printf '%s\n' "${listener}" | sed -nE 's/.*users:\\(\\("([^"]+)".*/\\1/p' | head -n1)"
  [[ -n "${listener_owner}" ]] || listener_owner="another process"
  die "Port ${openresty_port} is already occupied by ${listener_owner}; choose a free port."
}
persist_install_parameters() {
  install -d -m 0750 -- "${state_dir}"
  local temporary
  temporary="$(mktemp "${state_dir}/.install-parameters.XXXXXX")"
  {
    printf 'OPENRESTY_PORT=%s\n' "${openresty_port}"
    printf 'INSTALL_DIR=%s\n' "${install_dir}"
    printf 'WEB_ROOT=%s\n' "${web_root}"
    printf 'LOG_DIR=%s\n' "${log_dir}"
    printf 'WEB_VHOST_ROOT=%s\n' "${web_vhost_root}"
    printf 'RUN_USER=%s\n' "${run_user}"
    printf 'RUN_GROUP=%s\n' "${run_group}"
    printf 'PHP_FPM_SOCKET=%s\n' "${php_fpm_socket}"
  } >"${temporary}"
  chmod 0600 "${temporary}"
  mv -f -- "${temporary}" "${install_parameters_file}"
}
load_install_parameters() {
  [[ -r "${install_parameters_file}" ]] || return 0
  local requested_openresty_port="${openresty_port}" requested_install_dir="${install_dir}"
  local requested_web_root="${web_root}" requested_log_dir="${log_dir}"
  local requested_web_vhost_root="${web_vhost_root}" requested_run_user="${run_user}" requested_run_group="${run_group}"
  local requested_php_fpm_socket="${php_fpm_socket}"
  local key value
  while IFS='=' read -r key value; do
    case "${key}" in
      OPENRESTY_PORT) openresty_port="${value}" ;;
      INSTALL_DIR) install_dir="${value}" ;;
      WEB_ROOT) web_root="${value}" ;;
      LOG_DIR) log_dir="${value}" ;;
      WEB_VHOST_ROOT) web_vhost_root="${value}" ;;
      RUN_USER) run_user="${value}" ;;
      RUN_GROUP) run_group="${value}" ;;
      PHP_FPM_SOCKET) php_fpm_socket="${value}" ;;
    esac
  done <"${install_parameters_file}"
  local marker
  for key in OPENRESTY_PORT INSTALL_DIR WEB_ROOT LOG_DIR WEB_VHOST_ROOT RUN_USER RUN_GROUP PHP_FPM_SOCKET; do
    marker="ONEINSTACK_PARAMETER_${key}_EXPLICIT"
    [[ "${!marker:-false}" == "true" ]] || continue
    case "${key}" in
      OPENRESTY_PORT) openresty_port="${requested_openresty_port}" ;;
      INSTALL_DIR) install_dir="${requested_install_dir}" ;;
      WEB_ROOT) web_root="${requested_web_root}" ;;
      LOG_DIR) log_dir="${requested_log_dir}" ;;
      WEB_VHOST_ROOT) web_vhost_root="${requested_web_vhost_root}" ;;
      RUN_USER) run_user="${requested_run_user}" ;;
      RUN_GROUP) run_group="${requested_run_group}" ;;
      PHP_FPM_SOCKET) php_fpm_socket="${requested_php_fpm_socket}" ;;
    esac
  done
  web_root="$(normalize_web_root_parameter "${web_root}")"
  openresty_binary="${install_dir}/nginx/sbin/nginx"
  openresty_pid_file="${install_dir}/nginx/logs/nginx.pid"
}
configured_default_site_port() {
  local site_config_file="$1"
  [[ -f "${site_config_file}" ]] || return 0
  sed -nE 's/^[[:space:]]*listen[[:space:]]+([0-9]+)[[:space:]]+default_server.*;/\1/p' \
    "${site_config_file}" | head -n1
}
rewrite_default_site_port() {
  local site_config_file="$1" target_port="$2"
  [[ -f "${site_config_file}" ]] || return 0
  sed -Ei \
    "s|^([[:space:]]*listen[[:space:]]+)[0-9]+([[:space:]]+default_server.*;)|\\1${target_port}\\2|" \
    "${site_config_file}"
}
reconcile_default_site_port() {
  local site_config_file="${install_dir}/nginx/conf/conf.d/default.conf" detected_port
  detected_port="$(configured_default_site_port "${site_config_file}" || true)"
  if [[ "${ONEINSTACK_PARAMETER_OPENRESTY_PORT_EXPLICIT:-false}" == "true" ]]; then
    [[ -z "${detected_port}" || "${detected_port}" == "${openresty_port}" ]] ||
      rewrite_default_site_port "${site_config_file}" "${openresty_port}"
    return 0
  fi
  [[ -z "${detected_port}" ]] || openresty_port="${detected_port}"
}
ensure_openresty_managed_includes() {
  local config_file="${install_dir}/nginx/conf/nginx.conf"
  local add_conf_d=true add_vhost=true candidate
  grep -Eq '^[[:space:]]*include[[:space:]]+([^;[:space:]]*/)?conf\.d/\*\.conf[[:space:]]*;' "${config_file}" && add_conf_d=false
  grep -Fq "include ${web_vhost_root}/*.conf;" "${config_file}" && add_vhost=false
  [[ "${add_conf_d}" == true || "${add_vhost}" == true ]] || return 0
  candidate="$(mktemp "$(dirname -- "${config_file}")/.oneinstack-openresty-includes.XXXXXX")"
  if ! awk -v add_conf_d="${add_conf_d}" -v add_vhost="${add_vhost}" \
    -v conf_d="    include conf.d/*.conf;" -v vhost="    include ${web_vhost_root}/*.conf;" '
      !inserted && $0 ~ /^[[:space:]]*http[[:space:]]*\{/ {
        print
        if (add_conf_d == "true") print conf_d
        if (add_vhost == "true") print vhost
        inserted=1
        next
      }
      { print }
      END { if (!inserted) exit 65 }
    ' "${config_file}" >"${candidate}"; then
    rm -f -- "${candidate}"
    die "OpenResty main configuration does not contain a manageable http block."
  fi
  chmod --reference="${config_file}" "${candidate}"
  chown --reference="${config_file}" "${candidate}"
  mv -f -- "${candidate}" "${config_file}"
}
check_host() {
  [[ -r /etc/os-release ]] || die "/etc/os-release is unavailable."
  source /etc/os-release
  os_id="${ID:-}"
  os_release_name="${NAME:-} ${PRETTY_NAME:-}"
  os_version="${VERSION_ID:-}"
  os_major="${os_version%%.*}"
  case "${os_id}" in
    ubuntu)
      case "${os_version}" in 22.04|24.04|26.04) os_family="debian" ;; *) die "Unsupported Ubuntu release: ${os_version}" ;; esac ;;
    debian)
      case "${os_major}" in 11|12|13) os_family="debian" ;; *) die "Unsupported Debian release: ${os_version}" ;; esac ;;
    centos)
      case "${os_major}" in 7|8|9|10) os_family="rhel" ;; *) die "Unsupported CentOS release: ${os_version}" ;; esac
      ;;
    centos-stream)
      case "${os_major}" in 8|9|10) os_family="rhel" ;; *) die "Unsupported CentOS Stream release: ${os_version}" ;; esac
      ;;
    rhel|rocky|almalinux|ol)
      case "${os_major}" in 8|9|10) os_family="rhel" ;; *) die "Unsupported RHEL-family release: ${os_id} ${os_version}" ;; esac ;;
    fedora) os_family="rhel" ;;
    amzn)
      [[ "${os_major}" == "2023" ]] || die "Unsupported Amazon Linux release: ${os_version}"
      os_family="rhel"
      ;;
    sles)
      [[ "${os_major}" == "15" || "${os_major}" == "16" ]] || die "Unsupported SLES release: ${os_version}"
      os_family="suse"
      ;;
    opensuse-leap|opensuse-tumbleweed|opensuse) os_family="suse" ;;
    *) die "Unsupported Linux distribution: ${os_id:-unknown} ${os_version:-unknown}" ;;
  esac
  case "$(uname -m)" in
    x86_64) host_architecture="amd64" ;;
    aarch64|arm64) host_architecture="arm64" ;;
    *) die "Only amd64 and arm64 are supported by this package." ;;
  esac
  case "${os_family}" in
    debian) package_manager="apt" ;;
    rhel) command -v dnf >/dev/null 2>&1 && package_manager="dnf" || package_manager="yum" ;;
    suse) package_manager="zypper" ;;
  esac
}
install_dependencies() {
	if [[ "${install_mode}" == "offline" ]]; then
		install_dependencies_offline
		return
	fi
	case "${os_family}" in
    debian)
      export DEBIAN_FRONTEND=noninteractive
      apt-get update
      apt-get install -y --no-install-recommends \
        acl build-essential patch ca-certificates curl iproute2 libpcre2-dev libreadline-dev libssl-dev perl tar zlib1g-dev
      ;;
    rhel)
      local package_manager="dnf"
      command -v "${package_manager}" >/dev/null 2>&1 || package_manager="yum"
      local pcre_devel="pcre2-devel"
      local openssl_devel="openssl-devel"
      if [[ "${os_id}" == "centos" && "${os_major}" == "7" ]]; then
        pcre_devel="pcre-devel"
        openssl_devel="openssl11-devel"
      fi
      "${package_manager}" install -y \
        acl gcc gcc-c++ make patch ca-certificates curl iproute "${pcre_devel}" readline-devel "${openssl_devel}" perl tar gzip zlib-devel
      ;;
    suse)
      zypper --non-interactive refresh
      zypper --non-interactive install --no-recommends \
        acl gcc gcc-c++ make patch ca-certificates curl iproute2 libpcre2-devel readline-devel libopenssl-devel perl tar gzip zlib-devel
      ;;
	  esac
}
offline_package_dir() {
	[[ -n "${offline_package_path}" ]] || die "Offline installation requires ONEINSTACK_OFFLINE_PACKAGE_PATH."
	printf '%s/packages/%s/%s/%s\n' "${offline_package_path}" "${os_id}" "${os_version}" "${host_architecture}"
}
validate_offline_bundle() {
  local package_dir
  [[ -d "${offline_package_path}" ]] || die "Offline OpenResty Bundle is unavailable."
  [[ -f "${offline_package_path}/manifest.yaml" ]] || die "Offline Bundle manifest is missing."
  [[ -f "${offline_package_path}/files.sha256" ]] || die "Offline Bundle checksum file is missing."
  [[ -f "${offline_package_path}/bundle-info" ]] || die "Offline Bundle metadata is missing."
  grep -Fxq "component=openresty" "${offline_package_path}/bundle-info" || die "Offline Bundle component metadata is not OpenResty."
  grep -Fxq "packageVersion=1.0.13" "${offline_package_path}/bundle-info" || die "Offline Bundle package version metadata does not match OpenResty 1.0.13."
  grep -Fxq "softwareVersion=${software_version}" "${offline_package_path}/bundle-info" || die "Offline Bundle software version metadata does not match OpenResty ${software_version}."
  grep -Fxq "osId=${os_id}" "${offline_package_path}/bundle-info" || die "Offline Bundle OS metadata does not match this host."
  grep -Fxq "osVersion=${os_version}" "${offline_package_path}/bundle-info" || die "Offline Bundle OS version metadata does not match this host."
  grep -Fxq "architecture=${host_architecture}" "${offline_package_path}/bundle-info" || die "Offline Bundle architecture metadata does not match this host."
  package_dir="$(offline_package_dir)"
  [[ -d "${package_dir}" ]] || die "Offline OpenResty dependencies are missing for this host."
  [[ -f "${offline_package_path}/artifacts/${host_architecture}/${source_archive}" ]] || die "Offline OpenResty source artifact is missing."
  [[ -f "${offline_package_path}/artifacts/${host_architecture}/${source_archive}.asc" ]] || die "Offline OpenResty source signature is missing."
  [[ -f "${offline_package_path}/keys/openresty/release.key" ]] || die "Offline OpenResty signing key is missing."
  for required_script in precheck.sh install.sh configure.sh verify.sh status.sh start.sh stop.sh restart.sh reload.sh rollback.sh uninstall.sh config.sh; do
    [[ -x "${offline_package_path}/scripts/${required_script}" ]] || die "Offline OpenResty lifecycle script is missing: ${required_script}"
  done
  find "${package_dir}" -maxdepth 1 -type f \( -name '*.deb' -o -name '*.rpm' \) -print -quit | grep -q . || die "Offline OpenResty dependency packages are missing."
  (cd "${offline_package_path}" && sha256sum -c files.sha256 --status) || die "Offline Bundle checksum verification failed."
  grep -Eq '^[[:space:]]+id:[[:space:]]+openresty[[:space:]]*$' "${offline_package_path}/manifest.yaml" || die "Offline Bundle component is not OpenResty."
  grep -Eq "^[[:space:]]+version:[[:space:]]+1\\.0\\.12[[:space:]]*$" "${offline_package_path}/manifest.yaml" || die "Offline Bundle package version does not match OpenResty 1.0.13."
}
install_dependencies_offline() {
	local package_dir
	local -a packages=()
	package_dir="$(offline_package_dir)"
	mapfile -t packages < <(find "${package_dir}" -maxdepth 1 -type f \( -name '*.deb' -o -name '*.rpm' \) -print | sort)
	((${#packages[@]} > 0)) || die "Offline OpenResty dependency packages are missing."
	case "${package_manager}" in
		apt) DEBIAN_FRONTEND=noninteractive dpkg -i "${packages[@]}" || apt-get -y --no-download -f install ;;
		dnf|yum) "${package_manager}" --disablerepo='*' --cacheonly install -y "${packages[@]}" ;;
		zypper) zypper --non-interactive --no-refresh --no-gpg-checks install --allow-unsigned-rpm "${packages[@]}" ;;
	esac
}
ensure_account() {
  getent group "${run_group}" >/dev/null || groupadd --system "${run_group}"
  id "${run_user}" >/dev/null 2>&1 || useradd --system --gid "${run_group}" --home-dir /nonexistent --shell /usr/sbin/nologin "${run_user}"
}
record_managed_path_acl() {
  local acl_user="$1" acl_path="$2" record
  record="${acl_user}"$'\t'"${acl_path}"$'\t--x'
  install -d -m 0750 -- "${state_dir}"
  if [[ ! -f "${managed_path_acl_file}" ]] || ! grep -Fqx -- "${record}" "${managed_path_acl_file}"; then
    printf '%s\n' "${record}" >>"${managed_path_acl_file}"
    chmod 0600 "${managed_path_acl_file}"
  fi
  if [[ -n "${path_acl_transaction_file}" && -f "${path_acl_transaction_file}" ]] &&
    ! grep -Fqx -- "${record}" "${path_acl_transaction_file}"; then
    printf '%s\n' "${record}" >>"${path_acl_transaction_file}"
    chmod 0600 "${path_acl_transaction_file}"
  fi
  if [[ -f "${transaction_path_acl_file}" ]] &&
    ! grep -Fqx -- "${record}" "${transaction_path_acl_file}"; then
    printf '%s\n' "${record}" >>"${transaction_path_acl_file}"
    chmod 0600 "${transaction_path_acl_file}"
  fi
}
ensure_runtime_path_traversal() {
  local acl_user="$1" managed_path="$2" description="$3"
  local current_path index current_acl access_mask existing_line effective_permissions
  local -a path_chain=()
  current_path="${managed_path%/}"
  while [[ "${current_path}" != "/" ]]; do
    path_chain+=("${current_path}")
    current_path="$(dirname -- "${current_path}")"
  done
  for ((index=${#path_chain[@]}; index > 0; index--)); do
    current_path="${path_chain[index - 1]}"
    [[ -d "${current_path}" && ! -L "${current_path}" ]] ||
      die "${description} contains a missing or symbolic-link directory: ${current_path}."
    if runuser -u "${acl_user}" -- test -x "${current_path}"; then
      continue
    fi
    require_command getfacl
    require_command setfacl
    current_acl="$(getfacl -cp -- "${current_path}")" ||
      die "Unable to inspect the ACL for ${current_path}."
    existing_line="$(awk -F: -v user="${acl_user}" '$1 == "user" && $2 == user {print; exit}' <<<"${current_acl}")"
    if [[ -n "${existing_line}" ]]; then
      effective_permissions="$(awk -F'#effective:' '{print $2}' <<<"${existing_line}" | tr -d '[:space:]')"
      [[ -n "${effective_permissions}" ]] || effective_permissions="$(awk -F: '{print $3}' <<<"${existing_line}" | tr -d '[:space:]')"
      [[ "${effective_permissions}" == *x* ]] ||
        die "Existing ACL entry for ${acl_user} on ${current_path} does not permit traversal and was preserved."
      die "${acl_user} still cannot traverse ${current_path} despite its existing ACL entry; refusing to overwrite it."
    fi
    access_mask="$(awk -F: '$1 == "mask" && $2 == "" {print $3; exit}' <<<"${current_acl}")"
    if [[ -n "${access_mask}" ]]; then
      [[ "${access_mask}" == *x* ]] ||
        die "Existing ACL mask on ${current_path} denies traversal; refusing to broaden unrelated ACL access."
      setfacl --no-mask -m "u:${acl_user}:--x" -- "${current_path}" ||
        die "Unable to grant ${acl_user} traverse access to ${current_path}."
    else
      setfacl -m "u:${acl_user}:--x" -- "${current_path}" ||
        die "Unable to grant ${acl_user} traverse access to ${current_path}."
    fi
    record_managed_path_acl "${acl_user}" "${current_path}"
    runuser -u "${acl_user}" -- test -x "${current_path}" ||
      die "${acl_user} still cannot traverse ${current_path} after applying the managed ACL."
  done
}
normalize_runtime_permissions() {
  ensure_account
  install -d -m 0755 -- "${web_root}" "${web_root}/default"
  install -d -m 0750 -- "${log_dir}"
  chown "${run_user}:${run_group}" "${web_root}" "${web_root}/default" "${log_dir}"
  chmod 0755 "${web_root}" "${web_root}/default"
  chmod 0750 "${log_dir}"
  chmod 0755 "${install_dir}" "${install_dir}/nginx/sbin" "${openresty_binary}"
  ensure_runtime_path_traversal "${run_user}" "${web_root}" "WEB_ROOT"
  ensure_runtime_path_traversal "${run_user}" "${log_dir}" "LOG_DIR"
  emit_progress 20 permissions.runtime.applied "OpenResty web and log directory permissions applied"
}
verify_runtime_permissions() {
  require_command runuser
  runuser -u "${run_user}" -- test -x "${web_root}" ||
    die "OpenResty worker user cannot traverse the managed web root."
  runuser -u "${run_user}" -- test -x "${web_root}/default" ||
    die "OpenResty worker user cannot traverse the default web root."
  runuser -u "${run_user}" -- test -r "${web_root}/default" ||
    die "OpenResty worker user cannot read the default web root."
  runuser -u "${run_user}" -- test -w "${log_dir}" ||
    die "OpenResty worker user cannot write the managed log directory."
  emit_progress 25 permissions.runtime.verified "OpenResty runtime access verified as ${run_user}"
}
remove_managed_path_acl_entries() {
  local records_file="$1" acl_user acl_path expected_permissions current_permissions
  [[ -f "${records_file}" ]] || return 0
  if ! command -v getfacl >/dev/null 2>&1 || ! command -v setfacl >/dev/null 2>&1; then
    printf 'WARNING: managed OpenResty path ACL could not be restored because ACL utilities are unavailable.\n' >&2
    return 0
  fi
  while IFS=$'\t' read -r acl_user acl_path expected_permissions; do
    [[ "${acl_user}" =~ ^[a-z_][a-z0-9_-]{0,30}$ && "${acl_path}" == /* && "${acl_path}" != "/" && -d "${acl_path}" ]] || continue
    expected_permissions="${expected_permissions:---x}"
    current_permissions="$(getfacl -cp -- "${acl_path}" 2>/dev/null | awk -F: -v user="${acl_user}" '$1 == "user" && $2 == user {print $3; exit}' || true)"
    if [[ "${current_permissions}" == "${expected_permissions}" ]]; then
      setfacl --no-mask -x "u:${acl_user}" -- "${acl_path}" ||
        printf 'WARNING: managed OpenResty path ACL could not be restored for %s.\n' "${acl_path}" >&2
    elif [[ -n "${current_permissions}" ]]; then
      printf 'WARNING: managed OpenResty path ACL for %s was changed externally and was preserved.\n' "${acl_path}" >&2
    fi
  done <"${records_file}"
}
restore_transaction_path_acl() {
  local records_file="$1" temporary record
  [[ -f "${records_file}" ]] || return 0
  remove_managed_path_acl_entries "${records_file}"
  if [[ -f "${managed_path_acl_file}" ]]; then
    temporary="$(mktemp "${state_dir}/.managed-path-acl.XXXXXX")"
    while IFS= read -r record; do
      grep -Fqx -- "${record}" "${records_file}" || printf '%s\n' "${record}" >>"${temporary}"
    done <"${managed_path_acl_file}"
    if [[ -s "${temporary}" ]]; then
      chmod 0600 "${temporary}"
      mv -f -- "${temporary}" "${managed_path_acl_file}"
    else
      rm -f -- "${temporary}" "${managed_path_acl_file}"
    fi
  fi
  rm -f -- "${records_file}"
}
download_verified() {
  local destination="$1" signature key_file gpg_home fingerprints verification_status gpg_import_output
  local fingerprint marker status signing_fingerprint created timestamp expire version reserved pubkey hash class primary_fingerprint rest
  local gpg_command="gpg"
  command -v "${gpg_command}" >/dev/null 2>&1 || gpg_command="gpg2"
  require_command "${gpg_command}"
  if [[ "${install_mode}" == "offline" ]]; then
    local offline_source="${offline_package_path}/artifacts/${host_architecture}/${source_archive}"
    cp -- "${offline_source}" "${destination}"
    signature="${destination}.asc"
    cp -- "${offline_source}.asc" "${signature}"
    key_file="${offline_package_path}/keys/openresty/release.key"
  else
    curl --proto '=https' --tlsv1.2 --fail --location --retry 3 --connect-timeout 20 --output "${destination}" "${source_url}"
    signature="${destination}.asc"
    curl --proto '=https' --tlsv1.2 --fail --location --retry 3 --connect-timeout 20 --output "${signature}" "${source_signature_url}"
    key_file="${openresty_release_signing_key_file}"
    [[ -f "${key_file}" ]] || die "Bundled OpenResty release signing key is missing."
  fi
  printf '%s  %s\n' "${source_sha256}" "${destination}" | sha256sum --check --status || die "OpenResty source checksum verification failed."
  gpg_home="$(mktemp -d "$(dirname -- "${destination}")/.gnupg.XXXXXX")"
  chmod 0700 "${gpg_home}"
  if ! gpg_import_output="$("${gpg_command}" --batch --homedir "${gpg_home}" --import "${key_file}" 2>&1)"; then
    if grep -qi "no user ID" <<<"${gpg_import_output}"; then
      die "OpenResty release signing key has no importable user ID for this GnuPG version."
    fi
    die "OpenResty release signing key import failed."
  fi
  fingerprints="$("${gpg_command}" --batch --homedir "${gpg_home}" --with-colons --fingerprint 2>/dev/null | awk -F: '$1 == "fpr" {print $10}')"
  if [[ -z "${fingerprints}" ]] && grep -qi "no user ID" <<<"${gpg_import_output}"; then
    die "OpenResty release signing key has no importable user ID for this GnuPG version."
  fi
  grep -Fxq "${openresty_release_signing_fingerprint}" <<<"${fingerprints}" || die "OpenResty release signing key fingerprint verification failed."
  verification_status="$("${gpg_command}" --batch --homedir "${gpg_home}" --status-fd=1 --verify "${signature}" "${destination}" 2>/dev/null)" || die "OpenResty source signature verification failed."
  local signature_verified=false
  while IFS=' ' read -r marker status signing_fingerprint created timestamp expire version reserved pubkey hash class primary_fingerprint rest; do
    [[ "${marker}" == "[GNUPG:]" && "${status}" == "VALIDSIG" ]] || continue
    if [[ "${signing_fingerprint}" == "${openresty_release_signing_fingerprint}" || "${primary_fingerprint}" == "${openresty_release_signing_fingerprint}" ]]; then
      signature_verified=true
      break
    fi
  done <<<"${verification_status}"
  [[ "${signature_verified}" == true ]] || die "OpenResty source signature fingerprint verification failed."
  rm -rf -- "${gpg_home}"
}
systemd_available() {
  command -v systemctl >/dev/null 2>&1 &&
    systemctl show-environment >/dev/null 2>&1
}
systemctl_property_value() {
  local unit="$1" property="$2" value
  value="$(systemctl show "${unit}" --property="${property}" 2>/dev/null || true)"
  if [[ "${value}" == "${property}="* ]]; then
    value="${value#*=}"
  fi
  printf '%s' "${value}"
}
legacy_openresty_service_matches() {
  systemd_available || return 1
  [[ -x "${openresty_binary}" || -x "${install_dir}/nginx/sbin/nginx" ]] || return 1
  local exec_start="$(systemctl_property_value "${legacy_service_name}.service" ExecStart)"
  [[ "${exec_start}" == *"${openresty_binary}"* || "${exec_start}" == *"${install_dir}/nginx/sbin/nginx"* ]]
}
legacy_openresty_is_active() {
  legacy_openresty_service_matches || return 1
  systemctl is-active --quiet "${legacy_service_name}.service"
}
legacy_openresty_is_enabled() {
  legacy_openresty_service_matches || return 1
  systemctl is-enabled --quiet "${legacy_service_name}.service"
}
adopt_legacy_openresty_enablement() {
  legacy_openresty_service_matches || return 0
  if legacy_openresty_is_enabled; then
    systemctl disable "${legacy_service_name}.service" 2>/dev/null || true
    systemctl enable "${service_name}.service" 2>/dev/null || true
  fi
}
openresty_runtime_binary() {
  [[ -x "${openresty_binary}" ]] && { printf '%s' "${openresty_binary}"; return 0; }
  [[ -x "${install_dir}/nginx/sbin/nginx" ]] && { printf '%s' "${install_dir}/nginx/sbin/nginx"; return 0; }
  return 1
}
openresty_main_config() {
  [[ -f "${install_dir}/nginx/conf/nginx.conf" ]] && { printf '%s' "${install_dir}/nginx/conf/nginx.conf"; return 0; }
  [[ -f "${install_dir}/nginx/conf/nginx.conf" ]] && { printf '%s' "${install_dir}/nginx/conf/nginx.conf"; return 0; }
  return 1
}
openresty_runtime_pid_file() {
  [[ -f "${install_dir}/nginx/logs/nginx.pid" ]] && { printf '%s' "${install_dir}/nginx/logs/nginx.pid"; return 0; }
  printf '%s' "${install_dir}/nginx/logs/nginx.pid"
}
openresty_is_running() {
  if systemd_available; then
    systemctl is-active --quiet "${service_name}.service" && return 0
    legacy_openresty_is_active && return 0
    return 1
  fi
  local pid=""
  runtime_pid_file="$(openresty_runtime_pid_file || true)"
  [[ -r "${runtime_pid_file}" ]] && read -r pid <"${runtime_pid_file}"
  [[ "${pid}" =~ ^[0-9]+$ ]] && kill -0 "${pid}" 2>/dev/null
}
external_openresty_detected() { return 1; }
snapshot_external_openresty() { return 0; }
snapshot_managed_openresty_config() {
  [[ -d "${install_dir}/nginx/conf" ]] || return 0
  local snapshot="${web_server_migration_root}/openresty/$(date -u +%Y%m%dT%H%M%SZ)-$$"
  install -d -m 0700 -- "${snapshot}"
  cp -a -- "${install_dir}/nginx/conf" "${snapshot}/config"
  printf '%s\n' openresty >"${snapshot}/component"
  emit_progress 12 migration.config.snapshot "Preserved OpenResty virtual-host configuration for the next Web server"
}
migrate_external_openresty_config() {
  [[ -d "${external_migration_dir}/config" ]] || return 0
  local source destination index=0
  shopt -s nullglob
  for source in "${external_migration_dir}/config/conf.d/"*.conf \
    "${external_migration_dir}/config/sites-enabled/"*; do
    [[ -f "${source}" && ! -L "${source}" ]] || continue
    if grep -Eq '/etc/openresty|include[[:space:]]+[^;]*\*' "${source}"; then
      die "External OpenResty configuration cannot be migrated safely: $(basename -- "${source}")"
    fi
    index=$((index + 1))
    destination="${install_dir}/nginx/conf/conf.d/migrated-$(printf '%03d' "${index}")-$(basename -- "${source}")"
    install -m 0640 -- "${source}" "${destination}"
    emit_progress 55 migration.config.copied "Migrated OpenResty virtual-host configuration $(basename -- "${source}")"
  done
  shopt -u nullglob
}
commit_external_openresty() {
  return 0
}
start_openresty() {
  if systemd_available; then
    if [[ -x "${openresty_binary}" ]]; then
      systemctl start "${service_name}.service"
    elif legacy_openresty_service_matches; then
      systemctl start "${legacy_service_name}.service"
    else
      systemctl start "${service_name}.service"
    fi
    return
  fi
  local runtime_binary runtime_config
  runtime_binary="$(openresty_runtime_binary)"
  runtime_config="$(openresty_main_config)"
  "${runtime_binary}" -p "${install_dir}/nginx/" -c "${runtime_config}"
}
stop_openresty() {
  if systemd_available; then
    if systemctl is-active --quiet "${service_name}.service"; then
      systemctl stop "${service_name}.service"
    fi
    if legacy_openresty_service_matches && legacy_openresty_is_active; then
      systemctl stop "${legacy_service_name}.service" 2>/dev/null || true
    fi
    local attempt
    for attempt in {1..30}; do
      openresty_is_running || return 0
      sleep 1
    done
    die "OpenResty did not stop within 30 seconds."
    return
  fi
  openresty_is_running || return 0
  local runtime_binary runtime_config
  runtime_binary="$(openresty_runtime_binary)"
  runtime_config="$(openresty_main_config)"
  "${runtime_binary}" -p "${install_dir}/nginx/" -c "${runtime_config}" -s quit
  local attempt
  for attempt in {1..30}; do
    openresty_is_running || return 0
    sleep 1
  done
  die "OpenResty did not stop within 30 seconds."
}
reload_openresty() {
  if systemd_available; then
    if systemctl is-active --quiet "${service_name}.service"; then
      systemctl reload "${service_name}.service"
    elif legacy_openresty_service_matches; then
      systemctl reload "${legacy_service_name}.service"
    else
      systemctl reload "${service_name}.service"
    fi
    return
  fi
  runtime_binary="$(openresty_runtime_binary)"
  runtime_config="$(openresty_main_config)"
  "${runtime_binary}" -p "${install_dir}/nginx/" -c "${runtime_config}" -s reload
}
prepare_rollback() {
  install -d -m 0750 -- "${state_dir}"
  rm -rf -- "${rollback_dir}"
  install -d -m 0750 -- "${rollback_dir}"
  if systemd_available; then
    systemctl is-enabled --quiet "${service_name}.service" 2>/dev/null && : >"${rollback_dir}/managed-was-enabled"
    legacy_openresty_is_enabled && : >"${rollback_dir}/legacy-was-enabled"
    if systemctl is-active --quiet "${service_name}.service"; then
      printf 'managed\n' >"${rollback_dir}/active-service"
    elif legacy_openresty_is_active; then
      printf 'legacy\n' >"${rollback_dir}/active-service"
    fi
  elif openresty_is_running; then
    printf 'direct\n' >"${rollback_dir}/active-service"
  fi
  openresty_is_running && stop_openresty
  [[ ! -e "${install_parameters_file}" ]] || cp -a -- "${install_parameters_file}" "${rollback_dir}/install-parameters"
  [[ ! -e "${install_dir}" ]] || mv -- "${install_dir}" "${rollback_dir}/install"
  [[ ! -e "${unit_file}" ]] || cp -a -- "${unit_file}" "${rollback_dir}/openresty.service"
  snapshot_external_openresty
}
restore_rollback() {
  local active_service=""
  [[ -d "${rollback_dir}" ]] || die "No OpenResty rollback point is available."
  stop_openresty 2>/dev/null || true
  if [[ ! -e "${rollback_dir}/restored" ]]; then
    systemd_available && systemctl disable "${service_name}.service" 2>/dev/null || true
    [[ ! -e "${install_dir}" ]] || rm -rf -- "${install_dir}"
    [[ ! -e "${rollback_dir}/install" ]] || mv -- "${rollback_dir}/install" "${install_dir}"
    if [[ -e "${rollback_dir}/install-parameters" ]]; then
      cp -a -- "${rollback_dir}/install-parameters" "${install_parameters_file}"
    else
      rm -f -- "${install_parameters_file}"
    fi
    if [[ -e "${rollback_dir}/openresty.service" ]]; then
      cp -a -- "${rollback_dir}/openresty.service" "${unit_file}"
    else
      rm -f -- "${unit_file}"
    fi
    : >"${rollback_dir}/restored"
  fi
  if systemd_available; then
    systemctl daemon-reload
    if [[ -e "${rollback_dir}/managed-was-enabled" ]]; then
      systemctl enable "${service_name}.service" 2>/dev/null || true
    else
      systemctl disable "${service_name}.service" 2>/dev/null || true
    fi
    if [[ -e "${rollback_dir}/legacy-was-enabled" ]]; then
      systemctl enable "${legacy_service_name}.service" 2>/dev/null || true
    fi
  fi
  if [[ -r "${rollback_dir}/active-service" ]]; then
    read -r active_service <"${rollback_dir}/active-service"
    case "${active_service}" in
      managed) systemctl start "${service_name}.service" ;;
      legacy) systemctl start "${legacy_service_name}.service" ;;
      direct) start_openresty ;;
      *) die "OpenResty rollback runtime marker is invalid." ;;
    esac
  fi
  rm -rf -- "${rollback_dir}"
}
load_install_parameters

openresty_phpmyadmin_detected() {
  [[ -f "${state_root}/phpmyadmin/installed.json" ]] && return 0
  for candidate in /data/wwwroot/phpMyAdmin /data/wwwroot/phpmyadmin /data/wwwroot/default/phpMyAdmin /data/wwwroot/default/phpmyadmin /usr/share/phpmyadmin; do
    [[ -e "${candidate}" ]] && return 0
  done
  return 1
}
openresty_php_fpm_ready() {
  local unit
  if command -v systemctl >/dev/null 2>&1; then
	    for unit in php-fpm.service php-fpm-8.3.service php-fpm-8.2.service php-fpm-8.1.service php-fpm-7.4.service php-fpm-7.0.service php-fpm-5.6.service php8.3-fpm.service php8.2-fpm.service php8.1-fpm.service php7.4-fpm.service php7.0-fpm.service php5.6-fpm.service; do
      systemctl is-active --quiet "${unit}" 2>/dev/null || continue
      [[ -S "${php_fpm_socket}" ]] && return 0
    done
    return 1
  fi
  [[ -S "${php_fpm_socket}" ]]
}
openresty_report_phpmyadmin_integration() {
  openresty_phpmyadmin_detected || return 0
  if ! openresty_php_fpm_ready; then
    printf 'WARNING: OPENRESTY_PHPMYADMIN_INTEGRATION_PENDING: phpMyAdmin was detected, but PHP-FPM socket %s is unavailable. OpenResty installation succeeded; phpMyAdmin access was not verified.\n' "${php_fpm_socket}" >&2
    emit_progress 95 OPENRESTY_PHPMYADMIN_INTEGRATION_PENDING "phpMyAdmin detected, but PHP-FPM integration is unavailable"
    return 0
  fi
  local php_probe php_response php_status php_body response http_status body
  php_probe="${web_root}/default/.oneinstack-php-probe-${BASHPID}.php"
  printf '%s\n' '<?php echo "oneinstack-php-ok";' >"${php_probe}"
  chown "${run_user}:${run_group}" "${php_probe}"
  php_response="$(curl --proto '=http' --connect-timeout 5 --max-time 10 --silent --show-error --output - --write-out $'\n%{http_code}' "http://127.0.0.1:${openresty_port}/$(basename -- "${php_probe}")" || true)"
  php_status="${php_response##*$'\n'}"
  php_body="${php_response%$'\n'*}"
  rm -f -- "${php_probe}"
  if [[ ! "${php_status}" =~ ^2[0-9][0-9]$ || "${php_body}" != *oneinstack-php-ok* ]]; then
    printf 'WARNING: OPENRESTY_PHPMYADMIN_INTEGRATION_PENDING: phpMyAdmin was detected, but the temporary PHP-FPM probe failed. OpenResty installation succeeded; phpMyAdmin access was not verified.\n' >&2
    emit_progress 95 OPENRESTY_PHPMYADMIN_INTEGRATION_PENDING "phpMyAdmin PHP-FPM probe was not verified"
  fi
  response="$(curl --proto '=http' --connect-timeout 5 --max-time 10 --silent --show-error --output - --write-out $'\n%{http_code}' "http://127.0.0.1:${openresty_port}/phpMyAdmin/index.php" || true)"
  http_status="${response##*$'\n'}"
  body="${response%$'\n'*}"
  if [[ ! "${http_status}" =~ ^2[0-9][0-9]$ || -z "${body}" ]]; then
    printf 'WARNING: OPENRESTY_PHPMYADMIN_INTEGRATION_PENDING: phpMyAdmin was detected, but /phpMyAdmin/index.php did not return a verified response. OpenResty installation succeeded; phpMyAdmin access was not verified.\n' >&2
    emit_progress 95 OPENRESTY_PHPMYADMIN_INTEGRATION_PENDING "phpMyAdmin HTTP integration was not verified"
  fi
  return 0
}
