#!/usr/bin/env bash
set -Eeuo pipefail
umask 027

component_id="caddy"
package_version="1.0.18"
software_version="${SOFTWARE_VERSION:-2.10.2}"
caddy_port="${CADDY_PORT:-80}"
php_fpm_socket="${PHP_FPM_SOCKET:-/dev/shm/php-cgi.sock}"
install_dir="${INSTALL_DIR:-/usr/local/caddy}"
web_root="${WEB_ROOT:-/data/wwwroot}"
log_dir="${LOG_DIR:-/data/wwwlogs}"
web_vhost_root="${WEB_VHOST_ROOT:-/usr/local/one/vhost}"
run_user="${RUN_USER:-caddy}"
run_group="${RUN_GROUP:-caddy}"
data_dir="/var/lib/caddy"
service_name="oneinstack-caddy"
unit_file="/etc/systemd/system/${service_name}.service"
state_root="${ONEINSTACK_COMPONENT_STATE:-/var/lib/oneinstack/components}"
state_dir="${state_root}/${component_id}"
install_parameters_file="${state_dir}/install-parameters"
installed_state_file="${state_dir}/installed.json"
managed_path_acl_file="${state_dir}/managed-path-acl"
rollback_pointer="${state_dir}/rollback-current"
path_acl_transaction_file=""
binary="${install_dir}/bin/caddy"
config_dir="${install_dir}/conf"
caddyfile="${config_dir}/Caddyfile"
managed_config="${config_dir}/oneinstack-default.caddy"
vhost_dir="${web_vhost_root}/caddy"
install_mode="${ONEINSTACK_INSTALL_MODE:-center}"
offline_package_path="${ONEINSTACK_OFFLINE_PACKAGE_PATH:-}"
uninstall_data_policy="${UNINSTALL_DATA_POLICY:-preserve}"
uninstall_confirm_data_deletion="${UNINSTALL_CONFIRM_DATA_DELETION:-false}"
detected_os_id=""
detected_os_version=""
detected_architecture=""
artifact_file=""
artifact_sha256=""
artifact_url=""

log() { printf '[caddy] %s\n' "$*"; }
warn() { printf 'WARNING: %s\n' "$*" >&2; }
die_code() { local code="$1"; shift; printf '%s: %s\n' "${code}" "$*" >&2; exit 1; }
emit_progress() { printf 'PROGRESS %s %s %s\n' "$1" "$2" "$3"; }
require_root() { [[ "$(id -u)" -eq 0 ]] || die_code CADDY_ROOT_REQUIRED "Caddy lifecycle actions must run as root."; }
command_exists() { command -v "$1" >/dev/null 2>&1; }
systemd_available() { command_exists systemctl && [[ -d /run/systemd/system ]]; }

validate_path() {
  local value="$1" name="$2"
  [[ "${value}" == /* && "${value}" != "/" && "${value}" != *$'\n'* && "${value}" != *$'\r'* ]] ||
    die_code CADDY_INVALID_PARAMETER "${name} must be a safe absolute path."
  case "${value}" in *"/../"*|*/..|*"/./"*) die_code CADDY_INVALID_PARAMETER "${name} must be normalized." ;; esac
  [[ "${value}" =~ ^/[A-Za-z0-9_./@:+-]+$ ]] || die_code CADDY_INVALID_PARAMETER "${name} contains unsupported characters."
}

validate_inputs() {
  [[ "${software_version}" == "2.10.2" ]] || die_code CADDY_UNSUPPORTED_VERSION "SOFTWARE_VERSION must be exactly 2.10.2."
  [[ "${caddy_port}" =~ ^[0-9]+$ && "${caddy_port}" -ge 1 && "${caddy_port}" -le 65535 ]] ||
    die_code CADDY_INVALID_PORT "CADDY_PORT must be between 1 and 65535."
  validate_path "${php_fpm_socket}" PHP_FPM_SOCKET
  validate_path "${install_dir}" INSTALL_DIR
  validate_path "${web_root}" WEB_ROOT
  validate_path "${log_dir}" LOG_DIR
  validate_path "${web_vhost_root}" WEB_VHOST_ROOT
  validate_path "${state_root}" ONEINSTACK_COMPONENT_STATE
  [[ "${install_dir}" == "/usr/local/caddy" ]] || die_code CADDY_FIXED_PARAMETER "INSTALL_DIR is server-managed and must be /usr/local/caddy."
  [[ "${web_vhost_root}" == "/usr/local/one/vhost" ]] || die_code CADDY_FIXED_PARAMETER "WEB_VHOST_ROOT is server-managed and must be /usr/local/one/vhost."
  [[ "${run_user}" == "caddy" && "${run_group}" == "caddy" ]] || die_code CADDY_FIXED_PARAMETER "RUN_USER and RUN_GROUP are server-managed and must be caddy."
  [[ "${install_mode}" == "center" || "${install_mode}" == "offline" ]] || die_code CADDY_INVALID_INSTALL_MODE "ONEINSTACK_INSTALL_MODE must be center or offline."
  [[ "${uninstall_data_policy}" == "preserve" || "${uninstall_data_policy}" == "delete" ]] || die_code CADDY_INVALID_PARAMETER "UNINSTALL_DATA_POLICY must be preserve or delete."
  [[ "${uninstall_confirm_data_deletion}" == "true" || "${uninstall_confirm_data_deletion}" == "false" ]] || die_code CADDY_INVALID_PARAMETER "UNINSTALL_CONFIRM_DATA_DELETION must be true or false."
}

normalize_major_version() { printf '%s' "${1%%.*}"; }

detect_platform() {
  [[ -r /etc/os-release ]] || die_code CADDY_UNSUPPORTED_SYSTEM "/etc/os-release is unavailable."
  local raw_id raw_version raw_name ID="" VERSION_ID="" NAME=""
  # shellcheck disable=SC1091
  . /etc/os-release
  raw_id="${ID}"
  raw_version="${VERSION_ID}"
  raw_name="${NAME}"
  [[ -n "${raw_id}" && -n "${raw_version}" ]] || die_code CADDY_UNSUPPORTED_SYSTEM "OS identity is incomplete."
  detected_os_id="${raw_id,,}"
  if [[ "${detected_os_id}" == "centos" && "${raw_name,,}" == *stream* ]]; then detected_os_id="centos-stream"; fi
  case "${detected_os_id}" in
    ubuntu)
      case "${raw_version}" in
        22.04|22.04.*) detected_os_version="22.04" ;;
        24.04|24.04.*) detected_os_version="24.04" ;;
        26.04|26.04.*) detected_os_version="26.04" ;;
        *) die_code CADDY_UNSUPPORTED_SYSTEM "Unsupported Ubuntu version ${raw_version}." ;;
      esac
      ;;
    debian)
      detected_os_version="$(normalize_major_version "${raw_version}")"
      case "${detected_os_version}" in 11|12|13) ;; *) die_code CADDY_UNSUPPORTED_SYSTEM "Unsupported Debian version ${raw_version}." ;; esac
      ;;
    rhel|rocky|almalinux|ol)
      detected_os_version="$(normalize_major_version "${raw_version}")"
      case "${detected_os_version}" in 8|9|10) ;; *) die_code CADDY_UNSUPPORTED_SYSTEM "Unsupported ${detected_os_id} version ${raw_version}." ;; esac
      ;;
    centos)
      detected_os_version="$(normalize_major_version "${raw_version}")"
      [[ "${detected_os_version}" == "7" ]] || die_code CADDY_UNSUPPORTED_SYSTEM "Only CentOS 7 is supported."
      ;;
    centos-stream)
      detected_os_version="$(normalize_major_version "${raw_version}")"
      case "${detected_os_version}" in 8|9|10) ;; *) die_code CADDY_UNSUPPORTED_SYSTEM "Unsupported CentOS Stream version ${raw_version}." ;; esac
      ;;
    fedora) detected_os_version="${raw_version}" ;;
    amzn)
      detected_os_version="$(normalize_major_version "${raw_version}")"
      [[ "${detected_os_version}" == "2023" ]] || die_code CADDY_UNSUPPORTED_SYSTEM "Only Amazon Linux 2023 is supported."
      ;;
    sles)
      detected_os_version="$(normalize_major_version "${raw_version}")"
      case "${detected_os_version}" in 15|16) ;; *) die_code CADDY_UNSUPPORTED_SYSTEM "Unsupported SLES version ${raw_version}." ;; esac
      ;;
    opensuse-leap|opensuse-tumbleweed|opensuse) detected_os_version="${raw_version}" ;;
    *) die_code CADDY_UNSUPPORTED_SYSTEM "Unsupported operating system ${detected_os_id}." ;;
  esac
  case "$(uname -m)" in
    x86_64|amd64) detected_architecture="amd64" ;;
    aarch64|arm64) detected_architecture="arm64" ;;
    *) die_code CADDY_UNSUPPORTED_ARCHITECTURE "Only amd64 and arm64 are supported." ;;
  esac
}

