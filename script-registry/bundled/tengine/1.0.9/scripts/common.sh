#!/usr/bin/env bash
set -Eeuo pipefail
umask 027

component_id="tengine"
package_version="1.0.9"
software_version="${SOFTWARE_VERSION:-3.1.0}"
tengine_port="${TENGINE_PORT:-80}"
install_dir="${INSTALL_DIR:-/usr/local/tengine}"
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

state_root="${ONEINSTACK_COMPONENT_STATE:-/var/lib/oneinstack/components}"
state_dir="${state_root}/${component_id}"
web_server_migration_root="${ONEINSTACK_WEB_SERVER_MIGRATION_ROOT:-/var/lib/oneinstack/web-server-migration}"
rollback_dir="${state_dir}/rollback"
managed_path_acl_file="${state_dir}/managed-path-acl"
transaction_path_acl_file="${rollback_dir}/path-acl-added"
path_acl_transaction_file=""
service_name="oneinstack-tengine"
unit_file="/etc/systemd/system/${service_name}.service"
tengine_binary="${install_dir}/sbin/tengine"
tengine_pid_file="${install_dir}/logs/tengine.pid"
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
  case "${value}" in /|/usr|/usr/local|/etc|/var|/data|/home|/root) die "${label} is too broad: ${value}" ;; esac
}
validate_inputs() {
  case "${software_version}" in
    3.1.0) source_sha256="64ed7155c0c904ce0fe7199c21b8eb6c2abfc267278fa8af832c0cb781e864dc" ;;
    *) die "Unsupported Tengine version: ${software_version}; select the Center-published exact release." ;;
  esac
  source_url="https://mirrors.oneinstack.com/oneinstack/src/tengine-${software_version}.tar.gz"
  source_archive="tengine-${software_version}.tar.gz"
  [[ "${install_mode}" == "center" || "${install_mode}" == "offline" ]] || die "ONEINSTACK_INSTALL_MODE must be center or offline."
  if [[ "${install_mode}" == "offline" ]]; then
    [[ -n "${offline_package_path}" && "${offline_package_path}" == /* &&
      "$(realpath -m -- "${offline_package_path}")" == "${offline_package_path}" ]] ||
      die "Offline installation requires a normalized absolute Bundle path."
  elif [[ -n "${offline_package_path}" ]]; then
    die "Offline Bundle path cannot be used in Center mode."
  fi
  [[ "${tengine_port}" =~ ^[0-9]+$ && "${tengine_port}" -ge 1 && "${tengine_port}" -le 65535 ]] || die "TENGINE_PORT must be a valid TCP port."
  validate_identifier "${run_user}"; validate_identifier "${run_group}"
  validate_path "${install_dir}" INSTALL_DIR; validate_path "${web_root}" WEB_ROOT
  validate_path "${log_dir}" LOG_DIR; validate_path "${php_fpm_socket}" PHP_FPM_SOCKET
  validate_path "${web_vhost_root}" WEB_VHOST_ROOT
  validate_path "${state_root}" ONEINSTACK_COMPONENT_STATE
	validate_path "${web_server_migration_root}" ONEINSTACK_WEB_SERVER_MIGRATION_ROOT
}
persist_install_parameters() {
  install -d -m 0750 -- "${state_dir}"
  local temporary
  temporary="$(mktemp "${state_dir}/.install-parameters.XXXXXX")"
  {
    printf 'TENGINE_PORT=%s\n' "${tengine_port}"
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
  local requested_tengine_port="${tengine_port}" requested_install_dir="${install_dir}"
  local requested_web_root="${web_root}" requested_log_dir="${log_dir}"
  local requested_web_vhost_root="${web_vhost_root}" requested_run_user="${run_user}" requested_run_group="${run_group}"
  local requested_php_fpm_socket="${php_fpm_socket}"
  local key value
  while IFS='=' read -r key value; do
    case "${key}" in
      TENGINE_PORT) tengine_port="${value}" ;;
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
  for key in TENGINE_PORT INSTALL_DIR WEB_ROOT LOG_DIR WEB_VHOST_ROOT RUN_USER RUN_GROUP PHP_FPM_SOCKET; do
    marker="ONEINSTACK_PARAMETER_${key}_EXPLICIT"
    [[ "${!marker:-false}" == "true" ]] || continue
    case "${key}" in
      TENGINE_PORT) tengine_port="${requested_tengine_port}" ;;
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
  tengine_binary="${install_dir}/sbin/tengine"
  tengine_pid_file="${install_dir}/logs/tengine.pid"
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
  local site_config_file="${install_dir}/conf/conf.d/default.conf" detected_port
  detected_port="$(configured_default_site_port "${site_config_file}" || true)"
  if [[ "${ONEINSTACK_PARAMETER_TENGINE_PORT_EXPLICIT:-false}" == "true" ]]; then
    [[ -z "${detected_port}" || "${detected_port}" == "${tengine_port}" ]] ||
      rewrite_default_site_port "${site_config_file}" "${tengine_port}"
    return 0
  fi
  [[ -z "${detected_port}" ]] || tengine_port="${detected_port}"
}
check_host() {
  [[ -r /etc/os-release ]] || die "/etc/os-release is unavailable."
  source /etc/os-release
  os_id="${ID:-}"
  os_release_name="${NAME:-} ${PRETTY_NAME:-}"
  if [[ "${os_id}" == "centos" && "${os_release_name}" == *"Stream"* ]]; then
    os_id="centos-stream"
  fi
  os_version="${VERSION_ID:-}"
  os_major="${os_version%%.*}"
  case "${os_id}" in
    ubuntu)
      case "${os_version}" in 22.04|24.04|26.04) os_family="debian" ;; *) die "Unsupported Ubuntu release: ${os_version}" ;; esac ;;
    debian)
      case "${os_major}" in 11|12|13) os_family="debian" ;; *) die "Unsupported Debian release: ${os_version}" ;; esac ;;
    centos)
      case "${os_major}" in 7) os_family="rhel" ;; *) die "Unsupported CentOS release: ${os_version}" ;; esac
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
        acl build-essential ca-certificates curl iproute2 libpcre2-dev libssl-dev tar zlib1g-dev
      ;;
    rhel)
      local package_manager="dnf"
      command -v "${package_manager}" >/dev/null 2>&1 || package_manager="yum"
      local pcre_devel="pcre2-devel"
      [[ "${os_id}" == "centos" && "${os_major}" == "7" ]] && pcre_devel="pcre-devel"
      "${package_manager}" install -y \
        acl gcc gcc-c++ make ca-certificates curl iproute "${pcre_devel}" openssl-devel tar gzip zlib-devel
      ;;
    suse)
      zypper --non-interactive refresh
      zypper --non-interactive install --no-recommends \
        acl gcc gcc-c++ make ca-certificates curl iproute2 libpcre2-devel libopenssl-devel tar gzip zlib-devel
      ;;
	  esac
}
offline_package_dir() {
	[[ -n "${offline_package_path}" ]] || die "Offline installation requires ONEINSTACK_OFFLINE_PACKAGE_PATH."
	printf '%s/packages/%s/%s/%s\n' "${offline_package_path}" "${os_id}" "${os_version}" "${host_architecture}"
}
validate_offline_bundle() {
  local package_dir="$(offline_package_dir)"
  [[ -d "${offline_package_path}" ]] || die "Offline Tengine Bundle is unavailable."
  [[ -f "${offline_package_path}/manifest.yaml" ]] || die "Offline Bundle manifest is missing."
  [[ -f "${offline_package_path}/files.sha256" ]] || die "Offline Bundle checksum file is missing."
  [[ -f "${offline_package_path}/bundle-info" ]] || die "Offline Bundle metadata is missing."
  grep -Fxq "component=tengine" "${offline_package_path}/bundle-info" || die "Offline Bundle component metadata is not Tengine."
  grep -Fxq "packageVersion=1.0.9" "${offline_package_path}/bundle-info" || die "Offline Bundle package version metadata does not match Tengine 1.0.9."
  grep -Fxq "softwareVersion=${software_version}" "${offline_package_path}/bundle-info" || die "Offline Bundle software version metadata does not match Tengine ${software_version}."
  grep -Fxq "osId=${os_id}" "${offline_package_path}/bundle-info" || die "Offline Bundle OS metadata does not match this host."
  grep -Fxq "osVersion=${os_version}" "${offline_package_path}/bundle-info" || die "Offline Bundle OS version metadata does not match this host."
  grep -Fxq "architecture=${host_architecture}" "${offline_package_path}/bundle-info" || die "Offline Bundle architecture metadata does not match this host."
  [[ -d "${package_dir}" ]] || die "Offline Tengine dependencies are missing for this host."
  [[ -f "${offline_package_path}/artifacts/${host_architecture}/${source_archive}" ]] || die "Offline Tengine ${software_version} source artifact is missing."
  for required_script in precheck.sh install.sh configure.sh verify.sh status.sh start.sh stop.sh restart.sh reload.sh rollback.sh uninstall.sh config.sh; do
    [[ -x "${offline_package_path}/scripts/${required_script}" ]] || die "Offline Tengine lifecycle script is missing: ${required_script}"
  done
  find "${package_dir}" -maxdepth 1 -type f \( -name '*.deb' -o -name '*.rpm' \) -print -quit | grep -q . || die "Offline Tengine dependency packages are missing."
  (cd "${offline_package_path}" && sha256sum -c files.sha256 --status) || die "Offline Bundle checksum verification failed."
  grep -Eq '^[[:space:]]+id:[[:space:]]+tengine[[:space:]]*$' "${offline_package_path}/manifest.yaml" || die "Offline Bundle component is not Tengine."
  grep -Eq "^[[:space:]]+version:[[:space:]]+1\\.0\\.8[[:space:]]*$" "${offline_package_path}/manifest.yaml" || die "Offline Bundle package version does not match Tengine 1.0.9."
}
install_dependencies_offline() {
	local package_dir
	local -a packages=()
	package_dir="$(offline_package_dir)"
	mapfile -t packages < <(find "${package_dir}" -maxdepth 1 -type f \( -name '*.deb' -o -name '*.rpm' \) -print | sort)
	((${#packages[@]} > 0)) || die "Offline Tengine dependency packages are missing."
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
  chmod 0755 "${install_dir}" "${install_dir}/sbin" "${tengine_binary}"
  ensure_runtime_path_traversal "${run_user}" "${web_root}" "WEB_ROOT"
  ensure_runtime_path_traversal "${run_user}" "${log_dir}" "LOG_DIR"
  emit_progress 20 permissions.runtime.applied "Tengine web and log directory permissions applied"
}
verify_runtime_permissions() {
  require_command runuser
  runuser -u "${run_user}" -- test -x "${web_root}" ||
    die "Tengine worker user cannot traverse the managed web root."
  runuser -u "${run_user}" -- test -x "${web_root}/default" ||
    die "Tengine worker user cannot traverse the default web root."
  runuser -u "${run_user}" -- test -r "${web_root}/default" ||
    die "Tengine worker user cannot read the default web root."
  runuser -u "${run_user}" -- test -w "${log_dir}" ||
    die "Tengine worker user cannot write the managed log directory."
  emit_progress 25 permissions.runtime.verified "Tengine runtime access verified as ${run_user}"
}
remove_managed_path_acl_entries() {
  local records_file="$1" acl_user acl_path expected_permissions current_permissions
  [[ -f "${records_file}" ]] || return 0
  if ! command -v getfacl >/dev/null 2>&1 || ! command -v setfacl >/dev/null 2>&1; then
    printf 'WARNING: managed Tengine path ACL could not be restored because ACL utilities are unavailable.\n' >&2
    return 0
  fi
  while IFS=$'\t' read -r acl_user acl_path expected_permissions; do
    [[ "${acl_user}" =~ ^[a-z_][a-z0-9_-]{0,30}$ && "${acl_path}" == /* && "${acl_path}" != "/" && -d "${acl_path}" ]] || continue
    expected_permissions="${expected_permissions:---x}"
    current_permissions="$(getfacl -cp -- "${acl_path}" 2>/dev/null | awk -F: -v user="${acl_user}" '$1 == "user" && $2 == user {print $3; exit}' || true)"
    if [[ "${current_permissions}" == "${expected_permissions}" ]]; then
      setfacl --no-mask -x "u:${acl_user}" -- "${acl_path}" ||
        printf 'WARNING: managed Tengine path ACL could not be restored for %s.\n' "${acl_path}" >&2
    elif [[ -n "${current_permissions}" ]]; then
      printf 'WARNING: managed Tengine path ACL for %s was changed externally and was preserved.\n' "${acl_path}" >&2
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
  local destination="$1"
  if [[ "${install_mode}" == "offline" ]]; then
    local offline_source="${offline_package_path}/artifacts/${host_architecture}/${source_archive}"
    [[ -f "${offline_source}" ]] || die "Offline Tengine source is missing."
    cp -- "${offline_source}" "${destination}"
  else
    curl --proto '=https' --tlsv1.2 --fail --location --retry 3 --connect-timeout 20 --output "${destination}" "${source_url}"
  fi
  printf '%s  %s\n' "${source_sha256}" "${destination}" | sha256sum --check --status || die "Tengine source checksum verification failed."
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
legacy_tengine_service_matches() {
  systemd_available || return 1
  [[ -x "${tengine_binary}" || -x "${install_dir}/sbin/nginx" ]] || return 1
  local exec_start="$(systemctl_property_value "${legacy_service_name}.service" ExecStart)"
  [[ "${exec_start}" == *"${tengine_binary}"* || "${exec_start}" == *"${install_dir}/sbin/nginx"* ]]
}
legacy_tengine_is_active() {
  legacy_tengine_service_matches || return 1
  systemctl is-active --quiet "${legacy_service_name}.service"
}
legacy_tengine_is_enabled() {
  legacy_tengine_service_matches || return 1
  systemctl is-enabled --quiet "${legacy_service_name}.service"
}
adopt_legacy_tengine_enablement() {
  legacy_tengine_service_matches || return 0
  if legacy_tengine_is_enabled; then
    systemctl disable "${legacy_service_name}.service" 2>/dev/null || true
    systemctl enable "${service_name}.service" 2>/dev/null || true
  fi
}
tengine_runtime_binary() {
  [[ -x "${tengine_binary}" ]] && { printf '%s' "${tengine_binary}"; return 0; }
  [[ -x "${install_dir}/sbin/nginx" ]] && { printf '%s' "${install_dir}/sbin/nginx"; return 0; }
  return 1
}
tengine_main_config() {
  [[ -f "${install_dir}/conf/tengine.conf" ]] && { printf '%s' "${install_dir}/conf/tengine.conf"; return 0; }
  [[ -f "${install_dir}/conf/nginx.conf" ]] && { printf '%s' "${install_dir}/conf/nginx.conf"; return 0; }
  return 1
}
tengine_runtime_pid_file() {
  [[ -f "${install_dir}/logs/tengine.pid" ]] && { printf '%s' "${install_dir}/logs/tengine.pid"; return 0; }
  printf '%s' "${install_dir}/logs/nginx.pid"
}
tengine_is_running() {
  if systemd_available; then
    systemctl is-active --quiet "${service_name}.service" && return 0
    legacy_tengine_is_active && return 0
    return 1
  fi
  local pid=""
  runtime_pid_file="$(tengine_runtime_pid_file || true)"
  [[ -r "${runtime_pid_file}" ]] && read -r pid <"${runtime_pid_file}"
  [[ "${pid}" =~ ^[0-9]+$ ]] && kill -0 "${pid}" 2>/dev/null
}
external_tengine_detected() { return 1; }
snapshot_external_tengine() { return 0; }
snapshot_managed_tengine_config() {
  [[ -d "${install_dir}/conf" ]] || return 0
  local snapshot="${web_server_migration_root}/tengine/$(date -u +%Y%m%dT%H%M%SZ)-$$"
  install -d -m 0700 -- "${snapshot}"
  cp -a -- "${install_dir}/conf" "${snapshot}/config"
  printf '%s\n' tengine >"${snapshot}/component"
  emit_progress 12 migration.config.snapshot "Preserved Tengine virtual-host configuration for the next Web server"
}
migrate_external_tengine_config() {
  [[ -d "${external_migration_dir}/config" ]] || return 0
  local source destination index=0
  shopt -s nullglob
  for source in "${external_migration_dir}/config/conf.d/"*.conf \
    "${external_migration_dir}/config/sites-enabled/"*; do
    [[ -f "${source}" && ! -L "${source}" ]] || continue
    if grep -Eq '/etc/tengine|include[[:space:]]+[^;]*\*' "${source}"; then
      die "External Tengine configuration cannot be migrated safely: $(basename -- "${source}")"
    fi
    index=$((index + 1))
    destination="${install_dir}/conf/conf.d/migrated-$(printf '%03d' "${index}")-$(basename -- "${source}")"
    install -m 0640 -- "${source}" "${destination}"
    emit_progress 55 migration.config.copied "Migrated Tengine virtual-host configuration $(basename -- "${source}")"
  done
  shopt -u nullglob
}
commit_external_tengine() {
  return 0
}
start_tengine() {
  if systemd_available; then
    if [[ -x "${tengine_binary}" ]]; then
      systemctl start "${service_name}.service"
    elif legacy_tengine_service_matches; then
      systemctl start "${legacy_service_name}.service"
    else
      systemctl start "${service_name}.service"
    fi
    return
  fi
  local runtime_binary runtime_config
  runtime_binary="$(tengine_runtime_binary)"
  runtime_config="$(tengine_main_config)"
  "${runtime_binary}" -p "${install_dir}/" -c "${runtime_config}"
}
stop_tengine() {
  if systemd_available; then
    if systemctl is-active --quiet "${service_name}.service"; then
      systemctl stop "${service_name}.service"
    fi
    if legacy_tengine_service_matches && legacy_tengine_is_active; then
      systemctl stop "${legacy_service_name}.service" 2>/dev/null || true
    fi
    local attempt
    for attempt in {1..30}; do
      tengine_is_running || return 0
      sleep 1
    done
    die "Tengine did not stop within 30 seconds."
    return
  fi
  tengine_is_running || return 0
  local runtime_binary runtime_config
  runtime_binary="$(tengine_runtime_binary)"
  runtime_config="$(tengine_main_config)"
  "${runtime_binary}" -p "${install_dir}/" -c "${runtime_config}" -s quit
  local attempt
  for attempt in {1..30}; do
    tengine_is_running || return 0
    sleep 1
  done
  die "Tengine did not stop within 30 seconds."
}
reload_tengine() {
  if systemd_available; then
    if systemctl is-active --quiet "${service_name}.service"; then
      systemctl reload "${service_name}.service"
    elif legacy_tengine_service_matches; then
      systemctl reload "${legacy_service_name}.service"
    else
      systemctl reload "${service_name}.service"
    fi
    return
  fi
  runtime_binary="$(tengine_runtime_binary)"
  runtime_config="$(tengine_main_config)"
  "${runtime_binary}" -p "${install_dir}/" -c "${runtime_config}" -s reload
}
prepare_rollback() {
  install -d -m 0750 -- "${state_dir}"
  rm -rf -- "${rollback_dir}"; install -d -m 0750 -- "${rollback_dir}"
  : >"${transaction_path_acl_file}"
  chmod 0600 "${transaction_path_acl_file}"
  if tengine_is_running; then : >"${rollback_dir}/was-active"; stop_tengine; fi
  [[ ! -e "${install_dir}" ]] || mv -- "${install_dir}" "${rollback_dir}/install"
  [[ ! -e "${unit_file}" ]] || cp -a -- "${unit_file}" "${rollback_dir}/tengine.service"
  snapshot_external_tengine
}
restore_rollback() {
  stop_tengine 2>/dev/null || true
  [[ ! -e "${install_dir}" ]] || rm -rf -- "${install_dir}"
  [[ ! -e "${rollback_dir}/install" ]] || mv -- "${rollback_dir}/install" "${install_dir}"
  if [[ -s "${external_migration_dir}/packages" ]]; then
    local packages=()
    mapfile -t packages <"${external_migration_dir}/packages"
    if ((${#packages[@]} > 0)); then
      case "${os_family}" in
        debian)
          if [[ "${install_mode}" == "offline" ]]; then
            DEBIAN_FRONTEND=noninteractive apt-get -y --no-download install "${packages[@]}"
          else
            DEBIAN_FRONTEND=noninteractive apt-get install -y "${packages[@]}"
          fi
          ;;
        rhel)
          local package_manager="dnf"
          command -v "${package_manager}" >/dev/null 2>&1 || package_manager="yum"
          if [[ "${install_mode}" == "offline" ]]; then
            "${package_manager}" --disablerepo='*' --cacheonly install -y "${packages[@]}"
          else
            "${package_manager}" install -y "${packages[@]}"
          fi
          ;;
        suse)
          if [[ "${install_mode}" == "offline" ]]; then
            zypper --non-interactive --no-refresh --no-gpg-checks install --allow-unsigned-rpm "${packages[@]}"
          else
            zypper --non-interactive install "${packages[@]}"
          fi
          ;;
      esac
    fi
  fi
  if [[ -d "${external_migration_dir}/config" ]]; then
    rm -rf -- /etc/tengine
    cp -a -- "${external_migration_dir}/config" /etc/tengine
  fi
  if [[ -e "${rollback_dir}/tengine.service" ]]; then cp -a -- "${rollback_dir}/tengine.service" "${unit_file}"; else rm -f -- "${unit_file}"; fi
  systemd_available && systemctl daemon-reload
  [[ ! -e "${rollback_dir}/was-active" ]] || start_tengine
  restore_transaction_path_acl "${transaction_path_acl_file}"
}

load_install_parameters

tengine_phpmyadmin_detected() {
  [[ -f "${state_root}/phpmyadmin/installed.json" ]] && return 0
  for candidate in /data/wwwroot/phpMyAdmin /data/wwwroot/phpmyadmin /data/wwwroot/default/phpMyAdmin /data/wwwroot/default/phpmyadmin /usr/share/phpmyadmin; do
    [[ -e "${candidate}" ]] && return 0
  done
  return 1
}
tengine_php_fpm_ready() {
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
tengine_report_phpmyadmin_integration() {
  tengine_phpmyadmin_detected || return 0
  if ! tengine_php_fpm_ready; then
    printf 'WARNING: TENGINE_PHPMYADMIN_INTEGRATION_PENDING: phpMyAdmin was detected, but PHP-FPM socket %s is unavailable. Tengine installation succeeded; phpMyAdmin access was not verified.\n' "${php_fpm_socket}" >&2
    emit_progress 95 phpmyadmin_integration_pending "phpMyAdmin detected, but PHP-FPM integration is unavailable"
    return 0
  fi
  local php_probe php_response php_status php_body response http_status body
  php_probe="${web_root}/default/.oneinstack-php-probe-${BASHPID}.php"
  printf '%s\n' '<?php echo "oneinstack-php-ok";' >"${php_probe}"
  chown "${run_user}:${run_group}" "${php_probe}"
  php_response="$(curl --proto '=http' --connect-timeout 5 --max-time 10 --silent --show-error --output - --write-out $'\n%{http_code}' "http://127.0.0.1:${tengine_port}/$(basename -- "${php_probe}")" || true)"
  php_status="${php_response##*$'\n'}"
  php_body="${php_response%$'\n'*}"
  rm -f -- "${php_probe}"
  if [[ ! "${php_status}" =~ ^2[0-9][0-9]$ || "${php_body}" != *oneinstack-php-ok* ]]; then
    printf 'WARNING: TENGINE_PHPMYADMIN_INTEGRATION_PENDING: phpMyAdmin was detected, but the temporary PHP-FPM probe failed. Tengine installation succeeded; phpMyAdmin access was not verified.\n' >&2
    emit_progress 95 phpmyadmin_integration_pending "phpMyAdmin PHP-FPM probe was not verified"
  fi
  response="$(curl --proto '=http' --connect-timeout 5 --max-time 10 --silent --show-error --output - --write-out $'\n%{http_code}' "http://127.0.0.1:${tengine_port}/phpMyAdmin/index.php" || true)"
  http_status="${response##*$'\n'}"
  body="${response%$'\n'*}"
  if [[ ! "${http_status}" =~ ^2[0-9][0-9]$ || -z "${body}" ]]; then
    printf 'WARNING: TENGINE_PHPMYADMIN_INTEGRATION_PENDING: phpMyAdmin was detected, but /phpMyAdmin/index.php did not return a verified response. Tengine installation succeeded; phpMyAdmin access was not verified.\n' >&2
    emit_progress 95 phpmyadmin_integration_pending "phpMyAdmin HTTP integration was not verified"
  fi
  return 0
}
