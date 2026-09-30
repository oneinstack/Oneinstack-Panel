#!/usr/bin/env bash
set -Eeuo pipefail
umask 027

component_id="nginx"
package_version="1.0.11"
software_version="${SOFTWARE_VERSION:-1.31.0}"
nginx_port="${NGINX_PORT:-80}"
install_dir="${INSTALL_DIR:-/usr/local/nginx}"
web_root="${WEB_ROOT:-/data/wwwroot}"
log_dir="${LOG_DIR:-/data/wwwlogs}"
run_user="${RUN_USER:-www}"
run_group="${RUN_GROUP:-www}"
php_fpm_socket="${PHP_FPM_SOCKET:-/dev/shm/php-cgi.sock}"
install_mode="${ONEINSTACK_INSTALL_MODE:-center}"
offline_package_path="${ONEINSTACK_OFFLINE_PACKAGE_PATH:-}"
source_url=""
source_signature_url="${source_url}.asc"
source_archive=""
source_sha256=""
package_manager=""
nginx_release_signing_key_urls=(
  "https://nginx.org/keys/arut.key"
  "https://nginx.org/keys/pluknet.key"
  "https://nginx.org/keys/sb.key"
  "https://nginx.org/keys/thresh.key"
)
nginx_release_signing_fingerprints=(
  "43387825DDB1BB97EC36BA5D007C8D7C15D87369"
  "D6786CE303D9A9022998DC6CC8464D549AF75C0A"
  "7338973069ED3F443F4D37DFA64FD5B17ADB39A8"
  "13C82A63B603576156E30A4EA0EA981B66B0D967"
)
state_root="${ONEINSTACK_COMPONENT_STATE:-/var/lib/oneinstack/components}"
state_dir="${state_root}/${component_id}"
web_server_migration_root="${ONEINSTACK_WEB_SERVER_MIGRATION_ROOT:-/var/lib/oneinstack/web-server-migration}"
rollback_dir="${state_dir}/rollback"
managed_path_acl_file="${state_dir}/managed-path-acl"
transaction_path_acl_file="${rollback_dir}/path-acl-added"
path_acl_transaction_file=""
service_name="oneinstack-nginx"
unit_file="/etc/systemd/system/${service_name}.service"
nginx_binary="${install_dir}/sbin/nginx"
nginx_pid_file="${install_dir}/logs/nginx.pid"
external_migration_dir="${rollback_dir}/external"
legacy_service_name="nginx"
install_parameters_file="${state_dir}/install-parameters"

