#!/usr/bin/env bash
set -Eeuo pipefail
umask 027

component_id="firewalld"
requested_version="${SOFTWARE_VERSION:-2.1.1}"
state_root="${ONEINSTACK_COMPONENT_STATE:-/var/lib/oneinstack/components}"
state_dir="${state_root}/${component_id}"
installed_marker="${state_dir}/installed-by-oneinstack"
migration_dir="${state_dir}/migration"
managed_rules_file="${state_dir}/managed-rules.json"
firewalld_pid_file="${state_dir}/firewalld.pid"
os_id=""
os_version=""
package_manager=""
host_arch=""
install_mode="${ONEINSTACK_INSTALL_MODE:-center}"
offline_package_path="${ONEINSTACK_OFFLINE_PACKAGE_PATH:-}"

die() {
  local code="${1:-SCRIPT_EXECUTION_FAILED}" message="${2:-operation failed}"
  printf 'ERROR_CODE=%s\n' "${code}" >&2
  printf 'ERROR: %s\n' "${message}" >&2
  exit 1
}

emit_progress() {
  local percent="$1" code="$2" message="$3" fd="${ONEINSTACK_PROGRESS_FD:-}"
  [[ "${fd}" =~ ^[0-9]+$ ]] || return 0
  message="${message//\\/\\\\}"
  message="${message//\"/\\\"}"
  message="${message//$'\n'/ }"
  printf '{"type":"progress","percent":%s,"code":"%s","message":"%s"}\n' \
    "${percent}" "${code}" "${message}" 2>/dev/null 1>&"${fd}" || true
}

require_root() { [[ "$(id -u)" -eq 0 ]] || die "PERMISSION_DENIED" "This action must run as root."; }
require_command() { command -v "$1" >/dev/null 2>&1 || die "HOST_DEPENDENCY_UNAVAILABLE" "Required command is missing: $1"; }

