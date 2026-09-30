#!/usr/bin/env bash
set -Eeuo pipefail
umask 027

component_id="fail2ban"
software_version="${SOFTWARE_VERSION:-1.1.1}"
install_mode="${FAIL2BAN_INSTALL_MODE:-center}"
offline_package_path="${FAIL2BAN_OFFLINE_PACKAGE_PATH:-}"
service_name="fail2ban"
state_root="${ONEINSTACK_COMPONENT_STATE:-/var/lib/oneinstack/components}"
state_dir="${state_root}/${component_id}"
installed_marker="${state_dir}/package-installed-by-oneinstack"
integration_marker="${state_dir}/integration-installed"
migration_dir="${state_dir}/migration"
event_root="/var/lib/oneinstack/fail2ban"
event_file="${event_root}/events.jsonl"
manual_log="${event_root}/manual.log"
helper_path="/usr/local/libexec/oneinstack-fail2ban-report"
action_path="/etc/fail2ban/action.d/oneinstack-report.conf"
filter_path="/etc/fail2ban/filter.d/oneinstack-manual.conf"
redis_auth_filter_path="/etc/fail2ban/filter.d/oneinstack-redis-auth.conf"
redis_acl_helper="/usr/local/libexec/oneinstack-redis-acl-log.py"
redis_acl_service="oneinstack-redis-acl-log.service"
redis_acl_unit="/etc/systemd/system/${redis_acl_service}"
redis_auth_log="/var/lib/oneinstack/fail2ban/redis-auth.log"
redis_acl_state="/var/lib/oneinstack/fail2ban/redis-acl-log.state"
redis_acl_lock="/var/lib/oneinstack/fail2ban/redis-acl-log.lock"
defaults_path="/etc/fail2ban/jail.d/90-oneinstack-defaults.local"
component_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
source_root="/usr/local/lib/oneinstack/fail2ban"
source_prefix="${source_root}/${software_version}"
source_bin_dir="${source_prefix}/bin"
source_python_lib_dir="${source_prefix}/lib/python3/site-packages"
source_version_marker="${state_dir}/source-version"
source_url=""
source_sha256=""
source_work_dir=""