die() { echo "ERROR: $*" >&2; exit 1; }
emit_progress() {
  local percent="$1" code="$2" message="$3" fd="${ONEINSTACK_PROGRESS_FD:-}"
  [[ "${fd}" =~ ^[0-9]+$ ]] || return 0
  message="${message//\\/\\\\}"; message="${message//\"/\\\"}"; message="${message//$'\n'/ }"
  printf '{"type":"progress","percent":%s,"code":"%s","message":"%s"}\n' \
    "${percent}" "${code}" "${message}" >"${fd}" 2>/dev/null || true
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
		1.28.2|1.31.0) ;;
		*) die "Unsupported Nginx version: ${software_version}; select a Center-published exact release." ;;
	esac
	source_url="https://nginx.org/download/nginx-${software_version}.tar.gz"
	source_signature_url="${source_url}.asc"
	source_archive="nginx-${software_version}.tar.gz"
	case "${software_version}" in
		1.28.2) source_sha256="20e5e0f2c917acfb51120eec2fba9a4ba4e1e10fd28465067cc87a7d81a829a3" ;;
		1.31.0) source_sha256="6d5b00d45393af2e4e7c52a442d2a198f0ccbc7678ed062a46f403edd833ebaa" ;;
	esac
	[[ "${install_mode}" == "center" || "${install_mode}" == "offline" ]] || die "ONEINSTACK_INSTALL_MODE must be center or offline."
	if [[ "${install_mode}" == "offline" ]]; then
		[[ -n "${offline_package_path}" && "${offline_package_path}" == /* &&
			"$(realpath -m -- "${offline_package_path}")" == "${offline_package_path}" ]] ||
			die "Offline installation requires a normalized absolute Bundle path."
	elif [[ -n "${offline_package_path}" ]]; then
		die "Offline Bundle path cannot be used in Center mode."
	fi
	[[ "${nginx_port}" =~ ^[0-9]+$ && "${nginx_port}" -ge 1 && "${nginx_port}" -le 65535 ]] || die "NGINX_PORT must be a valid TCP port."
  validate_identifier "${run_user}"; validate_identifier "${run_group}"
  validate_path "${install_dir}" INSTALL_DIR; validate_path "${web_root}" WEB_ROOT
	validate_path "${log_dir}" LOG_DIR; validate_path "${php_fpm_socket}" PHP_FPM_SOCKET
	validate_path "${state_root}" ONEINSTACK_COMPONENT_STATE
  validate_path "${web_server_migration_root}" ONEINSTACK_WEB_SERVER_MIGRATION_ROOT
}
persist_install_parameters() {
  install -d -m 0750 -- "${state_dir}"
  local temporary
  temporary="$(mktemp "${state_dir}/.install-parameters.XXXXXX")"
  {
    printf 'NGINX_PORT=%s\n' "${nginx_port}"
    printf 'INSTALL_DIR=%s\n' "${install_dir}"
    printf 'WEB_ROOT=%s\n' "${web_root}"
    printf 'LOG_DIR=%s\n' "${log_dir}"
    printf 'RUN_USER=%s\n' "${run_user}"
	    printf 'RUN_GROUP=%s\n' "${run_group}"
	    printf 'PHP_FPM_SOCKET=%s\n' "${php_fpm_socket}"
  } >"${temporary}"
  chmod 0600 "${temporary}"
  mv -f -- "${temporary}" "${install_parameters_file}"
}
load_install_parameters() {
  [[ -r "${install_parameters_file}" ]] || return 0
	  local requested_nginx_port="${nginx_port}" requested_install_dir="${install_dir}"
	  local requested_web_root="${web_root}" requested_log_dir="${log_dir}"
	  local requested_run_user="${run_user}" requested_run_group="${run_group}"
	  local requested_php_fpm_socket="${php_fpm_socket}"
  local saved_nginx_port="" saved_install_dir="" saved_web_root="" saved_log_dir=""
  local saved_run_user="" saved_run_group="" saved_php_fpm_socket=""
  local key value marker
  while IFS='=' read -r key value; do
    case "${key}" in
      NGINX_PORT) saved_nginx_port="${value}" ;;
      INSTALL_DIR) saved_install_dir="${value}" ;;
      WEB_ROOT) saved_web_root="${value}" ;;
      LOG_DIR) saved_log_dir="${value}" ;;
      RUN_USER) saved_run_user="${value}" ;;
      RUN_GROUP) saved_run_group="${value}" ;;
      PHP_FPM_SOCKET) saved_php_fpm_socket="${value}" ;;
    esac
  done <"${install_parameters_file}"
  [[ -z "${saved_nginx_port}" ]] || nginx_port="${saved_nginx_port}"
  [[ -z "${saved_install_dir}" ]] || install_dir="${saved_install_dir}"
  [[ -z "${saved_web_root}" ]] || web_root="${saved_web_root}"
  [[ -z "${saved_log_dir}" ]] || log_dir="${saved_log_dir}"
  [[ -z "${saved_run_user}" ]] || run_user="${saved_run_user}"
  [[ -z "${saved_run_group}" ]] || run_group="${saved_run_group}"
  [[ -z "${saved_php_fpm_socket}" ]] || php_fpm_socket="${saved_php_fpm_socket}"
	  for key in NGINX_PORT INSTALL_DIR WEB_ROOT LOG_DIR RUN_USER RUN_GROUP PHP_FPM_SOCKET; do
    marker="ONEINSTACK_PARAMETER_${key}_EXPLICIT"
    [[ "${!marker:-false}" == "true" ]] || continue
    case "${key}" in
      NGINX_PORT) nginx_port="${requested_nginx_port}" ;;
      INSTALL_DIR) install_dir="${requested_install_dir}" ;;
      WEB_ROOT) web_root="${requested_web_root}" ;;
      LOG_DIR) log_dir="${requested_log_dir}" ;;
      RUN_USER) run_user="${requested_run_user}" ;;
	      RUN_GROUP) run_group="${requested_run_group}" ;;
	      PHP_FPM_SOCKET) php_fpm_socket="${requested_php_fpm_socket}" ;;
    esac
  done
  web_root="$(normalize_web_root_parameter "${web_root}")"
  nginx_binary="${install_dir}/sbin/nginx"
  nginx_pid_file="${install_dir}/logs/nginx.pid"
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
  if [[ "${ONEINSTACK_PARAMETER_NGINX_PORT_EXPLICIT:-false}" == "true" ]]; then
    [[ -z "${detected_port}" || "${detected_port}" == "${nginx_port}" ]] ||
      rewrite_default_site_port "${site_config_file}" "${nginx_port}"
    return 0
  fi
  [[ -z "${detected_port}" ]] || nginx_port="${detected_port}"
}
check_host() {
  [[ -r /etc/os-release ]] || die "/etc/os-release is unavailable."
  source /etc/os-release
  os_id="${ID:-}"
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
        acl build-essential ca-certificates curl gnupg iproute2 libpcre2-dev libssl-dev tar zlib1g-dev
      ;;
    rhel)
      local package_manager="dnf"
      command -v "${package_manager}" >/dev/null 2>&1 || package_manager="yum"
      local pcre_devel="pcre2-devel"
      [[ "${os_id}" == "centos" && "${os_major}" == "7" ]] && pcre_devel="pcre-devel"
      "${package_manager}" install -y \
        acl gcc gcc-c++ make ca-certificates curl gnupg2 iproute "${pcre_devel}" openssl-devel tar gzip zlib-devel
      ;;
    suse)
      zypper --non-interactive refresh
      zypper --non-interactive install --no-recommends \
        acl gcc gcc-c++ make ca-certificates curl gpg2 iproute2 libpcre2-devel libopenssl-devel tar gzip zlib-devel
      ;;
	  esac
}
offline_package_dir() {
	[[ -n "${offline_package_path}" ]] || die "Offline installation requires ONEINSTACK_OFFLINE_PACKAGE_PATH."
	printf '%s/packages/%s/%s/%s\n' "${offline_package_path}" "${os_id}" "${os_version}" "${host_architecture}"
}
validate_offline_bundle() {
	local package_dir
	[[ -d "${offline_package_path}" ]] || die "Offline Nginx Bundle is unavailable."
	[[ -f "${offline_package_path}/manifest.yaml" ]] || die "Offline Bundle manifest is missing."
	[[ -f "${offline_package_path}/files.sha256" ]] || die "Offline Bundle checksum file is missing."
	package_dir="$(offline_package_dir)"
	[[ -d "${package_dir}" ]] || die "Offline Nginx dependencies are missing for this host."
	[[ -f "${offline_package_path}/artifacts/${host_architecture}/${source_archive}" ]] || die "Offline Nginx ${software_version} source artifact is missing."
	[[ -f "${offline_package_path}/artifacts/${host_architecture}/${source_archive}.asc" ]] || die "Offline Nginx ${software_version} signature is missing."
	(cd "${offline_package_path}" && sha256sum -c files.sha256 --status) || die "Offline Bundle checksum verification failed."
	grep -Eq '^[[:space:]]+id:[[:space:]]+nginx[[:space:]]*$' "${offline_package_path}/manifest.yaml" || die "Offline Bundle component is not Nginx."
	grep -Eq "^[[:space:]]+version:[[:space:]]+${package_version//./\\.}[[:space:]]*$" "${offline_package_path}/manifest.yaml" || die "Offline Bundle package version does not match Nginx ${package_version}."
	validate_offline_acl_package
}
validate_offline_acl_package() {
	local package_dir
	package_dir="$(offline_package_dir)"
	case "${package_manager}" in
		apt)
			find "${package_dir}" -maxdepth 1 -type f -name 'acl_*.deb' -print -quit 2>/dev/null | grep -q . ||
				die "Offline Nginx Bundle is missing the ACL package for ${os_id} ${os_version} ${host_architecture}."
			;;
		dnf|yum|zypper)
			find "${package_dir}" -maxdepth 1 -type f -name 'acl-[0-9]*.rpm' -print -quit 2>/dev/null | grep -q . ||
				die "Offline Nginx Bundle is missing the ACL package for ${os_id} ${os_version} ${host_architecture}."
			;;
		*) die "Unsupported package manager while validating the offline ACL package: ${package_manager}." ;;
	esac
}
install_dependencies_offline() {
	local package_dir
	local -a packages=()
	package_dir="$(offline_package_dir)"
	mapfile -t packages < <(find "${package_dir}" -maxdepth 1 -type f \( -name '*.deb' -o -name '*.rpm' \) -print | sort)
	((${#packages[@]} > 0)) || die "Offline Nginx dependency packages are missing."
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
  chmod 0755 "${install_dir}" "${install_dir}/sbin" "${nginx_binary}"
  ensure_runtime_path_traversal "${run_user}" "${web_root}" "WEB_ROOT"
  ensure_runtime_path_traversal "${run_user}" "${log_dir}" "LOG_DIR"
  emit_progress 20 permissions.runtime.applied "Nginx web and log directory permissions applied"
}
verify_runtime_permissions() {
  require_command runuser
  runuser -u "${run_user}" -- test -x "${web_root}" ||
    die "Nginx worker user cannot traverse the managed web root."
  runuser -u "${run_user}" -- test -x "${web_root}/default" ||
    die "Nginx worker user cannot traverse the default web root."
  runuser -u "${run_user}" -- test -r "${web_root}/default" ||
    die "Nginx worker user cannot read the default web root."
  runuser -u "${run_user}" -- test -w "${log_dir}" ||
    die "Nginx worker user cannot write the managed log directory."
  emit_progress 25 permissions.runtime.verified "Nginx runtime access verified as ${run_user}"
}
remove_managed_path_acl_entries() {
  local records_file="$1" acl_user acl_path expected_permissions current_permissions
  [[ -f "${records_file}" ]] || return 0
  if ! command -v getfacl >/dev/null 2>&1 || ! command -v setfacl >/dev/null 2>&1; then
    printf 'WARNING: managed Nginx path ACL could not be restored because ACL utilities are unavailable.\n' >&2
    return 0
  fi
  while IFS=$'\t' read -r acl_user acl_path expected_permissions; do
    [[ "${acl_user}" =~ ^[a-z_][a-z0-9_-]{0,30}$ && "${acl_path}" == /* && "${acl_path}" != "/" && -d "${acl_path}" ]] || continue
    expected_permissions="${expected_permissions:---x}"
    current_permissions="$(getfacl -cp -- "${acl_path}" 2>/dev/null | awk -F: -v user="${acl_user}" '$1 == "user" && $2 == user {print $3; exit}' || true)"
    if [[ "${current_permissions}" == "${expected_permissions}" ]]; then
      setfacl --no-mask -x "u:${acl_user}" -- "${acl_path}" ||
        printf 'WARNING: managed Nginx path ACL could not be restored for %s.\n' "${acl_path}" >&2
    elif [[ -n "${current_permissions}" ]]; then
      printf 'WARNING: managed Nginx path ACL for %s was changed externally and was preserved.\n' "${acl_path}" >&2
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
  local destination="$1" signature gpg_home fingerprints verification_status
  local key_index key_file key_url expected_fingerprint fingerprint
  local marker status signing_fingerprint primary_fingerprint created timestamp expire version reserved pubkey hash class rest
  local key_verified signature_verified
  local gpg_command="gpg"
  command -v "${gpg_command}" >/dev/null 2>&1 || gpg_command="gpg2"
  require_command "${gpg_command}"
  if [[ "${install_mode}" == "offline" ]]; then
    local offline_source="${offline_package_path}/artifacts/${host_architecture}/${source_archive}"
    local offline_signature="${offline_source}.asc"
    [[ -f "${offline_source}" && -f "${offline_signature}" ]] || die "Offline Nginx source or signature is missing."
    cp -- "${offline_source}" "${destination}"
    signature="${destination}.asc"
    cp -- "${offline_signature}" "${signature}"
  else
    curl --proto '=https' --tlsv1.2 --fail --location --retry 3 --connect-timeout 20 --output "${destination}" "${source_url}"
    signature="${destination}.asc"
  fi
  printf '%s  %s\n' "${source_sha256}" "${destination}" | sha256sum --check --status || die "Nginx source checksum verification failed."
  signature="${destination}.asc"
  gpg_home="$(mktemp -d "$(dirname -- "${destination}")/.gnupg.XXXXXX")"
  chmod 0700 "${gpg_home}"
  for key_index in "${!nginx_release_signing_key_urls[@]}"; do
    key_url="${nginx_release_signing_key_urls[${key_index}]}"
    expected_fingerprint="${nginx_release_signing_fingerprints[${key_index}]}"
    key_file="${gpg_home}/release-${key_index}.key"
    if [[ "${install_mode}" == "offline" ]]; then
      key_file="${offline_package_path}/keys/nginx/release-${key_index}.key"
      [[ -f "${key_file}" ]] || die "Offline Nginx signing key is missing."
    else
      curl --proto '=https' --tlsv1.2 --fail --location --retry 3 --connect-timeout 20 --output "${key_file}" "${key_url}"
    fi
    "${gpg_command}" --batch --homedir "${gpg_home}" --import "${key_file}" >/dev/null 2>&1
    fingerprints="$("${gpg_command}" --batch --homedir "${gpg_home}" --with-colons --fingerprint 2>/dev/null | awk -F: '$1 == "fpr" {print $10}')"
    key_verified=false
    while IFS= read -r fingerprint; do
      if [[ "${fingerprint}" == "${expected_fingerprint}" ]]; then
        key_verified=true
        break
      fi
    done <<<"${fingerprints}"
    [[ "${key_verified}" == true ]] || die "Nginx release signing key fingerprint verification failed."
  done
  if [[ "${install_mode}" != "offline" ]]; then
    curl --proto '=https' --tlsv1.2 --fail --location --retry 3 --connect-timeout 20 --output "${signature}" "${source_signature_url}"
  fi
  verification_status="$("${gpg_command}" --batch --homedir "${gpg_home}" --status-fd=1 --verify "${signature}" "${destination}" 2>/dev/null)" ||
    die "Nginx source signature verification failed."
  signature_verified=false
  while IFS=' ' read -r marker status signing_fingerprint created timestamp expire version reserved pubkey hash class primary_fingerprint rest; do
    [[ "${marker}" == "[GNUPG:]" && "${status}" == "VALIDSIG" ]] || continue
    for expected_fingerprint in "${nginx_release_signing_fingerprints[@]}"; do
      if [[ "${signing_fingerprint}" == "${expected_fingerprint}" || "${primary_fingerprint}" == "${expected_fingerprint}" ]]; then
        signature_verified=true
        break 2
      fi
    done
  done <<<"${verification_status}"
  [[ "${signature_verified}" == true ]] || die "Nginx source signature fingerprint verification failed."
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
legacy_nginx_service_matches() {
  systemd_available || return 1
  [[ -x "${nginx_binary}" ]] || return 1
  local exec_start
  exec_start="$(systemctl_property_value "${legacy_service_name}.service" ExecStart)"
  [[ "${exec_start}" == *"${nginx_binary}"* ]]
}
legacy_nginx_is_active() {
  legacy_nginx_service_matches || return 1
  systemctl is-active --quiet "${legacy_service_name}.service"
}
legacy_nginx_is_enabled() {
  legacy_nginx_service_matches || return 1
  systemctl is-enabled --quiet "${legacy_service_name}.service"
}
adopt_legacy_nginx_enablement() {
  legacy_nginx_service_matches || return 0
  if legacy_nginx_is_enabled; then
    systemctl disable "${legacy_service_name}.service" 2>/dev/null || true
    systemctl enable "${service_name}.service" 2>/dev/null || true
  fi
}
nginx_is_running() {
  if systemd_available; then
    systemctl is-active --quiet "${service_name}.service" && return 0
    legacy_nginx_is_active && return 0
    return 1
  fi
  local pid=""
  [[ -r "${nginx_pid_file}" ]] && read -r pid <"${nginx_pid_file}"
  [[ "${pid}" =~ ^[0-9]+$ ]] && kill -0 "${pid}" 2>/dev/null
}
external_nginx_detected() {
  [[ ! -f "${state_dir}/version" ]] &&
    { [[ -d /etc/nginx ]] || command -v nginx >/dev/null 2>&1 ||
      (command -v dpkg-query >/dev/null 2>&1 && dpkg-query -W -f='${Status}' nginx 2>/dev/null | grep -Fq 'install ok installed') ||
      (command -v rpm >/dev/null 2>&1 && rpm -qa 'nginx*' 2>/dev/null | grep -q .); }
}
snapshot_external_nginx() {
  external_nginx_detected || return 0
  install -d -m 0700 -- "${external_migration_dir}"
  [[ -d /etc/nginx ]] && cp -a -- /etc/nginx "${external_migration_dir}/config"
  if command -v dpkg-query >/dev/null 2>&1; then
    dpkg-query -W -f='${binary:Package}\n' 'nginx*' 2>/dev/null | sort -u >"${external_migration_dir}/packages" || true
  elif command -v rpm >/dev/null 2>&1; then
    rpm -qa --qf '%{NAME}\n' 'nginx*' 2>/dev/null | sort -u >"${external_migration_dir}/packages" || true
  else
    : >"${external_migration_dir}/packages"
  fi
  : >"${external_migration_dir}/detected"
  emit_progress 20 migration.snapshot.created "External Nginx configuration and package inventory captured"
}
snapshot_managed_nginx_config() {
  [[ -d "${install_dir}/conf" ]] || return 0
  local snapshot="${web_server_migration_root}/nginx/$(date -u +%Y%m%dT%H%M%SZ)-$$"
  install -d -m 0700 -- "${snapshot}"
  cp -a -- "${install_dir}/conf" "${snapshot}/config"
  printf '%s\n' nginx >"${snapshot}/component"
  emit_progress 12 migration.config.snapshot "Preserved Nginx virtual-host configuration for the next Web server"
}
migrate_external_nginx_config() {
  [[ -d "${external_migration_dir}/config" ]] || return 0
  local source destination index=0
  shopt -s nullglob
  for source in "${external_migration_dir}/config/conf.d/"*.conf \
    "${external_migration_dir}/config/sites-enabled/"*; do
    [[ -f "${source}" && ! -L "${source}" ]] || continue
    if grep -Eq '/etc/nginx|include[[:space:]]+[^;]*\*' "${source}"; then
      die "External Nginx configuration cannot be migrated safely: $(basename -- "${source}")"
    fi
    index=$((index + 1))
    destination="${install_dir}/conf/conf.d/migrated-$(printf '%03d' "${index}")-$(basename -- "${source}")"
    install -m 0640 -- "${source}" "${destination}"
    emit_progress 55 migration.config.copied "Migrated Nginx virtual-host configuration $(basename -- "${source}")"
  done
  shopt -u nullglob
}
commit_external_nginx() {
  [[ -f "${external_migration_dir}/detected" ]] || return 0
  local packages=()
  mapfile -t packages <"${external_migration_dir}/packages"
  if ((${#packages[@]} > 0)); then
    emit_progress 82 migration.package.removing "Removing replaced external Nginx packages"
    case "${os_family}" in
      debian) DEBIAN_FRONTEND=noninteractive apt-get remove -y "${packages[@]}" ;;
      rhel)
        local package_manager="dnf"
        command -v "${package_manager}" >/dev/null 2>&1 || package_manager="yum"
        "${package_manager}" remove -y "${packages[@]}"
        ;;
      suse) zypper --non-interactive remove "${packages[@]}" ;;
    esac
  fi
  rm -rf -- /etc/nginx
  systemctl daemon-reload
  systemctl stop "${service_name}.service" 2>/dev/null || true
  "${nginx_binary}" -t
  if nginx_is_running; then
    die "Managed Nginx could not be stopped after external package removal."
  fi
  systemctl enable --now "${service_name}.service"
  emit_progress 88 migration.commit.completed "External Nginx package and configuration replacement committed"
}
start_nginx() {
  if systemd_available; then
    systemctl start "${service_name}.service"
  else
    "${nginx_binary}"
  fi
}
stop_nginx() {
  if systemd_available; then
    systemctl stop "${service_name}.service"
    if legacy_nginx_service_matches; then
      systemctl stop "${legacy_service_name}.service" 2>/dev/null || true
    fi
    local attempt
    for attempt in {1..30}; do
      nginx_is_running || return 0
      sleep 1
    done
    die "Nginx did not stop within 30 seconds."
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
    systemctl reload "${service_name}.service"
  else
    "${nginx_binary}" -s reload
  fi
}
prepare_rollback() {
  install -d -m 0750 -- "${state_dir}"
  rm -rf -- "${rollback_dir}"; install -d -m 0750 -- "${rollback_dir}"
  : >"${transaction_path_acl_file}"
  chmod 0600 "${transaction_path_acl_file}"
  if nginx_is_running; then : >"${rollback_dir}/was-active"; stop_nginx; fi
  [[ ! -e "${install_dir}" ]] || mv -- "${install_dir}" "${rollback_dir}/install"
  [[ ! -e "${unit_file}" ]] || cp -a -- "${unit_file}" "${rollback_dir}/nginx.service"
  snapshot_external_nginx
}
restore_rollback() {
  stop_nginx 2>/dev/null || true
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
    rm -rf -- /etc/nginx
    cp -a -- "${external_migration_dir}/config" /etc/nginx
  fi
  if [[ -e "${rollback_dir}/nginx.service" ]]; then cp -a -- "${rollback_dir}/nginx.service" "${unit_file}"; else rm -f -- "${unit_file}"; fi
  systemd_available && systemctl daemon-reload
  [[ ! -e "${rollback_dir}/was-active" ]] || start_nginx
  restore_transaction_path_acl "${transaction_path_acl_file}"
}

load_install_parameters