validate_state_root() {
  local normalized
  normalized="$(realpath -m -- "${state_root}" 2>/dev/null || true)"
  [[ "${state_root}" == /* && "${normalized}" == "${state_root}" ]] ||
    die "CONFIG_INVALID" "ONEINSTACK_COMPONENT_STATE must be a normalized absolute path."
  case "${state_root}" in
    /|/usr|/usr/local|/etc|/var|/home|/root|/data)
      die "CONFIG_INVALID" "ONEINSTACK_COMPONENT_STATE is too broad."
      ;;
  esac
}

version_line_matches() {
  local version="$1" line="$2"
  if [[ "${line}" != *.x ]]; then
    [[ "${version}" == "${line}" ]]
    return
  fi
  local prefix="${line%.x}"
  [[ "${version}" == "${prefix}" || "${version}" == "${prefix}".* ]]
}

version_major_line() {
  local version="$1"
  printf '%s.x' "${version%%.*}"
}

runtime_version() {
  local version=""
  if command -v rpm >/dev/null 2>&1; then
    if version="$(rpm -q --qf '%{VERSION}' firewalld 2>/dev/null)" && [[ -n "${version}" && "${version}" != "(none)" ]]; then
      printf '%s' "${version}"
      return 0
    fi
  fi
  if command -v dpkg-query >/dev/null 2>&1; then
    version="$(dpkg-query -W -f='${Version}' firewalld 2>/dev/null || true)"
    version="${version#*:}"
    [[ -n "${version}" && "${version}" != "(none)" ]] && { printf '%s' "${version%%-*}"; return 0; }
  fi
  if command -v python3 >/dev/null 2>&1; then
    version="$(python3 -c 'from firewall.config import VERSION; print(VERSION)' 2>/dev/null || true)"
    [[ -n "${version}" ]] && { printf '%s' "${version}"; return 0; }
  fi
  if command -v python >/dev/null 2>&1; then
    version="$(python -c 'from firewall.config import VERSION; print(VERSION)' 2>/dev/null || true)"
    [[ -n "${version}" ]] && { printf '%s' "${version}"; return 0; }
  fi
  if command -v firewall-cmd >/dev/null 2>&1; then
    # CentOS 7's firewalld 0.6.x connects to the daemon even for
    # `firewall-cmd --version`; installation verification intentionally runs
    # while the service is stopped, so do not make the version check depend on
    # a live D-Bus service.
    version="$(firewall-cmd --version 2>/dev/null | head -n1 | tr -d '[:space:]' || true)"
    [[ -n "${version}" ]] && { printf '%s' "${version}"; return 0; }
  fi
  return 1
}

runtime_version_matches_request() {
  local actual="$1"
  if [[ "${requested_version}" == *.x ]]; then
    version_line_matches "${actual}" "${requested_version}"
  else
    [[ "${actual}" == "${requested_version}" ]]
  fi
}

normalize_host_id() {
  case "${ID:-}" in
    ubuntu|debian|rhel|rocky|almalinux|centos|ol|fedora|amzn|sles|opensuse-leap|opensuse-tumbleweed|opensuse)
      printf '%s' "${ID}"
      ;;
    opensuse*) printf 'opensuse' ;;
    *)
      case " ${ID_LIKE:-} " in
        *' debian '*) printf 'debian' ;;
        *' rhel '*|*' fedora '*) printf 'rhel' ;;
        *' suse '*) printf 'opensuse' ;;
        *) return 1 ;;
      esac
      ;;
  esac
}

host_version_supported() {
  case "${os_id}" in
    ubuntu) case "${os_version}" in 22.04|24.04|26.04) return 0 ;; esac ;;
    debian) case "${os_version%%.*}" in 11|12|13) return 0 ;; esac ;;
    rhel|rocky|almalinux|ol) case "${os_version%%.*}" in 8|9|10) return 0 ;; esac ;;
    centos) case "${os_version%%.*}" in 7|8|9|10) return 0 ;; esac ;;
    amzn) [[ "${os_version%%.*}" == "2023" ]] && return 0 ;;
    fedora|sles|opensuse-leap|opensuse-tumbleweed|opensuse) return 0 ;;
  esac
  return 1
}

detect_package_manager() {
  case "${os_id}" in
    ubuntu|debian)
      command -v apt-get >/dev/null 2>&1 && printf 'apt' || return 1
      ;;
    centos)
      if [[ "${os_version}" == "7" ]] && command -v yum >/dev/null 2>&1; then
        printf 'yum'
      elif command -v dnf >/dev/null 2>&1; then
        printf 'dnf'
      elif command -v yum >/dev/null 2>&1; then
        printf 'yum'
      else
        return 1
      fi
      ;;
    rhel|rocky|almalinux|ol|fedora|amzn)
      if command -v dnf >/dev/null 2>&1; then printf 'dnf'; elif command -v yum >/dev/null 2>&1; then printf 'yum'; else return 1; fi
      ;;
    sles|opensuse-leap|opensuse-tumbleweed|opensuse)
      command -v zypper >/dev/null 2>&1 && printf 'zypper' || return 1
      ;;
    *) return 1 ;;
  esac
}

check_host() {
  [[ -r /etc/os-release ]] || die "HOST_PLATFORM_UNSUPPORTED" "/etc/os-release is unavailable."
  # shellcheck disable=SC1091
  source /etc/os-release
  os_id="$(normalize_host_id || true)"
  os_version="${VERSION_ID:-}"
  [[ -n "${os_id}" && -n "${os_version}" ]] || die "HOST_PLATFORM_UNSUPPORTED" "The host distribution could not be identified."
  host_version_supported || die "HOST_PLATFORM_UNSUPPORTED" "Unsupported Linux distribution or version: ${os_id} ${os_version}."
  package_manager="$(detect_package_manager || true)"
  [[ -n "${package_manager}" ]] || die "HOST_DEPENDENCY_UNAVAILABLE" "No supported package manager was found for ${os_id}."
  case "$(uname -m)" in
    x86_64) host_arch="amd64" ;;
    aarch64|arm64) host_arch="arm64" ;;
    *) die "HOST_PLATFORM_UNSUPPORTED" "Only amd64 and arm64 hosts are supported." ;;
  esac
}

validate_inputs() {
  [[ "${requested_version}" =~ ^[0-9]+(\.[0-9]+){1,3}([+-][0-9A-Za-z.-]+)?$ || "${requested_version}" =~ ^[0-9]+(\.[0-9]+)*\.x$ ]] ||
    die "VERSION_UNSUPPORTED" "Unsupported firewalld version or version line: ${requested_version}."
  validate_state_root
  [[ "${install_mode}" == "center" || "${install_mode}" == "offline" ]] ||
    die "CONFIG_INVALID" "ONEINSTACK_INSTALL_MODE must be center or offline."
  if [[ "${install_mode}" == "offline" ]]; then
    [[ -n "${offline_package_path}" && "${offline_package_path}" == /* &&
      "$(realpath -m -- "${offline_package_path}")" == "${offline_package_path}" ]] ||
      die "CONFIG_INVALID" "Offline installation requires a normalized absolute Bundle path."
  elif [[ -n "${offline_package_path}" ]]; then
    die "CONFIG_INVALID" "Offline Bundle path cannot be used in Center mode."
  fi
}

docker_apt_sources() {
  local source_file
  for source_file in /etc/apt/sources.list /etc/apt/sources.list.d/*; do
    [[ -f "${source_file}" ]] || continue
    if grep -Eiq '^[[:space:]]*(deb|URIs:).*download\.docker\.com/linux/' "${source_file}" 2>/dev/null; then
      printf '%s\n' "${source_file}"
    fi
  done
}

copy_apt_source_without_docker() {
  local source_file="$1" target_file="$2"
  case "${source_file}" in
    *.list) sed -E '/^[[:space:]]*deb(-src)?([[:space:]]|\[).*download\.docker\.com\/linux\//d' "${source_file}" >"${target_file}" ;;
    *.sources) awk 'BEGIN { RS=""; ORS="\n\n" } $0 !~ /download\.docker\.com\/linux\// { print }' "${source_file}" >"${target_file}" ;;
    *) cp -a -- "${source_file}" "${target_file}" ;;
  esac
}

apt_update_without_docker_source() (
  set -Eeuo pipefail
  local source_dir source_list source_file target_file
  source_dir="$(mktemp -d)"
  trap 'rm -rf -- "${source_dir}"' EXIT
  source_list="${source_dir}/sources.list"
  if [[ -f /etc/apt/sources.list ]]; then copy_apt_source_without_docker /etc/apt/sources.list "${source_list}"; else : >"${source_list}"; fi
  for source_file in /etc/apt/sources.list.d/*.list /etc/apt/sources.list.d/*.sources; do
    [[ -f "${source_file}" ]] || continue
    target_file="${source_dir}/$(basename -- "${source_file}")"
    copy_apt_source_without_docker "${source_file}" "${target_file}"
  done
  apt-get update -o Dir::Etc::sourcelist="${source_list}" -o Dir::Etc::sourceparts="${source_dir}/"
)

refresh_package_index() {
  [[ "${install_mode}" == "offline" ]] && return 0
  case "${package_manager}" in
    apt)
      mapfile -t docker_sources < <(docker_apt_sources)
      if ((${#docker_sources[@]} > 0)); then apt_update_without_docker_source; else apt-get update; fi
      ;;
    dnf) dnf makecache --refresh -y ;;
    yum) yum makecache -y ;;
    zypper) zypper --non-interactive refresh ;;
  esac
}

offline_package_dir() {
  [[ -n "${offline_package_path}" ]] || die "PACKAGE_UNAVAILABLE" "Offline Bundle path is missing."
  printf '%s/packages/%s/%s/%s\n' "${offline_package_path}" "${os_id}" "${os_version}" "${host_arch}"
}

validate_offline_bundle() {
  local package_dir
  [[ -d "${offline_package_path}" ]] || die "PACKAGE_UNAVAILABLE" "Offline firewalld Bundle is unavailable."
  [[ -f "${offline_package_path}/manifest.yaml" ]] || die "PACKAGE_INVALID" "Offline Bundle manifest is missing."
  [[ -f "${offline_package_path}/files.sha256" ]] || die "PACKAGE_INVALID" "Offline Bundle checksum file is missing."
  package_dir="$(offline_package_dir)"
  [[ -d "${package_dir}" ]] || die "PACKAGE_UNAVAILABLE" "Offline firewalld packages are missing for this host."
  (cd "${offline_package_path}" && sha256sum -c files.sha256 --status) ||
    die "PACKAGE_VERIFY_FAILED" "Offline Bundle checksum verification failed."
  grep -Eq '^[[:space:]]+id:[[:space:]]+firewalld[[:space:]]*$' "${offline_package_path}/manifest.yaml" ||
    die "PACKAGE_INVALID" "Offline Bundle component is not firewalld."
  grep -Eq "^[[:space:]]+version:[[:space:]]+1\\.0\\.12[[:space:]]*$" "${offline_package_path}/manifest.yaml" ||
    die "PACKAGE_INVALID" "Offline Bundle package version does not match firewalld 1.0.12."
}

# Both the read-only installation probe and installation use these candidates.
# Cache-only queries never refresh repositories from a status/list request.
# Output: upstream-version|exact-package-selector, oldest first.
available_package_candidates() {
  local cache_only="${1:-false}" rows="" version="" selector=""
  if [[ "${install_mode}" == "offline" ]]; then
    local package_dir package
    package_dir="$(offline_package_dir)"
    while IFS= read -r package; do
      case "${package_manager}" in
        apt)
          version="$(dpkg-deb -f "${package}" Version 2>/dev/null || true)"
          ;;
        dnf|yum|zypper)
          version="$(rpm -qp --qf '%{VERSION}-%{RELEASE}' "${package}" 2>/dev/null || true)"
          ;;
      esac
      version="${version#*:}"
      version="${version%%-*}"
      [[ "${version}" =~ ^[0-9]+(\.[0-9]+){1,3}$ ]] || continue
      printf '%s|%s\n' "${version}" "${package}"
    done < <(find "${package_dir}" -maxdepth 1 -type f \( -name 'firewalld*.deb' -o -name 'firewalld*.rpm' \) -print | sort)
    return 0
  fi
  local query_args=(--showduplicates list available firewalld)
  [[ "${cache_only}" != "true" ]] || query_args=(-C "${query_args[@]}")
  case "${package_manager}" in
    apt)
      rows="$(LC_ALL=C apt-cache madison firewalld 2>/dev/null)" || return 1
      rows="$(printf '%s\n' "${rows}" | awk -F '|' '$1 ~ /^[[:space:]]*firewalld[[:space:]]*$/ {gsub(/^[[:space:]]+|[[:space:]]+$/, "", $2); print $2 "|" $2}')"
      ;;
    dnf|yum)
      # list works without the optional repoquery plugin. Keep the name and
      # architecture: passing a bare version to yum is not a package spec.
      rows="$(LC_ALL=C "${package_manager}" "${query_args[@]}" 2>/dev/null)" || return 1
      rows="$(printf '%s\n' "${rows}" | awk '$1 ~ /^firewalld\./ {arch=$1; sub(/^firewalld\./, "", arch); print $2 "|firewalld-" $2 "." arch}')"
      ;;
    zypper)
      rows="$(LC_ALL=C zypper --non-interactive --no-refresh search -s --match-exact firewalld 2>/dev/null)" || return 1
      rows="$(printf '%s\n' "${rows}" | awk -F '|' '{for(i=1;i<=NF;i++) gsub(/^[[:space:]]+|[[:space:]]+$/, "", $i)} $2 == "firewalld" && $3 == "package" {print $4 "|" $4}')"
      ;;
    *) return 1 ;;
  esac
  while IFS='|' read -r version selector; do
    version="${version#*:}"
    version="${version%%-*}"
    [[ "${version}" =~ ^[0-9]+(\.[0-9]+){1,3}$ && -n "${selector}" ]] || continue
    printf '%s|%s\n' "${version}" "${selector}"
  done <<<"${rows}" | sort -t'|' -k1,1V -k2,2V
}

select_package_candidate() {
  local candidate="" version="" selector="" candidates=""
  candidates="$(available_package_candidates)" ||
    die "HOST_REPOSITORY_UNAVAILABLE" "Could not query firewalld candidates; check the host package repository and metadata."
  while IFS='|' read -r version selector; do
    [[ -n "${version}" && -n "${selector}" ]] || continue
    runtime_version_matches_request "${version}" && candidate="${selector}"
  done <<<"${candidates}"
  if [[ -z "${candidate}" ]]; then
    printf 'Available firewalld runtime versions: %s\n' "$(printf '%s\n' "${candidates}" | cut -d'|' -f1 | sort -Vu | tr '\n' ' ')" >&2
    die "HOST_PACKAGE_VERSION_UNAVAILABLE" "The requested firewalld version is not available from the host repository; select a host-recommended exact version."
  fi
  printf '%s' "${candidate}"
}

probe_installation() {
  local candidates="" version="" selector="" backend="" installed=""
  printf 'component=firewalld\nprobe=installation\nsystem_id=%s\nsystem_version=%s\npackage_manager=%s\n' "${os_id}" "${os_version}" "${package_manager}"
  installed="$(runtime_version || true)"
  printf 'installed_version=%s\n' "${installed}"
  backend="$(external_firewall_backend || true)"
  printf 'conflicting_backend=%s\n' "${backend}"
  if [[ -n "${backend}" ]]; then
    printf 'blocked_code=FIREWALL_BACKEND_CONFLICT\n'
  elif service_active && [[ ! -f "${installed_marker}" ]]; then
    printf 'blocked_code=EXTERNAL_SERVICE_CONFLICT\n'
  fi
  if ! candidates="$(available_package_candidates true)"; then
    if [[ "${installed}" =~ ^[0-9]+(\.[0-9]+){1,3}$ ]]; then
      printf 'available_version=%s\n' "${installed}"
    else
      printf 'repository_error=HOST_REPOSITORY_UNAVAILABLE\n'
    fi
    return 0
  fi
  if [[ -z "${candidates}" && "${installed}" =~ ^[0-9]+(\.[0-9]+){1,3}$ ]]; then
    printf 'available_version=%s\n' "${installed}"
    return 0
  fi
  while IFS='|' read -r version selector; do
    [[ -n "${version}" ]] && printf 'available_version=%s\n' "${version}"
  done <<<"${candidates}"
  return 0
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

installed_package_version() {
  if command -v dpkg-query >/dev/null 2>&1; then
    dpkg-query -W -f='${Version}' firewalld 2>/dev/null || true
  elif command -v rpm >/dev/null 2>&1; then
    rpm -q --qf '%{EPOCHNUM}:%{VERSION}-%{RELEASE}.%{ARCH}' firewalld 2>/dev/null || true
  fi
}

install_firewalld_package() {
  local candidate="$1"
  if [[ "${install_mode}" == "offline" ]]; then
    case "${package_manager}" in
      apt) DEBIAN_FRONTEND=noninteractive dpkg -i "${candidate}" || apt-get -y --no-download -f install ;;
      dnf|yum) "${package_manager}" --disablerepo='*' --cacheonly install -y "${candidate}" ;;
      zypper) zypper --non-interactive --no-refresh --no-gpg-checks install --allow-unsigned-rpm "${candidate}" ;;
    esac
    return 0
  fi
  case "${package_manager}" in
    apt)
      DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends "firewalld=${candidate}" netbase ca-certificates
      ;;
    dnf) dnf install -y --setopt=install_weak_deps=False "${candidate}" setup ca-certificates ;;
    yum) yum install -y "${candidate}" setup ca-certificates ;;
    zypper) zypper --non-interactive install --no-recommends "firewalld=${candidate}" netcfg ca-certificates ;;
  esac
}

install_dependencies_offline() {
  local package_dir package
  local -a packages=()
  package_dir="$(offline_package_dir)"
  mapfile -t packages < <(find "${package_dir}" -maxdepth 1 -type f \( -name '*.deb' -o -name '*.rpm' \) -print | sort)
  ((${#packages[@]} > 0)) || die "PACKAGE_UNAVAILABLE" "Offline firewalld dependency packages are missing."
  emit_progress 20 dependency.offline.installing "Installing firewalld dependencies from the offline Bundle"
  case "${package_manager}" in
    apt) DEBIAN_FRONTEND=noninteractive dpkg -i "${packages[@]}" || apt-get -y --no-download -f install ;;
    dnf|yum) "${package_manager}" --disablerepo='*' --cacheonly install -y "${packages[@]}" ;;
    zypper) zypper --non-interactive --no-refresh --no-gpg-checks install --allow-unsigned-rpm "${packages[@]}" ;;
  esac
}

remove_firewalld_package() {
  case "${package_manager}" in
    apt) DEBIAN_FRONTEND=noninteractive apt-get remove -y firewalld ;;
    dnf) dnf remove -y firewalld ;;
    yum) yum remove -y firewalld ;;
    zypper) zypper --non-interactive remove firewalld ;;
  esac
}

systemd_available() {
  command -v systemctl >/dev/null 2>&1 && systemctl show-environment >/dev/null 2>&1
}

service_active() {
  if systemd_available; then
    systemctl is-active --quiet firewalld.service
  else
    firewall-cmd --state >/dev/null 2>&1
  fi
}

service_enabled() {
  systemd_available && systemctl is-enabled --quiet firewalld.service
}

external_firewall_backend() {
  if command -v ufw >/dev/null 2>&1 && ufw status 2>/dev/null | grep -Eiq '^Status:[[:space:]]+active'; then
    printf 'ufw'
    return 0
  fi
  if systemd_available; then
    local unit
    for unit in nftables iptables; do
      if systemctl is-active --quiet "${unit}.service"; then
        printf '%s' "${unit}"
        return 0
      fi
    done
  fi
  if ! service_active && command -v nft >/dev/null 2>&1 && nft list ruleset 2>/dev/null | grep -Eq '^[[:space:]]*table[[:space:]]+(ip|ip6|inet)[[:space:]]'; then
    printf 'nftables'
    return 0
  fi
  # `iptables -S` always includes the default `-P INPUT/OUTPUT/FORWARD`
  # policy lines, even when no standalone iptables rules are active. Treat
  # only actual appended/inserted rules as an external backend; otherwise a
  # clean host is incorrectly blocked during firewalld takeover.
  if ! service_active && command -v iptables >/dev/null 2>&1 && iptables -S 2>/dev/null | grep -Eq '^[[:space:]]*-[AI][[:space:]]'; then
    printf 'iptables'
    return 0
  fi
  return 1
}

check_external_firewall_conflict() {
  local backend
  backend="$(external_firewall_backend || true)"
  # Installing or upgrading the package while firewalld is stopped leaves
  # external iptables rules intact. Starting firewalld still requires a
  # separately guarded migration path.
  if [[ "${backend}" == "iptables" && ( "${ONEINSTACK_ACTION:-install}" == "install" || "${ONEINSTACK_ACTION:-install}" == "upgrade" ) ]] && ! service_active; then
    return 0
  fi
  if [[ ("${backend}" == "ufw" || "${backend}" == "nftables") && "${ONEINSTACK_ALLOW_EXTERNAL_MIGRATION:-false}" == "true" ]]; then
    return 0
  fi
  [[ -z "${backend}" ]] || die "FIREWALL_BACKEND_CONFLICT" "External firewall backend ${backend} is active; firewalld takeover is blocked."
  if service_active && [[ ! -f "${installed_marker}" ]]; then
    die "EXTERNAL_SERVICE_CONFLICT" "An external firewalld service is already active; explicit migration is required."
  fi
}

firewalld_process_pid() {
  local pid=""
  if [[ -r "${firewalld_pid_file}" ]]; then
    pid="$(tr -d '[:space:]' <"${firewalld_pid_file}")"
  fi
  [[ "${pid}" =~ ^[0-9]+$ ]] || return 1
  kill -0 "${pid}" >/dev/null 2>&1 || return 1
  [[ "$(readlink -f "/proc/${pid}/exe" 2>/dev/null || true)" == */firewalld ]] || return 1
  printf '%s' "${pid}"
}