require_commands() {
  local required=(awk chmod cmp cp dirname find getent grep groupadd id install mkdir mktemp mv rm sed sha256sum sort stat systemctl tar test uname useradd)
  [[ "${install_mode}" != "center" ]] || required+=(curl)
  local name
  for name in "${required[@]}"; do command_exists "${name}" || die_code CADDY_MISSING_TOOL "Required command ${name} is unavailable."; done
}

offline_package_root() {
  printf '%s/packages/%s/%s/%s' "${offline_package_path}" "${detected_os_id}" "${detected_os_version}" "${detected_architecture}"
}

validate_offline_acl_package() {
  local package_root
  package_root="$(offline_package_root)"
  case "${detected_os_id}" in
    ubuntu|debian)
      find "${package_root}" -maxdepth 1 -type f -name 'acl_*.deb' -print -quit 2>/dev/null | grep -q . ||
        die_code CADDY_OFFLINE_BUNDLE_INVALID "Offline Bundle is missing the ACL utility package for ${detected_os_id} ${detected_os_version} ${detected_architecture}."
      ;;
    *)
      find "${package_root}" -maxdepth 1 -type f -name 'acl-[0-9]*.rpm' -print -quit 2>/dev/null | grep -q . ||
        die_code CADDY_OFFLINE_BUNDLE_INVALID "Offline Bundle is missing the ACL utility package for ${detected_os_id} ${detected_os_version} ${detected_architecture}."
      ;;
  esac
}

artifact_metadata() {
  artifact_file="caddy_${software_version}_linux_${detected_architecture}.tar.gz"
  artifact_url="https://github.com/caddyserver/caddy/releases/download/v${software_version}/${artifact_file}"
  case "${detected_architecture}" in
    amd64) artifact_sha256="5c218bc34c9197369263da7e9317a83acdbd80ef45d94dca5eff76e727c67cdd" ;;
    arm64) artifact_sha256="501e955fa634c5aab63247458c3ac655cfdd6cbf1e0436528f41248451c190ac" ;;
    *) die_code CADDY_UNSUPPORTED_ARCHITECTURE "No artifact is declared for ${detected_architecture}." ;;
  esac
}

verify_sha256() {
  local path="$1" expected="$2" actual
  [[ -f "${path}" && ! -L "${path}" ]] || die_code CADDY_ARTIFACT_MISSING "Artifact is missing or is a symlink: ${path}."
  actual="$(sha256sum "${path}" | awk '{print $1}')"
  [[ "${actual}" == "${expected}" ]] || die_code CADDY_ARTIFACT_CHECKSUM_MISMATCH "SHA-256 verification failed for $(basename -- "${path}")."
}

validate_bundle_file_set() {
  local bundle="$1" listed actual
  listed="$(mktemp "${state_dir}/.caddy-bundle-listed.XXXXXX")"
  actual="$(mktemp "${state_dir}/.caddy-bundle-actual.XXXXXX")"
  awk '{print $2}' "${bundle}/files.sha256" | sed 's#^\*##' | sort >"${listed}"
  if grep -Eq '(^/|(^|/)\.\.(/|$)|[[:cntrl:]])' "${listed}"; then
    rm -f -- "${listed}" "${actual}"
    die_code CADDY_OFFLINE_BUNDLE_INVALID "files.sha256 contains an unsafe path."
  fi
  (cd -- "${bundle}" && find . -type f ! -name files.sha256 -print | sed 's#^./##' | sort) >"${actual}"
  if ! cmp -s -- "${listed}" "${actual}"; then
    rm -f -- "${listed}" "${actual}"
    die_code CADDY_OFFLINE_BUNDLE_INVALID "files.sha256 does not cover the complete Bundle file set."
  fi
  rm -f -- "${listed}" "${actual}"
}