die() { printf 'ERROR: %s\n' "$*" >&2; exit 1; }
emit_progress() {
  local percent="$1" code="$2" message="$3" fd="${ONEINSTACK_PROGRESS_FD:-}"
  [[ "${fd}" =~ ^[0-9]+$ ]] || return 0
  message="${message//\\/\\\\}"; message="${message//\"/\\\"}"; message="${message//$'\n'/ }"
  printf '{"type":"progress","percent":%s,"code":"%s","message":"%s"}\n' \
    "${percent}" "${code}" "${message}" 1>&"${fd}" 2>/dev/null || true
}
require_root() { [[ "$(id -u)" -eq 0 ]] || die "This action must run as root."; }
require_command() { command -v "$1" >/dev/null 2>&1 || die "Required command not found: $1"; }
cleanup_source_work_dir() {
  if [[ -n "${source_work_dir:-}" ]]; then
    rm -rf -- "${source_work_dir}"
    source_work_dir=""
  fi
}
source_prefix_for_version() { printf '%s/%s\n' "${source_root}" "$1"; }
select_source() {
  case "${software_version}" in
    1.1.1)
      source_url="https://github.com/fail2ban/fail2ban/archive/1.1.1.tar.gz"
      source_sha256="4be0ea0488e32de260058462a44a040f0542cd26a9fb6fa6d2514f9dd8ec1609"
      ;;
    1.0.2)
      source_url="https://github.com/fail2ban/fail2ban/archive/1.0.2.tar.gz"
      source_sha256="ae8b0b41f27a7be12d40488789d6c258029b23a01168e3c0d347ee80b325ac23"
      ;;
    system)
      source_url=""
      source_sha256=""
      ;;
    *) die "Unsupported Fail2ban software version: ${software_version}" ;;
  esac
}
source_python_path() {
  if [[ -d "${source_python_lib_dir}" ]]; then
    printf '%s\n' "${source_python_lib_dir}"
    return 0
  fi
  [[ -d "${source_prefix}/lib" ]] || return 1
  find "${source_prefix}/lib" -type d \( -name site-packages -o -name dist-packages \) -print -quit
}
activate_source_runtime() {
  [[ "${software_version}" != "system" && -d "${source_bin_dir}" ]] || return 0
  export PATH="${source_bin_dir}:${PATH}"
  local python_path
  python_path="$(source_python_path || true)"
  [[ -n "${python_path}" ]] || die "Fail2ban source runtime Python path is missing."
  export PYTHONPATH="${python_path}${PYTHONPATH:+:${PYTHONPATH}}"
}
installed_source_version() {
  if [[ -r "${source_version_marker}" ]]; then
    local version
    version="$(tr -d '[:space:]' <"${source_version_marker}")"
    case "${version}" in
      1.1.1|1.0.2)
        printf '%s\n' "${version}"
        return 0
        ;;
    esac
  fi
  return 1
}
activate_source_runtime
validate_inputs() {
  case "${software_version}" in
    1.1.1|1.0.2|system) ;;
    *) die "Unsupported Fail2ban software version: ${software_version}" ;;
  esac
  select_source
  case "${install_mode}" in
    center) ;;
    offline)
      [[ -n "${offline_package_path}" && "${offline_package_path}" == /* && "$(realpath -m -- "${offline_package_path}")" == "${offline_package_path}" ]] ||
        die "FAIL2BAN_OFFLINE_PACKAGE_PATH must be a normalized absolute path."
      [[ -d "${offline_package_path}" ]] || die "Offline Fail2ban package path does not exist."
      ;;
    *) die "Unsupported Fail2ban install mode: ${install_mode}" ;;
  esac
  [[ "${state_root}" == /* && "$(realpath -m -- "${state_root}")" == "${state_root}" ]] ||
    die "ONEINSTACK_COMPONENT_STATE must be a normalized absolute path."
  case "${state_root}" in /|/var|/var/lib|/var/lib/oneinstack) die "ONEINSTACK_COMPONENT_STATE is too broad: ${state_root}" ;; esac
  for value in "${FAIL2BAN_DEFAULT_MAXRETRY:-5}" "${FAIL2BAN_DEFAULT_FINDTIME:-600}" "${FAIL2BAN_DEFAULT_BANTIME:-3600}"; do
    [[ "${value}" =~ ^[1-9][0-9]*$ ]] || die "Fail2ban default timing parameters must be positive integers."
  done
  if [[ -n "${FAIL2BAN_IGNORE_IP:-}" ]] && [[ ! "${FAIL2BAN_IGNORE_IP}" =~ ^[0-9A-Fa-f:./[:space:]]+$ ]]; then
    die "FAIL2BAN_IGNORE_IP contains unsupported characters."
  fi
}
check_host() {
  [[ -r /etc/os-release ]] || die "/etc/os-release is unavailable."
  source /etc/os-release
  case "${ID:-}:${VERSION_ID%%.*}" in
    ubuntu:22|ubuntu:24|ubuntu:26|debian:11|debian:12|debian:13|\
    rhel:8|rhel:9|rhel:10|rocky:8|rocky:9|rocky:10|\
    almalinux:8|almalinux:9|almalinux:10|ol:8|ol:9|ol:10|\
    centos:7|centos:8|centos:9|centos:10|fedora:*|amzn:2023|\
    sles:*|opensuse-leap:*|opensuse-tumbleweed:*|opensuse:*) ;;
    *) die "Unsupported Linux release: ${ID:-unknown} ${VERSION_ID:-unknown}" ;;
  esac
  case "$(uname -m)" in x86_64|aarch64|arm64) ;; *) die "Fail2ban component supports amd64 and arm64 hosts only." ;; esac
}
source_runtime_installed() {
  local existing_version existing_prefix
  existing_version="$(installed_source_version || true)"
  [[ -n "${existing_version}" ]] || return 1
  existing_prefix="$(source_prefix_for_version "${existing_version}")"
  [[ -x "${existing_prefix}/bin/fail2ban-client" ]]
}
system_package_installed() {
  case "${ID:-}" in
    ubuntu|debian)
      dpkg-query -W -f='${Status}\n' fail2ban 2>/dev/null | grep -Fq 'install ok installed'
      ;;
    *)
      rpm -q fail2ban >/dev/null 2>&1 || rpm -q fail2ban-server >/dev/null 2>&1
      ;;
  esac
}
system_package_name() {
  case "${ID:-}" in
    ubuntu|debian) printf '%s\n' fail2ban ;;
    *)
      if rpm -q fail2ban >/dev/null 2>&1; then
        printf '%s\n' fail2ban
      else
        printf '%s\n' fail2ban-server
      fi
      ;;
  esac
}
package_installed() {
  system_package_installed || source_runtime_installed
}
install_package() {
  source /etc/os-release
  if has_local_packages; then
    remove_existing_source_runtime
    install_local_packages
    return
  fi
  [[ "${install_mode}" != "offline" ]] || die "Offline Fail2ban bundle does not contain packages for ${ID:-unknown}."
  if [[ "${software_version}" != "system" ]]; then
    install_source_package
    return
  fi
  remove_existing_source_runtime
  case "${ID:-}" in
    ubuntu|debian)
      export DEBIAN_FRONTEND=noninteractive
      apt-get update
      apt-get install -y --no-install-recommends fail2ban python3
      ;;
    rhel|rocky|almalinux|ol|fedora|amzn)
      if ! dnf -q list --installed fail2ban >/dev/null 2>&1; then
        dnf install -y epel-release
      fi
      dnf install -y fail2ban python3
      ;;
    centos)
      if [[ "${VERSION_ID%%.*}" == "7" ]]; then
        yum install -y epel-release
        yum install -y fail2ban python3
      else
        dnf install -y epel-release
        dnf install -y fail2ban python3
      fi
      ;;
    sles|opensuse-leap|opensuse-tumbleweed|opensuse)
      zypper --non-interactive --no-refresh install --no-recommends fail2ban python3
      ;;
    *) die "Unsupported package manager for ${ID:-unknown}" ;;
  esac
}
reinstall_package() {
  source /etc/os-release
  if has_local_packages; then
    remove_existing_source_runtime
    install_local_packages
    return
  fi
  [[ "${install_mode}" != "offline" ]] || die "Offline Fail2ban bundle does not contain packages for ${ID:-unknown}."
  if [[ "${software_version}" != "system" ]]; then
    install_source_package
    return
  fi
  remove_existing_source_runtime
  case "${ID:-}" in
    ubuntu|debian)
      export DEBIAN_FRONTEND=noninteractive
      apt-get update
      apt-get install --reinstall -y fail2ban
      ;;
    rhel|rocky|almalinux|ol|fedora|amzn) dnf reinstall -y "$(system_package_name)" ;;
    centos)
      if [[ "${VERSION_ID%%.*}" == "7" ]]; then yum reinstall -y "$(system_package_name)"; else dnf reinstall -y "$(system_package_name)"; fi
      ;;
    sles|opensuse-leap|opensuse-tumbleweed|opensuse) zypper --non-interactive --no-refresh install --force-resolution "$(system_package_name)" ;;
    *) die "Unsupported package manager for ${ID:-unknown}" ;;
  esac
}
remove_package() {
  source /etc/os-release
  if [[ "${software_version}" != "system" ]] &&
    { [[ "$(installed_source_version || true)" == "${software_version}" ]] ||
      { [[ -r /etc/systemd/system/fail2ban.service ]] && grep -Fq "${source_bin_dir}/fail2ban-server" /etc/systemd/system/fail2ban.service; }; }; then
    remove_source_package
    return
  fi
  case "${ID:-}" in
    ubuntu|debian) apt-get remove -y fail2ban ;;
    rhel|rocky|almalinux|ol|fedora|amzn) dnf remove -y "$(system_package_name)" ;;
    centos)
      if [[ "${VERSION_ID%%.*}" == "7" ]]; then yum remove -y "$(system_package_name)"; else dnf remove -y "$(system_package_name)"; fi
      ;;
    sles|opensuse-leap|opensuse-tumbleweed|opensuse) zypper --non-interactive remove "$(system_package_name)" ;;
    *) die "Unsupported package manager for ${ID:-unknown}" ;;
  esac
}

package_artifact_root() {
  local bundle_root target_root arch
  if [[ "${install_mode}" == "offline" ]]; then
    bundle_root="${offline_package_path}"
  else
    bundle_root="${component_root}"
  fi
  case "$(uname -m)" in
    x86_64) arch="amd64" ;;
    aarch64|arm64) arch="arm64" ;;
    *) arch="unknown" ;;
  esac
  target_root="${bundle_root}/packages/${ID:-unknown}/${VERSION_ID%%.*}/${arch}"
  if [[ -d "${target_root}" ]]; then
    printf '%s\n' "${target_root}"
  else
    # A single-target offline bundle may omit the OS-specific directory.
    printf '%s\n' "${bundle_root}/packages"
  fi
}

has_local_packages() {
  local root
  root="$(package_artifact_root)"
  [[ -d "${root}" ]] || return 1
  case "${ID:-}" in
    ubuntu|debian) find "${root}" -maxdepth 1 -type f -name '*.deb' -print -quit | grep -q . ;;
    *) find "${root}" -maxdepth 1 -type f -name '*.rpm' -print -quit | grep -q . ;;
  esac
}

install_local_packages() {
  local root
  root="$(package_artifact_root)"
  local -a packages=()
  case "${ID:-}" in
    ubuntu|debian)
      mapfile -t packages < <(find "${root}" -maxdepth 1 -type f -name '*.deb' -print | sort)
      ((${#packages[@]} > 0)) || die "No Debian Fail2ban packages are present in the bundle."
      if [[ "${install_mode}" == "offline" ]]; then
        set +e
        dpkg -i "${packages[@]}"
        local dpkg_status=$?
        set -e
        apt-get --no-download -f install -y
        dpkg --configure -a
        ((dpkg_status == 0)) || true
      else
        apt-get install -y --no-install-recommends "${packages[@]}"
      fi
      ;;
    centos)
      mapfile -t packages < <(find "${root}" -maxdepth 1 -type f -name '*.rpm' -print | sort)
      ((${#packages[@]} > 0)) || die "No RPM Fail2ban packages are present in the bundle."
      if [[ "${VERSION_ID%%.*}" == "7" ]]; then
        if [[ "${install_mode}" == "offline" ]]; then yum --disablerepo='*' localinstall -y "${packages[@]}"; else yum localinstall -y "${packages[@]}"; fi
      elif [[ "${install_mode}" == "offline" ]]; then
        dnf --disablerepo='*' install -y "${packages[@]}"
      else
        dnf install -y "${packages[@]}"
      fi
      ;;
    rhel|rocky|almalinux|ol|fedora|amzn)
      mapfile -t packages < <(find "${root}" -maxdepth 1 -type f -name '*.rpm' -print | sort)
      ((${#packages[@]} > 0)) || die "No RPM Fail2ban packages are present in the bundle."
      if [[ "${install_mode}" == "offline" ]]; then dnf --disablerepo='*' install -y "${packages[@]}"; else dnf install -y "${packages[@]}"; fi
      ;;
    sles|opensuse-leap|opensuse-tumbleweed|opensuse)
      mapfile -t packages < <(find "${root}" -maxdepth 1 -type f -name '*.rpm' -print | sort)
      ((${#packages[@]} > 0)) || die "No RPM Fail2ban packages are present in the bundle."
      zypper --non-interactive --no-refresh --no-gpg-checks install --no-recommends --allow-unsigned-rpm "${packages[@]}"
      ;;
    *) die "Unsupported package manager for ${ID:-unknown}" ;;
  esac
}

install_source_dependencies() {
  case "${ID:-}" in
    ubuntu|debian)
      export DEBIAN_FRONTEND=noninteractive
      apt-get update
      apt-get install -y --no-install-recommends ca-certificates curl gzip python3 python3-setuptools tar
      ;;
    rhel|rocky|almalinux|ol|fedora|amzn)
      dnf install -y ca-certificates curl gzip python3 python3-setuptools tar
      ;;
    centos)
      if [[ "${VERSION_ID%%.*}" == "7" ]]; then
        yum install -y ca-certificates curl gzip python3 python3-setuptools tar
      else
        dnf install -y ca-certificates curl gzip python3 python3-setuptools tar
      fi
      ;;
    sles|opensuse-leap|opensuse-tumbleweed|opensuse)
      zypper --non-interactive refresh
      zypper --non-interactive install --no-recommends ca-certificates curl gzip python3 python3-setuptools tar
      ;;
    *) die "Unsupported package manager for ${ID:-unknown}" ;;
  esac
}

download_source_verified() {
  local destination="$1" cache_dir="/var/cache/oneinstack/downloads"
  local cache_file="${cache_dir}/fail2ban-${software_version}.tar.gz" temporary_cache
  install -d -m 0750 -- "${cache_dir}"
  if [[ -f "${cache_file}" ]] &&
    printf '%s  %s\n' "${source_sha256}" "${cache_file}" | sha256sum --check --status; then
    cp -- "${cache_file}" "${destination}"
    return 0
  fi
  temporary_cache="$(mktemp "${cache_file}.tmp.XXXXXX")"
  trap 'rm -f -- "${temporary_cache:-}"' RETURN
  curl --proto '=https' --tlsv1.2 --fail --location --retry 5 --retry-delay 2 \
    --connect-timeout 20 --max-time 180 --output "${temporary_cache}" "${source_url}"
  printf '%s  %s\n' "${source_sha256}" "${temporary_cache}" | sha256sum --check --status ||
    die "Fail2ban ${software_version} source checksum verification failed."
  chmod 0640 "${temporary_cache}"
  mv -f -- "${temporary_cache}" "${cache_file}"
  trap - RETURN
  cp -- "${cache_file}" "${destination}"
}

write_source_service_unit() {
  local python_path
  python_path="$(source_python_path || true)"
  [[ -n "${python_path}" ]] || die "Fail2ban source runtime Python path is missing."
  install -d -m 0755 -- /etc/systemd/system
  {
    printf '[Unit]\n'
    printf 'Description=Fail2Ban Service\n'
    printf 'After=network.target iptables.service firewalld.service ip6tables.service ipset.service nftables.service\n'
    printf 'PartOf=iptables.service firewalld.service ip6tables.service ipset.service nftables.service\n\n'
    printf '[Service]\n'
    printf 'Type=simple\n'
    printf 'Environment=PYTHONNOUSERSITE=1\n'
    printf 'Environment=PYTHONPATH=%s\n' "${python_path}"
    printf 'ExecStart=%s/fail2ban-server -xf start\n' "${source_bin_dir}"
    printf 'ExecStop=%s/fail2ban-client stop\n' "${source_bin_dir}"
    printf 'ExecReload=%s/fail2ban-client reload\n' "${source_bin_dir}"
    printf 'RuntimeDirectory=fail2ban\n'
    printf 'RuntimeDirectoryMode=0755\n'
    printf 'StateDirectory=fail2ban\n'
    printf 'Restart=on-failure\n\n'
    printf '[Install]\n'
    printf 'WantedBy=multi-user.target\n'
  } >/etc/systemd/system/fail2ban.service
  chmod 0644 /etc/systemd/system/fail2ban.service
  systemctl daemon-reload
}

install_source_package() {
  local archive source_dir stage python_path relative target source_file
  select_source
  [[ ! -d "${migration_dir}" ]] || : >"${migration_dir}/source-install"
  install_source_dependencies
  install -d -m 0755 -- /usr/local/src
  source_work_dir="$(mktemp -d /usr/local/src/oneinstack-fail2ban.XXXXXX)"
  trap cleanup_source_work_dir EXIT
  archive="${source_work_dir}/fail2ban.tar.gz"
  download_source_verified "${archive}"
  tar -xzf "${archive}" -C "${source_work_dir}"
  source_dir="${source_work_dir}/fail2ban-${software_version}"
  [[ -d "${source_dir}" ]] || die "Unexpected Fail2ban ${software_version} source archive layout."
  if [[ "${software_version}" == "1.0.2" ]]; then
    emit_progress 42 source.compatibility.converting "正在转换 Fail2ban 1.0.2 Python 兼容代码"
    (
      cd -- "${source_dir}"
      if command -v 2to3 >/dev/null 2>&1; then
        2to3 -w --no-diffs bin/* fail2ban
      else
        python3 -m lib2to3 -w --no-diffs bin/* fail2ban
      fi
    ) || die "Fail2ban 1.0.2 requires the Python 2-to-3 conversion tool."
  fi
  stage="${source_work_dir}/stage"
  install -d -m 0755 -- "${stage}"
  emit_progress 45 source.building "正在构建 Fail2ban ${software_version} 运行时"
  (
    cd -- "${source_dir}"
    # Debian's patched setuptools may append /local to an explicitly supplied
    # prefix. Keep the private runtime layout stable with explicit destinations.
    python3 setup.py install \
      --root="${stage}" \
      --prefix=/usr/local \
      --install-scripts="${source_bin_dir}" \
      --install-lib="${source_python_lib_dir}" \
      --without-tests
  )
  [[ -x "${stage}${source_bin_dir}/fail2ban-client" ]] ||
    die "Fail2ban ${software_version} source installation did not produce ${source_bin_dir}/fail2ban-client."
  rm -rf -- "${source_prefix}"
  install -d -m 0755 -- "${source_root}"
  mv -- "${stage}${source_prefix}" "${source_prefix}"
  if [[ -d "${stage}/etc/fail2ban" ]]; then
    if [[ ! -d /etc/fail2ban ]]; then
      install -d -m 0755 -- /etc
      mv -- "${stage}/etc/fail2ban" /etc/fail2ban
    else
      while IFS= read -r -d '' source_file; do
        relative="${source_file#"${stage}/etc/fail2ban/"}"
        target="/etc/fail2ban/${relative}"
        [[ -e "${target}" ]] || install -D -m 0644 -- "${source_file}" "${target}"
      done < <(find "${stage}/etc/fail2ban" -type f -print0)
    fi
  fi
  install -d -m 0750 -- /var/lib/fail2ban
  install -d -m 0755 -- /run/fail2ban
  activate_source_runtime
  python_path="$(source_python_path || true)"
  [[ -n "${python_path}" ]] || die "Fail2ban ${software_version} source installation did not produce Python modules."
  write_source_service_unit
  printf '%s\n' "${software_version}" >"${source_version_marker}"
  chmod 0640 "${source_version_marker}"
  emit_progress 55 source.installed "Fail2ban ${software_version} 运行时已安装"
  cleanup_source_work_dir
  trap - EXIT
}

remove_source_package() {
  local prefix="${source_prefix}"
  if [[ -r /etc/systemd/system/fail2ban.service ]] && grep -Fq "${prefix}/bin/fail2ban-server" /etc/systemd/system/fail2ban.service; then
    rm -f -- /etc/systemd/system/fail2ban.service
    systemctl daemon-reload
  fi
  rm -rf -- "${prefix}"
  if [[ "$(installed_source_version || true)" == "${software_version}" ]]; then
    rm -f -- "${source_version_marker}"
  fi
}

remove_existing_source_runtime() {
  local existing_version existing_prefix
  existing_version="$(installed_source_version || true)"
  [[ -n "${existing_version}" ]] || return 0
  existing_prefix="$(source_prefix_for_version "${existing_version}")"
  if [[ -r /etc/systemd/system/fail2ban.service ]] && grep -Fq "${existing_prefix}/bin/fail2ban-server" /etc/systemd/system/fail2ban.service; then
    rm -f -- /etc/systemd/system/fail2ban.service
  fi
  rm -rf -- "${existing_prefix}"
  rm -f -- "${source_version_marker}"
  systemctl daemon-reload
}

runtime_version() {
  fail2ban-client --version 2>&1 | sed -n 's/.*Fail2Ban v//p' | head -n1 | tr -d '[:space:]'
}

validate_runtime_version() {
  [[ "${software_version}" == "system" ]] && return 0
  local actual
  actual="$(runtime_version)"
  [[ "${actual}" == "${software_version}" ]] || die "Fail2ban runtime version ${actual:-unknown} does not match requested ${software_version}."
}

write_default_configuration() {
  install -d -m 0750 -- "$(dirname -- "${defaults_path}")"
  local banaction
  banaction="$(select_banaction)"
  {
    printf '[DEFAULT]\n'
    printf 'banaction = %s\n' "${banaction}"
    printf 'maxretry = %s\n' "${FAIL2BAN_DEFAULT_MAXRETRY:-5}"
    printf 'findtime = %s\n' "${FAIL2BAN_DEFAULT_FINDTIME:-600}"
    printf 'bantime = %s\n' "${FAIL2BAN_DEFAULT_BANTIME:-3600}"
    if [[ -n "${FAIL2BAN_IGNORE_IP:-}" ]]; then
      printf 'ignoreip = %s\n' "${FAIL2BAN_IGNORE_IP}"
    fi
  } >"${defaults_path}"
  chmod 0640 "${defaults_path}"
}
action_config_exists() {
  local action="$1"
  for root in /etc/fail2ban/action.d /usr/share/fail2ban/action.d; do
    [[ -r "${root}/${action}.conf" ]] && return 0
  done
  return 1
}
select_banaction() {
  if command -v firewall-cmd >/dev/null 2>&1 &&
    systemctl is-active --quiet firewalld 2>/dev/null &&
    action_config_exists firewallcmd-ipset; then
    printf '%s\n' firewallcmd-ipset
    return 0
  fi
  if command -v nft >/dev/null 2>&1 && action_config_exists nftables-multiport; then
    printf '%s\n' nftables-multiport
    return 0
  fi
  if command -v iptables >/dev/null 2>&1 && action_config_exists iptables-multiport; then
    printf '%s\n' iptables-multiport
    return 0
  fi
  die "No supported Fail2ban ban action is available (firewallcmd-ipset, nftables-multiport, or iptables-multiport)."
}
write_redis_acl_service() {
  local python_bin
  python_bin="$(command -v python3)"
  install -d -m 0755 -- "$(dirname -- "${redis_acl_unit}")"
  cat >"${redis_acl_unit}" <<EOF
[Unit]
Description=OneinStack Redis ACL event collector for Fail2ban
After=network-online.target redis.service redis-server.service
Wants=network-online.target

[Service]
Type=simple
User=root
ExecStart=${python_bin} ${redis_acl_helper}
Restart=always
RestartSec=15
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=full
ReadWritePaths=/var/lib/oneinstack/fail2ban

[Install]
WantedBy=multi-user.target
EOF
  systemctl daemon-reload
}
start_redis_acl_collector() {
  write_redis_acl_service
  systemctl enable --now "${redis_acl_service}"
}
stop_redis_acl_collector() {
  systemctl disable --now "${redis_acl_service}" 2>/dev/null || true
}
service_enable_start() { systemctl enable --now "${service_name}"; }
service_stop_disable() { systemctl disable --now "${service_name}" 2>/dev/null || true; }
snapshot_existing() {
  local previous_source_version previous_source_prefix
  rm -rf -- "${migration_dir}"
  install -d -m 0700 -- "${migration_dir}"
  stop_redis_acl_collector
  system_package_installed && : >"${migration_dir}/package-existed" || true
  previous_source_version="$(installed_source_version || true)"
  if [[ -n "${previous_source_version}" ]]; then
    printf '%s\n' "${previous_source_version}" >"${migration_dir}/source-version"
    previous_source_prefix="$(source_prefix_for_version "${previous_source_version}")"
    if [[ -d "${previous_source_prefix}" ]]; then
      mv -- "${previous_source_prefix}" "${migration_dir}/source-prefix"
    fi
  fi
  [[ -d /etc/fail2ban ]] && cp -a -- /etc/fail2ban "${migration_dir}/config"
  [[ -f /etc/systemd/system/fail2ban.service ]] && cp -a -- /etc/systemd/system/fail2ban.service "${migration_dir}/service"
  [[ -f "${state_dir}/installed.json" ]] && cp -a -- "${state_dir}/installed.json" "${migration_dir}/installed.json"
  [[ -f "${installed_marker}" ]] && : >"${migration_dir}/installed-marker"
  systemctl is-active --quiet "${service_name}" 2>/dev/null && : >"${migration_dir}/was-active" || true
  systemctl is-enabled --quiet "${service_name}" 2>/dev/null && : >"${migration_dir}/was-enabled" || true
  service_stop_disable
  emit_progress 20 migration.snapshot.created "Fail2ban package, configuration, and service snapshot created"
}
restore_existing() {
  local previous_source_version previous_source_prefix
  stop_redis_acl_collector
  service_stop_disable
  remove_managed_integration
  if [[ "${software_version}" != "system" ]]; then
    remove_source_package
  fi
  if [[ ! -f "${migration_dir}/package-existed" ]]; then
    if [[ "${software_version}" == "system" ]]; then
      system_package_installed && remove_package || true
    elif ! [[ -f "${migration_dir}/source-install" ]]; then
      remove_package
    fi
  fi
  previous_source_version="$(cat "${migration_dir}/source-version" 2>/dev/null || true)"
  if [[ -n "${previous_source_version}" && -d "${migration_dir}/source-prefix" ]]; then
    previous_source_prefix="$(source_prefix_for_version "${previous_source_version}")"
    install -d -m 0755 -- "${source_root}"
    mv -- "${migration_dir}/source-prefix" "${previous_source_prefix}"
  fi
  if [[ -d "${migration_dir}/config" ]]; then
    rm -rf -- /etc/fail2ban
    mv -- "${migration_dir}/config" /etc/fail2ban
  fi
  if [[ -f "${migration_dir}/service" ]]; then
    cp -a -- "${migration_dir}/service" /etc/systemd/system/fail2ban.service
  else
    rm -f -- /etc/systemd/system/fail2ban.service
  fi
  systemctl daemon-reload
  [[ -f "${migration_dir}/was-enabled" ]] && systemctl enable "${service_name}" 2>/dev/null || true
  [[ -f "${migration_dir}/was-active" ]] && systemctl start "${service_name}" 2>/dev/null || true
  rm -f -- "${installed_marker}" "${state_dir}/installed.json" "${source_version_marker}"
  [[ ! -f "${migration_dir}/installed-marker" ]] || : >"${installed_marker}"
  [[ ! -f "${migration_dir}/installed.json" ]] || cp -a -- "${migration_dir}/installed.json" "${state_dir}/installed.json"
  [[ ! -f "${migration_dir}/source-version" ]] || cp -a -- "${migration_dir}/source-version" "${source_version_marker}"
  emit_progress 100 rollback.service.restored "Previous Fail2ban package, configuration, and service state restored"
}
commit_migration() { rm -rf -- "${migration_dir}"; }
wait_for_service_ready() {
  local timeout_seconds="${1:-15}"
  local attempt
  for ((attempt = 1; attempt <= timeout_seconds; attempt++)); do
    if fail2ban-client ping 2>/dev/null | grep -q 'pong'; then
      return 0
    fi
    sleep 1
  done
  systemctl status --no-pager --full "${service_name}" >&2 || true
  if command -v journalctl >/dev/null 2>&1; then
    journalctl -u "${service_name}.service" -n 30 --no-pager >&2 || true
  fi
  die "Fail2ban service did not become ready within ${timeout_seconds} seconds."
}
write_state() {
  install -d -m 0750 -- "${state_dir}"
  printf '{"component":"%s","softwareVersion":"%s","service":"%s"}\n' \
    "${component_id}" "${software_version}" "${service_name}" >"${state_dir}/installed.json"
  chmod 0640 "${state_dir}/installed.json"
}
remove_managed_integration() {
  stop_redis_acl_collector
  rm -f -- "${helper_path}" "${action_path}" "${filter_path}" "${redis_auth_filter_path}" "${defaults_path}" "${redis_acl_helper}" "${redis_acl_unit}" "${redis_acl_lock}"
  systemctl daemon-reload
  rm -f -- "${integration_marker}"
}