start_firewalld_without_systemd() {
  if service_active; then return 0; fi
  require_command firewalld
  install -d -m 0750 -- "${state_dir}"
  nohup firewalld --nofork >"${state_dir}/firewalld.log" 2>&1 </dev/null &
  local pid="$!"
  printf '%s\n' "${pid}" >"${firewalld_pid_file}"
  chmod 0600 "${firewalld_pid_file}"
  for _ in {1..40}; do
    if service_active; then return 0; fi
    kill -0 "${pid}" >/dev/null 2>&1 || break
    sleep 0.25
  done
  die "SERVICE_START_FAILED" "firewalld did not become active without systemd."
}

stop_firewalld_without_systemd() {
  if ! service_active; then
    rm -f -- "${firewalld_pid_file}"
    return 0
  fi
  local pid
  pid="$(firewalld_process_pid || true)"
  [[ -n "${pid}" ]] || die "SERVICE_STOP_FAILED" "The active firewalld process is not owned by OneinStack."
  kill -TERM "${pid}" >/dev/null 2>&1 || true
  for _ in {1..40}; do
    if ! kill -0 "${pid}" >/dev/null 2>&1; then
      rm -f -- "${firewalld_pid_file}"
      return 0
    fi
    sleep 0.25
  done
  kill -KILL "${pid}" >/dev/null 2>&1 || true
  rm -f -- "${firewalld_pid_file}"
  service_active && die "SERVICE_STOP_FAILED" "firewalld remains active after stop."
  return 0
}