resolve_offline_artifact() {
  [[ "${offline_package_path}" == /* && -d "${offline_package_path}" ]] || die_code CADDY_OFFLINE_BUNDLE_INVALID "ONEINSTACK_OFFLINE_PACKAGE_PATH must be an absolute Bundle directory."
  [[ ! -L "${offline_package_path}" ]] || die_code CADDY_OFFLINE_BUNDLE_INVALID "Offline Bundle root cannot be a symlink."
  if find "${offline_package_path}" -type l -print -quit | grep -q .; then die_code CADDY_OFFLINE_BUNDLE_INVALID "Offline Bundle cannot contain symlinks."; fi
  for path in manifest.yaml bundle-info files.sha256 scripts/common.sh scripts/install.sh scripts/verify.sh; do
    [[ -f "${offline_package_path}/${path}" ]] || die_code CADDY_OFFLINE_BUNDLE_INVALID "Offline Bundle is missing ${path}."
  done
  grep -Fxq "component=${component_id}" "${offline_package_path}/bundle-info" || die_code CADDY_OFFLINE_BUNDLE_MISMATCH "Offline Bundle component does not match Caddy."
  grep -Fxq "packageVersion=${package_version}" "${offline_package_path}/bundle-info" || die_code CADDY_OFFLINE_BUNDLE_MISMATCH "Offline Bundle package version does not match ${package_version}."
  grep -Fxq "softwareVersion=${software_version}" "${offline_package_path}/bundle-info" || die_code CADDY_OFFLINE_BUNDLE_MISMATCH "Offline Bundle software version does not match ${software_version}."
  grep -Fxq "osId=${detected_os_id}" "${offline_package_path}/bundle-info" || die_code CADDY_OFFLINE_BUNDLE_MISMATCH "Offline Bundle OS does not match ${detected_os_id}."
  grep -Fxq "osVersion=${detected_os_version}" "${offline_package_path}/bundle-info" || die_code CADDY_OFFLINE_BUNDLE_MISMATCH "Offline Bundle OS version does not match ${detected_os_version}."
  grep -Fxq "architecture=${detected_architecture}" "${offline_package_path}/bundle-info" || die_code CADDY_OFFLINE_BUNDLE_MISMATCH "Offline Bundle architecture does not match ${detected_architecture}."
  grep -Eq '^[[:space:]]+id:[[:space:]]+caddy[[:space:]]*$' "${offline_package_path}/manifest.yaml" || die_code CADDY_OFFLINE_BUNDLE_MISMATCH "Offline manifest component does not match Caddy."
  grep -Eq '^[[:space:]]+version:[[:space:]]+1\.0\.18[[:space:]]*$' "${offline_package_path}/manifest.yaml" || die_code CADDY_OFFLINE_BUNDLE_MISMATCH "Offline manifest package version does not match 1.0.18."
  validate_bundle_file_set "${offline_package_path}"
  (cd -- "${offline_package_path}" && sha256sum -c files.sha256 >/dev/null) || die_code CADDY_OFFLINE_BUNDLE_CHECKSUM_MISMATCH "Offline Bundle checksum verification failed."
  validate_offline_acl_package
  local path="${offline_package_path}/artifacts/${detected_architecture}/${artifact_file}"
  verify_sha256 "${path}" "${artifact_sha256}"
  printf '%s' "${path}"
}

resolve_online_artifact() {
  local cache_dir="/var/cache/oneinstack/caddy/${software_version}" cache_path temporary
  install -d -m 0755 -- "${cache_dir}"
  cache_path="${cache_dir}/${artifact_file}"
  if [[ -f "${cache_path}" ]] && [[ "$(sha256sum "${cache_path}" | awk '{print $1}')" == "${artifact_sha256}" ]]; then
    log "Using verified Caddy artifact cache ${cache_path}." >&2
    printf '%s' "${cache_path}"
    return 0
  fi
  temporary="$(mktemp "${cache_dir}/.${artifact_file}.XXXXXX")"
  if ! curl --fail --location --proto '=https' --tlsv1.2 --connect-timeout 20 --retry 3 --output "${temporary}" "${artifact_url}"; then
    rm -f -- "${temporary}"
    die_code CADDY_DOWNLOAD_FAILED "Unable to download the fixed official Caddy artifact."
  fi
  verify_sha256 "${temporary}" "${artifact_sha256}"
  chmod 0644 "${temporary}"
  mv -f -- "${temporary}" "${cache_path}"
  printf '%s' "${cache_path}"
}

resolve_artifact() {
  artifact_metadata
  if [[ "${install_mode}" == "offline" ]]; then resolve_offline_artifact; else resolve_online_artifact; fi
}

ensure_account() {
  getent group "${run_group}" >/dev/null || groupadd --system "${run_group}"
  if ! id "${run_user}" >/dev/null 2>&1; then
    local nologin="/usr/sbin/nologin"
    [[ -x "${nologin}" ]] || nologin="/sbin/nologin"
    useradd --system --gid "${run_group}" --home-dir "${data_dir}" --shell "${nologin}" "${run_user}"
  fi
  [[ "$(id -gn "${run_user}")" == "${run_group}" ]] || die_code CADDY_ACCOUNT_CONFLICT "Existing caddy account does not use the caddy primary group."
}

active_unit() { systemd_available && systemctl is-active --quiet "$1"; }

legacy_caddy_service_matches() {
  [[ -f /etc/systemd/system/caddy.service ]] || return 1
  grep -Fq "${binary}" /etc/systemd/system/caddy.service
}

active_foreign_web_server() {
  local unit
  for unit in oneinstack-nginx.service oneinstack-tengine.service oneinstack-openresty.service oneinstack-httpd.service nginx.service httpd.service apache2.service openresty.service tengine.service; do
    if active_unit "${unit}"; then printf '%s' "${unit}"; return 0; fi
  done
  if active_unit caddy.service && ! legacy_caddy_service_matches; then printf '%s' caddy.service; return 0; fi
  return 1
}

port_listening() {
  if command_exists ss; then
    ss -ltnH 2>/dev/null | awk -v wanted="${caddy_port}" '{address=$4; sub(/^.*:/,"",address); if (address == wanted) found=1} END {exit found ? 0 : 1}'
    return
  fi
  local hex
  printf -v hex '%04X' "${caddy_port}"
  awk -v wanted="${hex}" '$4 == "0A" {split($2,a,":"); if (toupper(a[2]) == wanted) found=1} END {exit found ? 0 : 1}' /proc/net/tcp /proc/net/tcp6 2>/dev/null
}

wait_for_listener() {
  local attempts="${1:-15}"
  while ((attempts > 0)); do
    port_listening && return 0
    sleep 1
    attempts=$((attempts - 1))
  done
  return 1
}

precheck_runtime_conflicts() {
  local active
  if [[ -f "${unit_file}" ]] && ! grep -Fq "${binary}" "${unit_file}"; then
    die_code CADDY_UNMANAGED_CONFLICT "${unit_file} does not belong to the managed /usr/local/caddy runtime."
  fi
  if [[ -d "${install_dir}" && ! -f "${unit_file}" && ! -f "${installed_state_file}" ]] && ! legacy_caddy_service_matches; then
    die_code CADDY_UNMANAGED_CONFLICT "${install_dir} exists without a recognized managed Caddy service or component state."
  fi
  active="$(active_foreign_web_server || true)"
  [[ -z "${active}" ]] || die_code CADDY_WEB_SERVER_ACTIVE "Active Web Server ${active} conflicts with Caddy runtime ownership."
  if port_listening && ! active_unit "${service_name}.service" && ! { active_unit caddy.service && legacy_caddy_service_matches; }; then
    die_code CADDY_PORT_IN_USE "TCP port ${caddy_port} is already listening."
  fi
}

systemd_version() { systemctl --version | awk 'NR == 1 {print $2+0}'; }

install_centos7_libcap_offline() {
  command_exists rpm || die_code CADDY_CAPABILITY_TOOL_MISSING "rpm is required to install the bundled CentOS 7 libcap package."
  local package_root="${offline_package_path}/packages/${detected_os_id}/${detected_os_version}/${detected_architecture}"
  local packages=()
  mapfile -t packages < <(find "${package_root}" -maxdepth 1 -type f -name 'libcap*.rpm' -print 2>/dev/null | sort)
  [[ "${#packages[@]}" -gt 0 ]] || die_code CADDY_CAPABILITY_TOOL_MISSING "CentOS 7 requires setcap; add the matching libcap RPM to this offline Bundle."
  rpm -Uvh --replacepkgs -- "${packages[@]}" || die_code CADDY_CAPABILITY_TOOL_MISSING "Bundled CentOS 7 libcap RPM installation failed."
}

ensure_low_port_capability() {
  if [[ "$(systemd_version)" -ge 229 ]]; then return 0; fi
  if ! command_exists setcap; then
    if [[ "${install_mode}" == "offline" ]]; then
      install_centos7_libcap_offline
    elif command_exists yum; then
      yum -y install libcap || die_code CADDY_CAPABILITY_TOOL_MISSING "Unable to install libcap for CentOS 7 low-port binding."
    else
      die_code CADDY_CAPABILITY_TOOL_MISSING "setcap is required by this legacy systemd host."
    fi
  fi
  setcap cap_net_bind_service=+ep "${binary}" || die_code CADDY_CAPABILITY_FAILED "Unable to grant Caddy low-port capability."
  command_exists getcap || die_code CADDY_CAPABILITY_TOOL_MISSING "getcap is required to verify the Caddy low-port capability."
  getcap "${binary}" | grep -Fq cap_net_bind_service || die_code CADDY_CAPABILITY_FAILED "Caddy low-port capability verification failed."
}

php_fpm_service_active() {
  [[ -S "${php_fpm_socket}" ]] && return 0
  systemd_available || return 1
  active_unit php-fpm.service && return 0
  systemctl list-units --type=service --state=active --no-legend \
    'php*-fpm.service' 'php-fpm-*.service' 2>/dev/null | grep -q '[^[:space:]]' && return 0
  return 1
}

php_fpm_runtime_group() {
  local socket="${1:-${php_fpm_socket}}" group="" configured_socket="" configured_group=""
  local parameters="${state_root}/php/runtime-params" key value
  if [[ -S "${socket}" ]]; then
    group="$(stat -Lc '%G' -- "${socket}" 2>/dev/null || true)"
  elif [[ -r "${parameters}" ]]; then
    while IFS='=' read -r key value; do
      case "${key}" in
        socket-path) configured_socket="${value}" ;;
        run-group) configured_group="${value}" ;;
      esac
    done <"${parameters}"
    [[ "${configured_socket}" != "${socket}" ]] || group="${configured_group}"
  fi
  if [[ -z "${group}" && "${socket}" == "/dev/shm/php-cgi.sock" ]] && getent group www >/dev/null; then
    group="www"
  fi
  [[ -n "${group}" && "${group}" != "${run_group}" && "${group}" =~ ^[A-Za-z0-9_.-]+$ ]] || return 1
  getent group "${group}" >/dev/null || return 1
  printf '%s\n' "${group}"
}

phpmyadmin_detected() {
  [[ -f "${state_root}/phpmyadmin/installed.json" ]] && return 0
  local candidate
  for candidate in "${web_root}/default/phpMyAdmin" "${web_root}/phpMyAdmin" /data/wwwroot/default/phpMyAdmin /data/wwwroot/phpMyAdmin; do
    [[ -e "${candidate}" || -L "${candidate}" ]] && return 0
  done
  return 1
}

prepare_phpmyadmin_link() {
  local source="${web_root}/phpMyAdmin" target="${web_root}/default/phpMyAdmin"
  [[ -e "${source}" && ! -e "${target}" && ! -L "${target}" ]] || return 0
  ln -s -- "${source}" "${target}"
}

render_managed_config() {
  local destination="$1" port="$2" socket="$3" root="$4" logs="$5"
  {
    printf 'http://:%s {\n' "${port}"
    printf '    root * %s/default\n' "${root}"
    # PHP must never fall through to file_server, even when PHP-FPM is stopped
    # or installed after Caddy. An unavailable backend then fails closed with a
    # gateway error instead of exposing PHP source as a static download.
    printf '    php_fastcgi unix//%s\n' "${socket#/}"
    printf '    file_server\n'
    printf '    log {\n        output file %s/caddy/caddy-access.log\n    }\n' "${logs}"
    printf '}\n'
  } >"${destination}"
  chmod 0640 "${destination}"
  chown root:"${run_group}" "${destination}"
}

render_main_config() {
  local destination="$1" managed_path="${2:-${managed_config}}"
  {
    printf '{\n    admin 127.0.0.1:2019\n}\n'
    printf 'import %s\n' "${managed_path}"
    printf 'import %s/*.conf\n' "${vhost_dir}"
  } >"${destination}"
  chmod 0640 "${destination}"
  chown root:"${run_group}" "${destination}"
}

run_as_caddy() {
  if command_exists runuser; then runuser -u "${run_user}" -- "$@"; else su -s /bin/sh -c "$(printf '%q ' "$@")" "${run_user}"; fi
}

install_acl_tools() {
  command_exists getfacl && command_exists setfacl && return 0
  if [[ "${install_mode}" == "offline" ]]; then
    local package_root package
    package_root="$(offline_package_root)"
    case "${detected_os_id}" in
      ubuntu|debian)
        command_exists dpkg || die_code CADDY_ACL_TOOL_MISSING "dpkg is required to install the bundled ACL utility package."
        package="$(find "${package_root}" -maxdepth 1 -type f -name 'acl_*.deb' -print -quit 2>/dev/null || true)"
        [[ -n "${package}" ]] || die_code CADDY_ACL_TOOL_MISSING "Offline Bundle does not contain the ACL utility package."
        dpkg -i -- "${package}" || die_code CADDY_ACL_TOOL_MISSING "Bundled ACL utility package installation failed."
        ;;
      *)
        command_exists rpm || die_code CADDY_ACL_TOOL_MISSING "rpm is required to install the bundled ACL utility package."
        package="$(find "${package_root}" -maxdepth 1 -type f -name 'acl-[0-9]*.rpm' -print -quit 2>/dev/null || true)"
        [[ -n "${package}" ]] || die_code CADDY_ACL_TOOL_MISSING "Offline Bundle does not contain the ACL utility package."
        rpm -Uvh --replacepkgs -- "${package}" || die_code CADDY_ACL_TOOL_MISSING "Bundled ACL utility package installation failed."
        ;;
    esac
  else
    case "${detected_os_id}" in
      ubuntu|debian)
        command_exists apt-get || die_code CADDY_ACL_TOOL_MISSING "apt-get is required to install ACL utilities."
        DEBIAN_FRONTEND=noninteractive apt-get -y install acl || die_code CADDY_ACL_TOOL_MISSING "Unable to install ACL utilities."
        ;;
      centos)
        command_exists yum || die_code CADDY_ACL_TOOL_MISSING "yum is required to install ACL utilities."
        yum -y install acl || die_code CADDY_ACL_TOOL_MISSING "Unable to install ACL utilities."
        ;;
      rhel|rocky|almalinux|ol|centos-stream|fedora|amzn)
        command_exists dnf || die_code CADDY_ACL_TOOL_MISSING "dnf is required to install ACL utilities."
        dnf -y install acl || die_code CADDY_ACL_TOOL_MISSING "Unable to install ACL utilities."
        ;;
      sles|opensuse-leap|opensuse-tumbleweed|opensuse)
        command_exists zypper || die_code CADDY_ACL_TOOL_MISSING "zypper is required to install ACL utilities."
        zypper --non-interactive install acl || die_code CADDY_ACL_TOOL_MISSING "Unable to install ACL utilities."
        ;;
      *) die_code CADDY_ACL_TOOL_MISSING "ACL utilities are unavailable on this host." ;;
    esac
  fi
  if ! command_exists getfacl || ! command_exists setfacl; then
    die_code CADDY_ACL_TOOL_MISSING "ACL utilities remain unavailable after package installation."
  fi
}

record_managed_path_acl() {
  local acl_path="$1" record
  record="${run_user}"$'\t'"${acl_path}"
  install -d -m 0750 -- "${state_dir}"
  if [[ ! -f "${managed_path_acl_file}" ]] || ! grep -Fqx -- "${record}" "${managed_path_acl_file}"; then
    printf '%s\n' "${record}" >>"${managed_path_acl_file}"
    chmod 0600 "${managed_path_acl_file}"
  fi
  if [[ -n "${path_acl_transaction_file}" ]] &&
    { [[ ! -f "${path_acl_transaction_file}" ]] || ! grep -Fqx -- "${record}" "${path_acl_transaction_file}"; }; then
    printf '%s\n' "${record}" >>"${path_acl_transaction_file}"
    chmod 0600 "${path_acl_transaction_file}"
  fi
}

ensure_runtime_path_traversal() {
  local managed_path="$1" error_code="$2" description="$3"
  local current_path index current_acl access_mask existing_user_permissions
  local -a path_chain=()
  current_path="${managed_path%/}"
  while [[ "${current_path}" != "/" ]]; do
    path_chain+=("${current_path}")
    current_path="$(dirname -- "${current_path}")"
  done
  for ((index=${#path_chain[@]}; index > 0; index--)); do
    current_path="${path_chain[index - 1]}"
    [[ -d "${current_path}" && ! -L "${current_path}" ]] ||
      die_code "${error_code}" "${description} contains a missing or symbolic-link directory: ${current_path}."
    if run_as_caddy test -x "${current_path}"; then
      continue
    fi
    install_acl_tools
    current_acl="$(getfacl -cp -- "${current_path}")" ||
      die_code "${error_code}" "Unable to inspect the ACL for ${current_path}."
    existing_user_permissions="$(awk -F: -v user="${run_user}" '$1 == "user" && $2 == user {print $3; exit}' <<<"${current_acl}")"
    [[ -z "${existing_user_permissions}" ]] ||
      die_code "${error_code}" "Existing ACL entry for ${run_user} on ${current_path} does not permit traversal and was preserved."
    access_mask="$(awk -F: '$1 == "mask" && $2 == "" {print $3; exit}' <<<"${current_acl}")"
    if [[ -n "${access_mask}" ]]; then
      [[ "${access_mask}" == *x* ]] ||
        die_code "${error_code}" "Existing ACL mask on ${current_path} denies traversal; refusing to broaden unrelated ACL access."
      setfacl --no-mask -m "u:${run_user}:--x" -- "${current_path}" ||
        die_code "${error_code}" "Unable to grant Caddy traverse access to ${current_path}."
    else
      setfacl -m "u:${run_user}:--x" -- "${current_path}" ||
        die_code "${error_code}" "Unable to grant Caddy traverse access to ${current_path}."
    fi
    record_managed_path_acl "${current_path}"
    run_as_caddy test -x "${current_path}" ||
      die_code "${error_code}" "Caddy user still cannot traverse ${current_path} after applying the managed ACL."
  done
}

remove_managed_path_acl_entries() {
  local records_file="$1" acl_user acl_path current_permissions
  [[ -f "${records_file}" ]] || return 0
  if ! command_exists getfacl || ! command_exists setfacl; then
    warn "CADDY_ACL_RESTORE_SKIPPED: ACL utilities are unavailable; managed path ACL entries were preserved."
    return 0
  fi
  while IFS=$'\t' read -r acl_user acl_path; do
    [[ "${acl_user}" == "${run_user}" && "${acl_path}" == /* && "${acl_path}" != "/" && -d "${acl_path}" ]] || continue
    current_permissions="$(getfacl -cp -- "${acl_path}" 2>/dev/null | awk -F: -v user="${acl_user}" '$1 == "user" && $2 == user {print $3; exit}' || true)"
    if [[ "${current_permissions}" == "--x" ]]; then
      setfacl -x "u:${acl_user}" -- "${acl_path}" || warn "CADDY_ACL_RESTORE_FAILED: unable to remove the managed ACL entry from ${acl_path}."
    elif [[ -n "${current_permissions}" ]]; then
      warn "CADDY_ACL_CHANGED_EXTERNALLY: ACL entry for ${acl_user} on ${acl_path} was changed externally and was preserved."
    fi
  done <"${records_file}"
}

restore_path_acl_transaction() {
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

validate_caddy_config() {
  local path="${1:-${caddyfile}}"
  run_as_caddy "${binary}" validate --config "${path}" --adapter caddyfile
}

write_service_unit() {
  local capability_lines="" php_group="" supplementary_group_line=""
  if [[ "$(systemd_version)" -ge 229 ]]; then
    capability_lines=$'AmbientCapabilities=CAP_NET_BIND_SERVICE\nCapabilityBoundingSet=CAP_NET_BIND_SERVICE'
  fi
  php_group="$(php_fpm_runtime_group "${php_fpm_socket}" || true)"
  [[ -z "${php_group}" ]] || supplementary_group_line="SupplementaryGroups=${php_group}"
  cat >"${unit_file}" <<EOF
[Unit]
Description=OneinStack Caddy web server
Documentation=https://caddyserver.com/docs/
After=network-online.target
Wants=network-online.target

[Service]
Type=notify
User=${run_user}
Group=${run_group}
${supplementary_group_line}
ExecStart=${binary} run --environ --config ${caddyfile} --adapter caddyfile
ExecReload=${binary} reload --config ${caddyfile} --adapter caddyfile
TimeoutStartSec=30
TimeoutStopSec=30
Restart=on-failure
LimitNOFILE=1048576
PrivateTmp=true
ProtectSystem=full
ReadWritePaths=${data_dir} ${log_dir} ${state_dir}
${capability_lines}

[Install]
WantedBy=multi-user.target
EOF
  chmod 0644 "${unit_file}"
  systemctl daemon-reload
}

prepare_log_directory() {
  local directory="$1" component_log_dir="${1}/caddy"
  ensure_shared_directory "${directory}" 0755
  [[ ! -L "${component_log_dir}" ]] || die_code CADDY_LOG_PATH_INVALID "Caddy log directory cannot be a symlink."
  install -d -m 0750 -o "${run_user}" -g "${run_group}" -- "${component_log_dir}"
  ensure_runtime_path_traversal "${directory}" CADDY_LOG_PATH_PERMISSION_DENIED "Caddy log path"
  run_as_caddy test -x "${directory}" || die_code CADDY_LOG_PATH_PERMISSION_DENIED "Caddy user cannot traverse the log root: ${directory}."
  run_as_caddy test -w "${component_log_dir}" || die_code CADDY_LOG_PATH_PERMISSION_DENIED "Caddy user cannot write the component log directory: ${component_log_dir}."
}

prepare_web_directory() {
  local directory="$1" default_directory="${1}/default"
  ensure_shared_directory "${directory}" 0755
  ensure_shared_directory "${default_directory}" 0755
  ensure_runtime_path_traversal "${default_directory}" CADDY_WEB_PATH_PERMISSION_DENIED "Caddy web path"
}

ensure_shared_directory() {
  local directory="$1" mode="$2"
  [[ ! -L "${directory}" ]] || die_code CADDY_SHARED_PATH_INVALID "Shared directory cannot be a symlink: ${directory}."
  [[ -d "${directory}" ]] || install -d -m "${mode}" -- "${directory}"
}

prepare_directories() {
  ensure_account
  install -d -m 0755 -- "${install_dir}/bin" "${config_dir}"
  prepare_web_directory "${web_root}"
  install -d -m 0750 -o "${run_user}" -g "${run_group}" -- "${data_dir}"
  prepare_log_directory "${log_dir}"
  install -d -m 0750 -o root -g "${run_group}" -- "${vhost_dir}" "${state_dir}"
  [[ -f "${vhost_dir}/00-oneinstack.conf" ]] || printf '# Reserved managed vhost include.\n' >"${vhost_dir}/00-oneinstack.conf"
  chmod 0640 "${vhost_dir}/00-oneinstack.conf"
  chown root:"${run_group}" "${vhost_dir}/00-oneinstack.conf"
  prepare_phpmyadmin_link
}

install_binary_from_artifact() {
  local artifact="$1" extract_dir candidate version_output
  extract_dir="$(mktemp -d "${state_dir}/.caddy-extract.XXXXXX")"
  tar -xzf "${artifact}" -C "${extract_dir}" --no-same-owner
  candidate="$(find "${extract_dir}" -maxdepth 2 -type f -name caddy -print -quit)"
  [[ -n "${candidate}" ]] || { rm -rf -- "${extract_dir}"; die_code CADDY_ARTIFACT_INVALID "Official archive does not contain the Caddy binary."; }
  chmod 0755 "${candidate}"
  version_output="$("${candidate}" version 2>/dev/null | head -n1 || true)"
  [[ "${version_output}" =~ ^v?2\.10\.2([[:space:]]|$) ]] || { rm -rf -- "${extract_dir}"; die_code CADDY_VERSION_MISMATCH "Artifact reports unexpected version: ${version_output}."; }
  install -m 0755 -- "${candidate}" "${binary}"
  rm -rf -- "${extract_dir}"
}

stop_managed_caddy() {
  if active_unit "${service_name}.service"; then systemctl stop "${service_name}.service"; fi
  if active_unit caddy.service && legacy_caddy_service_matches; then systemctl stop caddy.service; fi
}

start_managed_caddy() {
  systemctl enable "${service_name}.service" >/dev/null
  systemctl restart "${service_name}.service"
}

reload_managed_caddy() {
  active_unit "${service_name}.service" || return 0
  systemctl reload "${service_name}.service"
}

current_version() {
  [[ -x "${binary}" ]] || return 1
  "${binary}" version 2>/dev/null | awk 'NR == 1 {sub(/^v/, "", $1); print $1; exit}'
}

config_revision() {
  [[ -r "${caddyfile}" && -r "${managed_config}" ]] || return 1
  { sha256sum "${caddyfile}"; sha256sum "${managed_config}"; } | sha256sum | awk '{print $1}'
}

persist_install_parameters() {
  install -d -m 0750 -o root -g "${run_group}" -- "${state_dir}"
  local candidate
  candidate="$(mktemp "${state_dir}/.install-parameters.XXXXXX")"
  {
    printf 'software-version=%s\n' "${software_version}"
    printf 'port=%s\n' "${caddy_port}"
    printf 'php-fpm-socket=%s\n' "${php_fpm_socket}"
    printf 'install-dir=%s\n' "${install_dir}"
    printf 'web-root=%s\n' "${web_root}"
    printf 'log-dir=%s\n' "${log_dir}"
    printf 'web-vhost-root=%s\n' "${web_vhost_root}"
    printf 'run-user=%s\nrun-group=%s\n' "${run_user}" "${run_group}"
    printf 'data-dir=%s\nservice-name=%s\ninstall-mode=%s\n' "${data_dir}" "${service_name}" "${install_mode}"
  } >"${candidate}"
  chmod 0640 "${candidate}"
  chown root:"${run_group}" "${candidate}"
  mv -f -- "${candidate}" "${install_parameters_file}"
}

write_installed_state() {
  local candidate actual_version revision
  actual_version="$(current_version)"
  revision="$(config_revision)"
  candidate="$(mktemp "${state_dir}/.installed.XXXXXX")"
  printf '{"component":"caddy","packageVersion":"%s","softwareVersion":"%s","actualVersion":"%s","service":"%s","port":%s,"installMode":"%s","architecture":"%s","osId":"%s","osVersion":"%s","revision":"%s"}\n' \
    "${package_version}" "${software_version}" "${actual_version}" "${service_name}" "${caddy_port}" "${install_mode}" "${detected_architecture}" "${detected_os_id}" "${detected_os_version}" "${revision}" >"${candidate}"
  chmod 0640 "${candidate}"
  chown root:"${run_group}" "${candidate}"
  mv -f -- "${candidate}" "${installed_state_file}"
}

prepare_rollback() {
  install -d -m 0700 -- "${state_dir}/rollbacks"
  local snapshot
  snapshot="$(mktemp -d "${state_dir}/rollbacks/install-$(date -u +%Y%m%dT%H%M%SZ)-XXXXXX")"
  path_acl_transaction_file="${snapshot}/path-acl-added"
  : >"${path_acl_transaction_file}"
  chmod 0600 "${path_acl_transaction_file}"
  if [[ -d "${install_dir}" ]]; then cp -a -- "${install_dir}" "${snapshot}/install"; fi
  if [[ -f "${unit_file}" ]]; then cp -a -- "${unit_file}" "${snapshot}/unit"; fi
  if [[ -f "${installed_state_file}" ]]; then cp -a -- "${installed_state_file}" "${snapshot}/installed.json"; fi
  if [[ -f "${install_parameters_file}" ]]; then cp -a -- "${install_parameters_file}" "${snapshot}/install-parameters"; fi
  [[ ! -d "${data_dir}" ]] || : >"${snapshot}/had-data-dir"
  active_unit "${service_name}.service" && : >"${snapshot}/was-active"
  systemctl is-enabled --quiet "${service_name}.service" 2>/dev/null && : >"${snapshot}/was-enabled"
  if legacy_caddy_service_matches; then
    active_unit caddy.service && : >"${snapshot}/legacy-was-active"
    systemctl is-enabled --quiet caddy.service 2>/dev/null && : >"${snapshot}/legacy-was-enabled"
  fi
  printf '%s\n' "${snapshot}" >"${rollback_pointer}"
  printf '%s' "${snapshot}"
}

restore_rollback_snapshot() {
  [[ -r "${rollback_pointer}" ]] || die_code CADDY_ROLLBACK_UNAVAILABLE "No Caddy rollback snapshot is available."
  local snapshot failed=0
  read -r snapshot <"${rollback_pointer}"
  [[ "${snapshot}" == "${state_dir}/rollbacks/"* && -d "${snapshot}" ]] || die_code CADDY_ROLLBACK_UNAVAILABLE "Rollback snapshot path is invalid."
  set +e
  stop_managed_caddy || failed=1
  rm -rf -- "${install_dir}" || failed=1
  rm -f -- "${unit_file}" "${installed_state_file}" "${install_parameters_file}" || failed=1
  if [[ -d "${snapshot}/install" ]]; then cp -a -- "${snapshot}/install" "${install_dir}" || failed=1; fi
  if [[ -f "${snapshot}/unit" ]]; then cp -a -- "${snapshot}/unit" "${unit_file}" || failed=1; fi
  if [[ -f "${snapshot}/installed.json" ]]; then cp -a -- "${snapshot}/installed.json" "${installed_state_file}" || failed=1; fi
  if [[ -f "${snapshot}/install-parameters" ]]; then cp -a -- "${snapshot}/install-parameters" "${install_parameters_file}" || failed=1; fi
  if [[ ! -f "${snapshot}/had-data-dir" ]]; then rm -rf -- "${data_dir}" || failed=1; fi
  restore_path_acl_transaction "${snapshot}/path-acl-added" || failed=1
  systemctl daemon-reload || failed=1
  if [[ -f "${snapshot}/was-enabled" && -f "${unit_file}" ]]; then systemctl enable "${service_name}.service" >/dev/null 2>&1 || failed=1; fi
  if [[ -f "${snapshot}/was-active" && -f "${unit_file}" ]]; then systemctl start "${service_name}.service" || failed=1; fi
  if [[ -f "${snapshot}/legacy-was-enabled" && -f /etc/systemd/system/caddy.service ]]; then systemctl enable caddy.service >/dev/null 2>&1 || failed=1; fi
  if [[ -f "${snapshot}/legacy-was-active" && -f /etc/systemd/system/caddy.service ]]; then systemctl start caddy.service || failed=1; fi
  set -e
  [[ "${failed}" -eq 0 ]]
}

http_probe() {
  local path="$1" expected="$2" response
  if [[ "${install_mode}" != "offline" ]] && command_exists curl; then
    response="$(curl --proto '=http' --connect-timeout 5 --max-time 10 --silent --show-error "http://127.0.0.1:${caddy_port}${path}" 2>/dev/null || true)"
  else
    response="$(
      exec 9<>"/dev/tcp/127.0.0.1/${caddy_port}" || exit 1
      printf 'GET %s HTTP/1.0\r\nHost: 127.0.0.1\r\nConnection: close\r\n\r\n' "${path}" >&9
      cat <&9
    )" || return 2
  fi
  [[ "${response}" == *"${expected}"* ]]
}

verify_http() {
  local token="oneinstack-caddy-${RANDOM}-$$" probe="${web_root}/default/.oneinstack-caddy-probe"
  printf '%s\n' "${token}" >"${probe}"
  chmod 0644 "${probe}"
  local result=0
  http_probe "/.oneinstack-caddy-probe" "${token}" || result=$?
  rm -f -- "${probe}"
  [[ "${result}" -eq 0 ]] || die_code CADDY_HTTP_PROBE_FAILED "Caddy HTTP probe failed on 127.0.0.1:${caddy_port}."
}

report_phpmyadmin_integration() {
  phpmyadmin_detected || return 0
  if ! php_fpm_service_active || [[ ! -S "${php_fpm_socket}" ]]; then
    warn "CADDY_PHPMYADMIN_INTEGRATION_PENDING: phpMyAdmin was detected, but PHP-FPM socket ${php_fpm_socket} is unavailable. Caddy installation succeeded; phpMyAdmin access was not verified."
    emit_progress 96 phpmyadmin_warning "CADDY_PHPMYADMIN_INTEGRATION_PENDING: phpMyAdmin detected without an available PHP-FPM service/socket"
    return 0
  fi
  local php_probe_file="${web_root}/default/.oneinstack-caddy-php-probe.php" token="oneinstack-caddy-php-${RANDOM}-$$"
  printf '<?php echo "%s";\n' "${token}" >"${php_probe_file}"
  chmod 0644 "${php_probe_file}"
  if ! http_probe "/.oneinstack-caddy-php-probe.php" "${token}"; then
    rm -f -- "${php_probe_file}"
    warn "CADDY_PHPMYADMIN_PHP_PROBE_FAILED: PHP-FPM is available, but Caddy PHP execution probe failed. Caddy installation succeeded; phpMyAdmin access was not verified."
    emit_progress 97 phpmyadmin_warning "CADDY_PHPMYADMIN_PHP_PROBE_FAILED: PHP execution probe failed"
    return 0
  fi
  rm -f -- "${php_probe_file}"
  if ! http_probe "/phpMyAdmin/index.php" "phpMyAdmin"; then
    warn "CADDY_PHPMYADMIN_HTTP_PROBE_FAILED: PHP execution succeeded, but /phpMyAdmin/index.php did not pass the HTTP probe. Caddy installation succeeded; phpMyAdmin access was not verified."
    emit_progress 98 phpmyadmin_warning "CADDY_PHPMYADMIN_HTTP_PROBE_FAILED: phpMyAdmin HTTP probe failed"
    return 0
  fi
  log "phpMyAdmin PHP execution and HTTP access were verified."
}

verify_runtime() {
  [[ -x "${binary}" ]] || die_code CADDY_NOT_INSTALLED "Caddy binary is unavailable."
  [[ "$(current_version)" == "${software_version}" ]] || die_code CADDY_VERSION_MISMATCH "Installed Caddy version does not match ${software_version}."
  [[ -r "${caddyfile}" && -r "${managed_config}" ]] || die_code CADDY_CONFIG_MISSING "Managed Caddy configuration is unavailable."
  validate_caddy_config "${caddyfile}" >/dev/null
  active_unit "${service_name}.service" || die_code CADDY_SERVICE_INACTIVE "${service_name}.service is not active."
  wait_for_listener 15 || die_code CADDY_LISTENER_MISSING "Caddy is active but port ${caddy_port} is not listening."
  verify_http
}

load_persisted_parameters() {
  [[ -r "${install_parameters_file}" ]] || return 0
  local key value
  while IFS='=' read -r key value; do
    case "${key}" in
      software-version) software_version="${value}" ;;
      port) caddy_port="${value}" ;;
      php-fpm-socket) php_fpm_socket="${value}" ;;
      web-root) web_root="${value}" ;;
      log-dir) log_dir="${value}" ;;
      install-mode) install_mode="${value}" ;;
    esac
  done <"${install_parameters_file}"
}