ensure_service_stopped() {
  if systemd_available; then
    systemctl stop firewalld.service >/dev/null 2>&1 || true
  elif service_active; then
    stop_firewalld_without_systemd
  fi
}

ensure_service_disabled() {
  ensure_service_stopped
  if systemd_available; then
    systemctl disable firewalld.service >/dev/null 2>&1 || true
  fi
}

ensure_service_started() {
  if systemd_available; then
    systemctl enable firewalld.service >/dev/null 2>&1 || die "SERVICE_START_FAILED" "firewalld could not be enabled."
    systemctl start firewalld.service >/dev/null 2>&1 || die "SERVICE_START_FAILED" "firewalld could not be started."
  else
    start_firewalld_without_systemd
  fi
  service_active || die "SERVICE_START_FAILED" "firewalld did not become active."
}

snapshot_existing() {
  local external_backend=""
  mkdir -p "${state_dir}"
  rm -rf -- "${migration_dir}"
  install -d -m 0700 -- "${migration_dir}"
  firewalld_package_installed && installed_package_version >"${migration_dir}/package-version" || true
  [[ -d /etc/firewalld ]] && cp -a -- /etc/firewalld "${migration_dir}/config"
  [[ -f "${managed_rules_file}" ]] && cp -a -- "${managed_rules_file}" "${migration_dir}/managed-rules.json"
  [[ -f "${installed_marker}" ]] && : >"${migration_dir}/was-managed" || true
  if service_active; then : >"${migration_dir}/was-active" || true; fi
  if service_enabled; then : >"${migration_dir}/was-enabled" || true; fi
  external_backend="$(external_firewall_backend || true)"
  if [[ -n "${external_backend}" ]]; then
    # A package-only install or upgrade must not claim ownership of or
    # rewrite an existing iptables ruleset. Keep firewalld stopped.
    if [[ "${external_backend}" != "iptables" || ( "${ONEINSTACK_ACTION:-install}" != "install" && "${ONEINSTACK_ACTION:-install}" != "upgrade" ) ]] || service_active; then
      printf '%s\n' "${external_backend}" >"${migration_dir}/external-backend"
      if [[ "${external_backend}" == "ufw" ]]; then
        [[ -d /etc/ufw ]] && cp -a -- /etc/ufw "${migration_dir}/ufw-config"
        if ufw status 2>/dev/null | grep -Eiq '^Status:[[:space:]]+active'; then
          : >"${migration_dir}/ufw-was-active"
        fi
        if systemd_available && systemctl is-enabled --quiet ufw.service 2>/dev/null; then
          : >"${migration_dir}/ufw-was-enabled"
        fi
      elif [[ "${external_backend}" == "nftables" ]]; then
        require_command nft
        nft list ruleset >"${migration_dir}/nftables-ruleset.nft" ||
          die "EXTERNAL_MIGRATION_FAILED" "The active nftables ruleset could not be saved before firewalld takeover."
        if systemd_available && systemctl is-active --quiet nftables.service 2>/dev/null; then
          : >"${migration_dir}/nftables-was-active"
        fi
        if systemd_available && systemctl is-enabled --quiet nftables.service 2>/dev/null; then
          : >"${migration_dir}/nftables-was-enabled"
        fi
      fi
    fi
  fi
  emit_progress 25 migration.snapshot.created "firewalld package, configuration, rules, and service snapshot created"
}

restore_external_firewall() {
  local external_backend=""
  [[ -r "${migration_dir}/external-backend" ]] || return 0
  external_backend="$(cat "${migration_dir}/external-backend")"
  case "${external_backend}" in
    ufw)
      if [[ -d "${migration_dir}/ufw-config" ]]; then
        rm -rf -- /etc/ufw
        mv -- "${migration_dir}/ufw-config" /etc/ufw
      fi
      if [[ -f "${migration_dir}/ufw-was-enabled" ]] && systemd_available; then
        systemctl enable ufw.service >/dev/null 2>&1 || return 1
      fi
      if [[ -f "${migration_dir}/ufw-was-active" ]]; then
        require_command ufw
        ufw --force enable >/dev/null 2>&1 || return 1
      fi
      ;;
    nftables)
      require_command nft
      nft flush ruleset >/dev/null 2>&1 || return 1
      if [[ -s "${migration_dir}/nftables-ruleset.nft" ]]; then
        nft -f "${migration_dir}/nftables-ruleset.nft" >/dev/null 2>&1 || return 1
      fi
      if systemd_available; then
        [[ -f "${migration_dir}/nftables-was-enabled" ]] && systemctl enable nftables.service >/dev/null 2>&1 || true
        [[ ! -f "${migration_dir}/nftables-was-enabled" ]] && systemctl disable nftables.service >/dev/null 2>&1 || true
        if [[ -f "${migration_dir}/nftables-was-active" ]]; then
          systemctl start nftables.service >/dev/null 2>&1 || return 1
        else
          systemctl stop nftables.service >/dev/null 2>&1 || true
        fi
      fi
      ;;
    *)
      return 1
      ;;
  esac
}

migrate_external_firewall_backend() {
  local external_backend=""
  [[ -r "${migration_dir}/external-backend" ]] || return 0
  external_backend="$(cat "${migration_dir}/external-backend")"
  case "${external_backend}" in
    ufw)
      require_command ufw
      ufw --force disable >/dev/null 2>&1 || die "EXTERNAL_MIGRATION_FAILED" "UFW could not be disabled before firewalld takeover."
      if systemd_available; then
        systemctl stop ufw.service >/dev/null 2>&1 || true
        systemctl disable ufw.service >/dev/null 2>&1 || true
      fi
      emit_progress 70 backend.migrated "UFW was disabled after the Panel port was protected for firewalld"
      ;;
    nftables)
      require_command nft
      if systemd_available && systemctl is-active --quiet nftables.service; then
        systemctl stop nftables.service >/dev/null 2>&1 ||
          die "EXTERNAL_MIGRATION_FAILED" "nftables could not be stopped before firewalld takeover."
      fi
      if systemd_available; then
        systemctl disable nftables.service >/dev/null 2>&1 || true
      fi
      nft flush ruleset >/dev/null 2>&1 ||
        die "EXTERNAL_MIGRATION_FAILED" "The active nftables ruleset could not be cleared before firewalld takeover."
      emit_progress 70 backend.migrated "nftables was stopped and cleared after the Panel port was protected for firewalld"
      ;;
    *)
      die "EXTERNAL_MIGRATION_UNSUPPORTED" "Automatic firewalld takeover is supported only for UFW or nftables."
      ;;
  esac
}

takeover_external_firewall() {
  local external_backend=""
  external_backend="$(external_firewall_backend || true)"
  [[ -n "${external_backend}" ]] || return 1
  snapshot_existing
  trap 'restore_existing' ERR
  ensure_panel_port_protected
  migrate_external_firewall_backend
  ensure_service_started
  firewall-cmd --state >/dev/null 2>&1 || die "SERVICE_START_FAILED" "firewalld command interface is not ready after backend migration."
  trap - ERR
  rm -rf -- "${migration_dir}"
  return 0
}

install_firewalld_version_exact() {
  local version="$1"
  case "${package_manager}" in
    apt) DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends "firewalld=${version}" netbase ca-certificates ;;
    dnf) dnf install -y --setopt=install_weak_deps=False "firewalld-${version#*:}" setup ca-certificates ;;
    yum) yum install -y "firewalld-${version#*:}" setup ca-certificates ;;
    zypper) zypper --non-interactive install --no-recommends "firewalld=${version#*:}" netcfg ca-certificates ;;
    *) return 1 ;;
  esac
}

restore_existing() {
  ensure_service_stopped
  if [[ -f "${migration_dir}/package-version" ]]; then
    local old_package_version current_package_version
    old_package_version="$(cat "${migration_dir}/package-version")"
    current_package_version="$(installed_package_version || true)"
    if [[ "${current_package_version}" != "${old_package_version}" ]]; then
      if firewalld_package_installed; then
        remove_firewalld_package || return 1
      fi
      install_firewalld_version_exact "${old_package_version}" || return 1
    fi
  elif firewalld_package_installed; then
    remove_firewalld_package || return 1
  fi
  if [[ -d "${migration_dir}/config" ]]; then
    rm -rf -- /etc/firewalld
    mv -- "${migration_dir}/config" /etc/firewalld
  fi
  if [[ -f "${migration_dir}/managed-rules.json" ]]; then cp -a -- "${migration_dir}/managed-rules.json" "${managed_rules_file}"; else rm -f -- "${managed_rules_file}"; fi
  if systemd_available; then
    systemctl daemon-reload || true
    [[ -f "${migration_dir}/was-enabled" ]] && systemctl enable firewalld.service >/dev/null 2>&1 || true
    [[ ! -f "${migration_dir}/was-enabled" ]] && systemctl disable firewalld.service >/dev/null 2>&1 || true
    if [[ -f "${migration_dir}/was-active" ]]; then
      systemctl start firewalld.service >/dev/null 2>&1 || return 1
      service_active || return 1
    fi
  elif [[ -f "${migration_dir}/was-active" ]]; then
    start_firewalld_without_systemd
    service_active || return 1
  fi
  restore_external_firewall || return 1
  if [[ -f "${migration_dir}/was-managed" ]]; then
    : >"${installed_marker}"
  else
    rm -f -- "${installed_marker}"
  fi
  emit_progress 100 rollback.service.restored "Previous firewalld state restored"
}

commit_state() {
  install -d -m 0750 -- "${state_dir}"
  printf '%s\n' "${requested_version}" >"${state_dir}/requested-version"
  printf '%s\n' "$(runtime_version || true)" >"${state_dir}/runtime-version"
  printf '%s\n' "$(installed_package_version || true)" >"${state_dir}/package-version"
  : >"${installed_marker}"
  rm -f -- "${state_dir}/pending-version"
  rm -rf -- "${migration_dir}"
}

firewalld_configuration_valid() {
  if service_active; then
    command -v firewall-cmd >/dev/null 2>&1 && firewall-cmd --check-config >/dev/null 2>&1
    return
  fi
  command -v firewall-offline-cmd >/dev/null 2>&1 && firewall-offline-cmd --check-config >/dev/null 2>&1
}

ensure_panel_port_protected() {
  local port="${PANEL_PORT:-0}"
  [[ "${port}" =~ ^[0-9]+$ && "${port}" -ge 1 && "${port}" -le 65535 ]] || return 0
  if service_active; then
    firewall-cmd --permanent --add-port="${port}/tcp" >/dev/null
    firewall-cmd --reload >/dev/null
  elif command -v firewall-offline-cmd >/dev/null 2>&1; then
    firewall-offline-cmd --add-port="${port}/tcp" >/dev/null
  else
    die "HOST_DEPENDENCY_UNAVAILABLE" "firewall-offline-cmd is required before protecting the Panel port."
  fi
}

write_default_managed_rules() {
  [[ -f "${managed_rules_file}" ]] || printf '%s\n' '{"managed":{"zones":[],"directRules":[],"icmpBlocks":[],"forwardPorts":[]}}' >"${managed_rules_file}"
  chmod 0600 "${managed_rules_file}"
}
