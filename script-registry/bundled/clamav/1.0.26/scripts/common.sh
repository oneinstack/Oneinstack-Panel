#!/usr/bin/env bash
set -Eeuo pipefail

umask 027

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
component_id=clamav
component_name=ClamAV
package_version=1.0.26
software_version="${SOFTWARE_VERSION:-latest}"
state_root="${ONEINSTACK_COMPONENT_STATE:-/var/lib/oneinstack/components}"
state_dir="${state_root}/${component_id}"
migration_dir="${state_dir}/migration"
external_dir="${state_dir}/external"
config_dir=/etc/oneinstack/clamav
data_dir="${DATA_DIR:-/var/lib/clamav}"
log_dir=/var/log/oneinstack/clamav
runtime_dir=/run/oneinstack-clamav
socket_path="${runtime_dir}/clamd.sock"
clamd_config="${config_dir}/clamd.conf"
freshclam_config="${config_dir}/freshclam.conf"
settings_file="${state_dir}/runtime-settings"
install_parameters_file="${state_dir}/install-parameters"
package_ownership_file="${state_dir}/owned-packages"
package_inventory_before_file="${state_dir}/preinstall-package-inventory"
package_inventory_after_file="${state_dir}/postinstall-package-inventory"
package_version_changes_file="${state_dir}/preexisting-package-version-changes"
runtime_package_state_before_file="${state_dir}/preinstall-runtime-package-states"
apparmor_local_state_file="${state_dir}/apparmor-local-profiles"
repository_bootstrap_file="${state_dir}/repository-bootstrap"
service_name=oneinstack-clamav
update_service_name=oneinstack-clamav-update
update_timer_name=oneinstack-clamav-update.timer
install_mode="${ONEINSTACK_INSTALL_MODE:-center}"
offline_package_path="${ONEINSTACK_OFFLINE_PACKAGE_PATH:-}"
offline_bundle_id="${ONEINSTACK_OFFLINE_BUNDLE_ID:-}"
offline_bundle_digest="${ONEINSTACK_OFFLINE_BUNDLE_DIGEST:-}"
offline_database_max_age_hours=168
apparmor_marker_begin='# BEGIN ONEINSTACK CLAMAV MANAGED RULES'
apparmor_marker_end='# END ONEINSTACK CLAMAV MANAGED RULES'

system_id=""
system_version=""
host_arch=""
package_manager=""
runtime_user=""
runtime_group=""
clamd_binary=""
clamdscan_binary=""
freshclam_binary=""
sigtool_binary=""
clamav_runtime_version=""
cvd_certs_dir=""
database_generated_at_unix=""
repository_bootstrap_profile=""
repository_extension_repo=""
repository_requires_epel_next=false
legacy_repository_cache_dir=""
declare -a runtime_packages=()

die() { printf 'ERROR: %s\n' "$*" >&2; exit 1; }

emit_progress() {
  local fd="${ONEINSTACK_PROGRESS_FD:-}"
  [[ "${fd}" =~ ^[0-9]+$ ]] || return 0
  { printf '{"type":"progress","percent":%s,"code":"%s","message":"%s"}\n' "$1" "$2" "$3" >&"${fd}"; } 2>/dev/null || true
}

require_root() { [[ "$(id -u)" -eq 0 ]] || die 'This action must run as root.'; }

# systemd 219 (notably CentOS 7) does not implement `systemctl show --value`.
# Always parse Property=value so status and conflict checks behave the same on
# every declared systemd generation.
systemctl_property() {
  local unit="$1" property="$2" value
  value="$({ systemctl show --property="${property}" "${unit}" 2>/dev/null || true; } | awk -F= -v property="${property}" '$1 == property {print substr($0, index($0, "=") + 1); exit}')"
  printf '%s' "${value}"
}

validate_path() {
  local value="$1" label="$2"
  [[ "${value}" == /* && "${value}" != / && "$(realpath -m -- "${value}")" == "${value}" ]] || die "${label} must be a normalized absolute path."
  case "${value}" in /etc|/var|/run|/usr|/usr/local|/data|/home|/root) die "${label} is too broad." ;; esac
}

apparmor_is_enabled() {
  if command -v aa-status >/dev/null 2>&1; then
    aa-status --enabled >/dev/null 2>&1 && return 0
  fi
  [[ -r /sys/module/apparmor/parameters/enabled ]] && grep -qi '^Y' /sys/module/apparmor/parameters/enabled
}

selinux_is_enforcing() {
  command -v getenforce >/dev/null 2>&1 && [[ "$(getenforce 2>/dev/null || true)" == Enforcing ]]
}

validate_mandatory_access_prerequisites() {
  if apparmor_is_enabled && ! command -v apparmor_parser >/dev/null 2>&1; then
    die 'APPARMOR_PARSER_UNAVAILABLE: AppArmor is enabled but apparmor_parser is unavailable to load the required scoped ClamAV policy.'
  fi
  # The component intentionally uses isolated Oneinstack paths. Do not guess
  # SELinux type names or relabel a host that has an unknown local policy.
  selinux_is_enforcing &&
    die 'SELINUX_POLICY_REQUIRED: SELinux is enforcing and this host has no verified Oneinstack ClamAV path policy; refusing an installation that would later fail with an opaque access error.'
  # In Bash strict mode, the false SELinux probe above would otherwise become
  # this function's status and terminate a healthy Ubuntu/Debian precheck
  # without emitting an error.
  return 0
}

validate_apparmor_path() {
  local value="$1" label="$2"
  case "${value}" in
    *' '*|*$'\t'*|*'*'*|*'?'*|*'['*|*']'*|*'{'*|*'}'*|*'"'*|*'\\'*)
      die "APPARMOR_PATH_UNSUPPORTED: ${label} contains characters that cannot be safely represented in a generated AppArmor rule."
      ;;
  esac
}

apparmor_marker_is_well_formed() {
  local file="$1" begins ends
  begins="$(grep -Fxc "${apparmor_marker_begin}" "${file}" 2>/dev/null || true)"
  ends="$(grep -Fxc "${apparmor_marker_end}" "${file}" 2>/dev/null || true)"
  [[ ( "${begins}" == 0 && "${ends}" == 0 ) || ( "${begins}" == 1 && "${ends}" == 1 ) ]]
}

strip_managed_apparmor_block() {
  local source="$1" destination="$2"
  awk -v begin="${apparmor_marker_begin}" -v end="${apparmor_marker_end}" '
    $0 == begin { inside = 1; next }
    $0 == end { inside = 0; next }
    !inside { print }
  ' "${source}" >"${destination}"
}

record_apparmor_local_profile() {
  local profile="$1" created="$2" temporary
  install -d -m 0750 -- "${state_dir}"
  temporary="$(mktemp "${state_dir}/.apparmor-local.XXXXXX")"
  if [[ -r "${apparmor_local_state_file}" ]]; then
    awk -F'|' -v profile="${profile}" '$1 != profile { print }' "${apparmor_local_state_file}" >"${temporary}"
  fi
  printf '%s|%s\n' "${profile}" "${created}" >>"${temporary}"
  chmod 0640 "${temporary}"
  mv -f -- "${temporary}" "${apparmor_local_state_file}"
}

emit_apparmor_read_tree() {
  local path="$1"
  printf '%s/ r,\n%s/** mr,\n' "${path}" "${path}"
}

emit_apparmor_write_tree() {
  local path="$1"
  printf '%s/ r,\n%s/** mrwk,\n' "${path}" "${path}"
}

write_apparmor_local_profile() {
  local local_name="$1" profile_kind="$2" destination temporary created=false
  destination="/etc/apparmor.d/local/${local_name}"
  install -d -m 0755 /etc/apparmor.d/local
  if [[ -e "${destination}" ]]; then
    [[ -f "${destination}" && ! -L "${destination}" ]] || die "APPARMOR_LOCAL_POLICY_CONFLICT: ${destination} is not a regular file."
    apparmor_marker_is_well_formed "${destination}" || die "APPARMOR_LOCAL_POLICY_CONFLICT: ${destination} contains an incomplete Oneinstack managed block."
    temporary="$(mktemp "/etc/apparmor.d/local/.${local_name}.XXXXXX")"
    strip_managed_apparmor_block "${destination}" "${temporary}"
    chown --reference="${destination}" "${temporary}"
    chmod --reference="${destination}" "${temporary}"
  else
    temporary="$(mktemp "/etc/apparmor.d/local/.${local_name}.XXXXXX")"
    created=true
    chown root:root "${temporary}"
    chmod 0644 "${temporary}"
  fi
  {
    printf '\n%s\n' "${apparmor_marker_begin}"
    printf '# Scoped paths required by the Oneinstack ClamAV service.\n'
    printf '/etc/oneinstack/ r,\n%s/ r,\n' "${config_dir}"
    emit_apparmor_read_tree "${config_dir}"
    # ClamAV 1.5+ uses signed CVD certificate files.  Older engines such as
    # Debian 12's 1.4 do not ship that directory, so do not create an invalid
    # root-level AppArmor rule or require a path which the runtime never uses.
    if requires_cvd_certificates; then
      emit_apparmor_read_tree "${cvd_certs_dir}"
    fi
    case "${profile_kind}" in
      freshclam)
        emit_apparmor_write_tree "${data_dir}"
        emit_apparmor_write_tree "${log_dir}"
        ;;
      clamd)
        emit_apparmor_read_tree "${data_dir}"
        emit_apparmor_write_tree "${log_dir}"
        printf '%s/ rw,\n%s/** rw,\n' "${runtime_dir}" "${runtime_dir}"
        ;;
      *) die "APPARMOR_PROFILE_UNSUPPORTED: unknown ClamAV AppArmor profile ${profile_kind}." ;;
    esac
    printf '%s\n' "${apparmor_marker_end}"
  } >>"${temporary}"
  mv -f -- "${temporary}" "${destination}"
  record_apparmor_local_profile "${local_name}" "${created}"
}

reload_apparmor_profile() {
  local source_profile="$1"
  apparmor_is_enabled || return 0
  apparmor_parser --replace "${source_profile}" ||
    die "APPARMOR_PROFILE_RELOAD_FAILED: failed to reload ${source_profile}."
}

configure_apparmor_profile() {
  # Bash expands every initializer in a `local` declaration before assigning
  # any of the locals. Keep dependent values separate for `set -u` hosts.
  local local_name profile_kind source_profile
  local_name="$1"
  profile_kind="$2"
  source_profile="/etc/apparmor.d/${local_name}"
  [[ -e "${source_profile}" ]] || return 0
  [[ -r "${source_profile}" ]] || die "APPARMOR_PROFILE_UNREADABLE: ${source_profile} cannot be read."
  grep -Fq "#include <local/${local_name}>" "${source_profile}" ||
    die "APPARMOR_PROFILE_UNSUPPORTED: ${source_profile} does not include its local policy extension."
  write_apparmor_local_profile "${local_name}" "${profile_kind}"
  reload_apparmor_profile "${source_profile}"
}

configure_mandatory_access() {
  validate_mandatory_access_prerequisites
  apparmor_is_enabled || return 0
  validate_apparmor_path "${config_dir}" config_dir
  validate_apparmor_path "${data_dir}" data_dir
  validate_apparmor_path "${log_dir}" log_dir
  validate_apparmor_path "${runtime_dir}" runtime_dir
  if requires_cvd_certificates; then
    [[ -n "${cvd_certs_dir}" ]] || die 'CVD_CERTS_UNAVAILABLE: cannot create AppArmor policy without the verified CVD certificate directory.'
    validate_apparmor_path "${cvd_certs_dir}" cvd_certs_dir
  fi
  configure_apparmor_profile usr.bin.freshclam freshclam
  configure_apparmor_profile usr.sbin.clamd clamd
}

remove_apparmor_overrides() {
  [[ -r "${apparmor_local_state_file}" ]] || return 0
  local local_name created destination source_profile temporary has_rules
  while IFS='|' read -r local_name created; do
    case "${local_name}" in usr.bin.freshclam|usr.sbin.clamd) ;; *) continue ;; esac
    destination="/etc/apparmor.d/local/${local_name}"
    source_profile="/etc/apparmor.d/${local_name}"
    [[ -f "${destination}" && ! -L "${destination}" ]] || continue
    apparmor_marker_is_well_formed "${destination}" || { printf 'WARNING: APPARMOR_LOCAL_POLICY_CONFLICT: preserving malformed %s\n' "${destination}" >&2; continue; }
    temporary="$(mktemp "/etc/apparmor.d/local/.${local_name}.XXXXXX")"
    strip_managed_apparmor_block "${destination}" "${temporary}"
    chown --reference="${destination}" "${temporary}"
    chmod --reference="${destination}" "${temporary}"
    has_rules="$(grep -Ev '^[[:space:]]*(#.*)?$' "${temporary}" || true)"
    if [[ "${created}" == true && -z "${has_rules}" ]]; then
      rm -f -- "${destination}" "${temporary}"
    else
      mv -f -- "${temporary}" "${destination}"
    fi
    if apparmor_is_enabled && [[ -e "${source_profile}" ]]; then
      apparmor_parser --replace "${source_profile}" ||
        printf 'WARNING: APPARMOR_PROFILE_RELOAD_FAILED: preserving host policy state after removing %s.\n' "${destination}" >&2
    fi
  done <"${apparmor_local_state_file}"
  rm -f -- "${apparmor_local_state_file}"
}

architecture_name() {
  case "$(uname -m)" in
    x86_64|amd64) printf 'amd64\n' ;;
    aarch64|arm64) printf 'arm64\n' ;;
    *) die "HOST_ARCH_UNSUPPORTED: unsupported architecture $(uname -m)." ;;
  esac
}

supported_host() {
  case "${system_id}:${system_version%%.*}" in
    centos:7) ;;
    *) return 1 ;;
  esac
}

centos7_eol_runtime_profile() {
  [[ "${system_id}" == centos && "${system_version%%.*}" == 7 ]]
}

detect_host() {
  [[ -r /etc/os-release ]] || die 'HOST_PROFILE_UNAVAILABLE: /etc/os-release is unavailable.'
  # shellcheck disable=SC1091
  source /etc/os-release
  system_id="${ID,,}"
  system_version="${VERSION_ID:-}"
  if [[ "${system_id}" == centos && "${NAME:-} ${VARIANT:-} ${VARIANT_ID:-}" =~ [Ss]tream ]]; then system_id=centos-stream; fi
  case "${system_id}" in opensuse-leap|opensuse-tumbleweed|opensuse|sles|ubuntu|debian|rhel|rocky|almalinux|ol|centos|centos-stream|fedora|amzn) ;; *) die "HOST_PROFILE_UNAVAILABLE: unsupported Linux platform ${system_id:-unknown}." ;; esac
  [[ -n "${system_version}" ]] || die "HOST_PROFILE_UNAVAILABLE: ${system_id} VERSION_ID is unavailable."
  host_arch="$(architecture_name)"
  supported_host || die "HOST_PROFILE_UNAVAILABLE: ${system_id} ${system_version} ${host_arch} has no ClamAV profile."
  if [[ "${system_id}" == centos && "${system_version%%.*}" =~ ^(7|8)$ ]]; then
    printf 'WARNING: LEGACY_CENTOS_PROFILE: CentOS Linux %s uses archived repositories and has no vendor security-update guarantee.\n' "${system_version}" >&2
  fi
  case "${system_id}" in
    ubuntu|debian)
      command -v apt-get >/dev/null 2>&1 || die "HOST_DEPENDENCY_UNSUPPORTED: ${system_id} requires apt-get."
      package_manager=apt
      ;;
    sles)
      command -v zypper >/dev/null 2>&1 || die 'HOST_DEPENDENCY_UNSUPPORTED: SLES requires zypper.'
      package_manager=zypper
      ;;
    centos)
      if [[ "${system_version%%.*}" == 7 ]] && command -v yum >/dev/null 2>&1; then
        package_manager=yum
      elif command -v dnf >/dev/null 2>&1; then
        package_manager=dnf
      elif command -v yum >/dev/null 2>&1; then
        package_manager=yum
      else
        die 'HOST_DEPENDENCY_UNSUPPORTED: CentOS requires yum or dnf.'
      fi
      ;;
    rhel|rocky|almalinux|ol|centos-stream|amzn)
      if command -v dnf >/dev/null 2>&1; then
        package_manager=dnf
      elif command -v yum >/dev/null 2>&1; then
        package_manager=yum
      else
        die "HOST_DEPENDENCY_UNSUPPORTED: ${system_id} requires dnf or yum."
      fi
      ;;
    *) die "HOST_PROFILE_UNAVAILABLE: ${system_id} ${system_version} has no verified ClamAV package-manager profile." ;;
  esac
  configure_runtime_packages
  configure_online_repository_profile
}

configure_runtime_packages() {
  case "${package_manager}" in
    apt) runtime_packages=(clamav clamav-daemon clamav-freshclam clamdscan) ;;
    dnf|yum)
      # AL2023 switched its default stream to the versioned ClamAV 1.4
      # packages. Do not apply the EL/EPEL package names to Amazon Linux.
      if [[ "${system_id}" == amzn ]]; then
        runtime_packages=(clamav1.4 clamd1.4 clamav1.4-freshclam)
      else
        runtime_packages=(clamav clamd clamav-freshclam)
      fi
      ;;
    zypper) runtime_packages=(clamav) ;;
  esac
}

validate_inputs() {
  [[ "${software_version}" == latest ]] || die 'SOFTWARE_VERSION must be latest; runtime version is selected by the verified host package profile.'
  [[ "${install_mode}" == center || "${install_mode}" == offline ]] || die 'ONEINSTACK_INSTALL_MODE must be center or offline.'
  validate_path "${state_root}" ONEINSTACK_COMPONENT_STATE
  validate_path "${data_dir}" DATA_DIR
  [[ "${data_dir}" != "${config_dir}" && "${data_dir}" != "${config_dir}"/* && "${config_dir}" != "${data_dir}"/* ]] || die 'DATA_DIR must not overlap the managed configuration directory.'
  if [[ "${install_mode}" == offline ]]; then
    [[ "${offline_package_path}" == /* && "$(realpath -m -- "${offline_package_path}")" == "${offline_package_path}" ]] || die 'OFFLINE_BUNDLE_INVALID: offline mode requires a normalized absolute Bundle path.'
    [[ "${offline_bundle_digest}" =~ ^[0-9a-f]{64}$ && "${offline_bundle_id}" == "sha256:${offline_bundle_digest}" ]] || die 'OFFLINE_BUNDLE_IDENTITY_MISMATCH: Panel Bundle identity and digest are required in offline mode.'
  elif [[ -n "${offline_package_path}" || -n "${offline_bundle_id}" || -n "${offline_bundle_digest}" ]]; then
    die 'OFFLINE_BUNDLE_INVALID: offline Bundle metadata is not allowed in online mode.'
  fi
}

emit_install_profile() {
  local packages
  packages="$(IFS=,; printf '%s' "${runtime_packages[*]}")"
  printf 'install_profile=clamav\ncomponent_package_version=%s\nsoftware_version=%s\npackage_manager=%s\nruntime_packages=%s\ndata_dir=%s\nservice=%s\ninstall_mode=%s\n' \
    "${package_version}" "${software_version}" "${package_manager}" "${packages}" "${data_dir}" "${service_name}" "${install_mode}"
}

is_managed() { [[ -f "${state_dir}/installed" ]]; }

package_is_installed() {
  local package="$1"
  case "${package_manager}" in
    apt) dpkg-query -W -f='${db:Status-Status}' "${package}" 2>/dev/null | grep -Fxq installed ;;
    dnf|yum|zypper) rpm -q "${package}" >/dev/null 2>&1 ;;
  esac
}

has_existing_packages() {
  local package
  for package in "${runtime_packages[@]}"; do package_is_installed "${package}" && return 0; done
  return 1
}

find_existing() {
  has_existing_packages || [[ -f /etc/clamav/clamd.conf || -f /etc/clamav/freshclam.conf || -d /etc/clamd.d ]] ||
    (database_file_present "${data_dir}" main && database_file_present "${data_dir}" daily) || return 1
  printf '%s\n' native-clamav
}

record_active_native_units() {
  local target="$1" unit
  : >"${target}"
  for unit in clamav-daemon.service clamav-daemon.socket clamav-freshclam.service clamav-freshclam.timer clamd.service clamd@scan.service clamd@scan.socket freshclam.service freshclam.timer; do
    systemctl is-active --quiet "${unit}" 2>/dev/null && printf '%s\n' "${unit}" >>"${target}" || true
  done
}

record_enabled_native_units() {
  local target="$1" unit state
  : >"${target}"
  for unit in clamav-daemon.service clamav-daemon.socket clamav-freshclam.service clamav-freshclam.timer clamd.service clamd@scan.service clamd@scan.socket freshclam.service freshclam.timer; do
    state="$(systemctl is-enabled "${unit}" 2>/dev/null || true)"
    case "${state}" in
      enabled|enabled-runtime|linked|linked-runtime|alias) printf '%s\n' "${unit}" >>"${target}" ;;
    esac
  done
}

snapshot_existing() {
  [[ -d "${migration_dir}" || -d "${external_dir}" ]] && return 0
  find_existing >/dev/null || return 0
  rm -rf -- "${migration_dir}"
  install -d -m 0700 -- "${migration_dir}" "${migration_dir}/config"
  has_existing_packages && : >"${migration_dir}/had-packages" || true
  local package
  for package in "${runtime_packages[@]}"; do package_is_installed "${package}" && printf '%s\n' "${package}" >>"${migration_dir}/preexisting-packages" || true; done
  record_active_native_units "${migration_dir}/active-native-units"
  record_enabled_native_units "${migration_dir}/enabled-native-units"
  [[ ! -d /etc/clamav ]] || cp -a -- /etc/clamav "${migration_dir}/config/clamav"
  [[ ! -d /etc/clamd.d ]] || cp -a -- /etc/clamd.d "${migration_dir}/config/clamd.d"
  [[ ! -d "${data_dir}" ]] || cp -a -- "${data_dir}" "${migration_dir}/data"
}

disable_native_units() {
  local unit load_state
  for unit in clamav-daemon.service clamav-daemon.socket clamav-freshclam.service clamav-freshclam.timer clamd.service clamd@scan.service clamd@scan.socket freshclam.service freshclam.timer; do
    load_state="$(systemctl_property "${unit}" LoadState)"
    [[ -z "${load_state}" || "${load_state}" == not-found ]] && continue
    systemctl disable --now "${unit}" || die "NATIVE_UNIT_DISABLE_FAILED: cannot stop conflicting native unit ${unit}."
  done
  [[ "$(native_unit_diagnostics)" == none ]] || die "NATIVE_UNIT_CONFLICT: native ClamAV units remain active: $(native_unit_diagnostics)."
}

native_unit_diagnostics() {
  local unit state output=''
  for unit in clamav-daemon.service clamav-daemon.socket clamav-freshclam.service clamav-freshclam.timer clamd.service clamd@scan.service clamd@scan.socket freshclam.service freshclam.timer; do
    state="$(systemctl_property "${unit}" ActiveState)"
    [[ -n "${state}" && "${state}" != inactive ]] || continue
    output+="${output:+,}${unit}:${state}"
  done
  printf '%s' "${output:-none}"
}

restore_native_units() {
  local state="$1" unit
  [[ -r "${state}" ]] || return 0
  while IFS= read -r unit; do [[ -z "${unit}" ]] || systemctl start "${unit}" 2>/dev/null || true; done <"${state}"
}

restore_native_unit_enablement() {
  local state="$1" unit
  [[ -r "${state}" ]] || return 0
  while IFS= read -r unit; do [[ -z "${unit}" ]] || systemctl enable "${unit}" 2>/dev/null || true; done <"${state}"
}

configure_online_repository_profile() {
  repository_bootstrap_profile=""
  repository_extension_repo=""
  repository_requires_epel_next=false
  local major="${system_version%%.*}"
  case "${system_id}:${major}" in
    rocky:8|almalinux:8)
      repository_bootstrap_profile=epel-compatible
      repository_extension_repo=powertools
      ;;
    rocky:9|rocky:10|almalinux:9|almalinux:10)
      repository_bootstrap_profile=epel-compatible
      repository_extension_repo=crb
      ;;
    centos-stream:9)
      repository_bootstrap_profile=epel-stream
      repository_extension_repo=crb
      repository_requires_epel_next=true
      ;;
    centos-stream:10)
      repository_bootstrap_profile=epel-stream
      repository_extension_repo=crb
      ;;
    rhel:8|rhel:9|rhel:10)
      repository_bootstrap_profile=epel-rhel
      ;;
    ol:8|ol:9|ol:10)
      repository_bootstrap_profile=epel-oracle
      repository_extension_repo="ol${major}_codeready_builder"
      ;;
    centos:7|centos:8)
      # CentOS Linux has reached EOL.  Keep its archived repositories isolated
      # from the host's original repo definitions and use them only for this
      # component transaction.
      repository_bootstrap_profile=centos-vault
      ;;
  esac
}

centos_legacy_repository_ids() {
  case "${system_version%%.*}" in
    7)
      printf '%s\n' oneinstack-clamav-centos7-vault-base oneinstack-clamav-centos7-vault-updates oneinstack-clamav-centos7-vault-extras oneinstack-clamav-epel7-archive
      ;;
    8)
      printf '%s\n' oneinstack-clamav-centos8-vault-baseos oneinstack-clamav-centos8-vault-appstream oneinstack-clamav-centos8-vault-extras oneinstack-clamav-centos8-vault-powertools oneinstack-clamav-epel8
      ;;
    *) die "HOST_PROFILE_UNAVAILABLE: CentOS ${system_version} has no verified Vault repository profile." ;;
  esac
}

# CentOS Linux 7 and EPEL 7 are archived. The vetted HTTPS mirror is first on
# x86_64 because legacy Yum clients can receive inconsistent metadata from the
# official archive CDN; the official archive remains the fallback. RPM
# signatures remain checked against component-pinned CentOS/EPEL keys, so a
# mirror cannot supply an untrusted package.
centos7_vault_archive_roots() {
  if [[ "${host_arch}" == arm64 ]]; then
    printf '%s\n' https://vault.centos.org/altarch/7.9.2009
  else
    printf '%s\n' https://mirrors.cloud.tencent.com/centos-vault/7.9.2009
    printf '%s\n' https://vault.centos.org/7.9.2009
  fi
}

centos7_epel_archive_roots() {
  if [[ "${host_arch}" == arm64 ]]; then
    printf '%s\n' https://archives.fedoraproject.org/pub/archive/epel/7
  else
    printf '%s\n' https://mirrors.cloud.tencent.com/epel-archive/7
    printf '%s\n' https://archives.fedoraproject.org/pub/archive/epel/7
  fi
}

write_yum_baseurl_list() {
  local roots_function="$1" relative_path="$2" root prefix='baseurl='
  while IFS= read -r root; do
    [[ -n "${root}" ]] || continue
    printf '%s%s/%s\n' "${prefix}" "${root}" "${relative_path}"
    prefix='        '
  done < <("${roots_function}")
}

run_online_package_manager() {
  local repository
  local -a repository_options=()
  if [[ "${repository_bootstrap_profile}" == centos-vault ]]; then
    repository_options=(--disablerepo='*')
    [[ -z "${legacy_repository_cache_dir}" ]] || repository_options+=("--setopt=cachedir=${legacy_repository_cache_dir}")
    while IFS= read -r repository; do
      [[ -n "${repository}" ]] && repository_options+=("--enablerepo=${repository}")
    done < <(centos_legacy_repository_ids)
  fi
  "${package_manager}" "${repository_options[@]}" "$@"
}

online_runtime_packages_available() {
  local package diagnostics="${1:-false}"
  case "${package_manager}" in
    apt)
      for package in "${runtime_packages[@]}"; do
        package_is_installed "${package}" && continue
        apt-cache show "${package}" >/dev/null 2>&1 && continue
        printf 'repository_package_unavailable=%s\n' "${package}" >&2
        return 1
      done
      ;;
    dnf|yum)
      for package in "${runtime_packages[@]}"; do
        package_is_installed "${package}" && continue
        case "${package_manager}" in
          # Yum 3 (CentOS 7) accepts the availability selector as a positional
          # argument. `--available` is a DNF-only option and exits before Yum
          # contacts the configured archive repositories.
          yum)
            if [[ "${diagnostics}" == true ]]; then
              run_online_package_manager -q list available "${package}" && continue
            else
              run_online_package_manager -q list available "${package}" >/dev/null 2>&1 && continue
            fi
            ;;
          dnf)
            if [[ "${diagnostics}" == true ]]; then
              run_online_package_manager -q list --available "${package}" && continue
            else
              run_online_package_manager -q list --available "${package}" >/dev/null 2>&1 && continue
            fi
            ;;
        esac
        printf 'repository_package_unavailable=%s\n' "${package}" >&2
        return 1
      done
      ;;
    zypper)
      for package in "${runtime_packages[@]}"; do
        package_is_installed "${package}" && continue
        zypper --non-interactive --no-refresh info "${package}" >/dev/null 2>&1 && continue
        printf 'repository_package_unavailable=%s\n' "${package}" >&2
        return 1
      done
      ;;
  esac
}

report_repository_bootstrap_plan() {
  [[ -n "${repository_bootstrap_profile}" ]] || return 1
  printf 'repository_bootstrap=%s\n' "${repository_bootstrap_profile}"
  [[ -z "${repository_extension_repo}" ]] || printf 'repository_extension_repo=%s\n' "${repository_extension_repo}"
  [[ "${repository_requires_epel_next}" != true ]] || printf 'repository_extra_release=epel-next\n'
}

validate_online_repositories() {
  [[ "${install_mode}" == center ]] || return 0
  if online_runtime_packages_available; then
    printf 'repository_profile=preconfigured\n'
    return 0
  fi
  case "${package_manager}" in
    dnf|yum)
      report_repository_bootstrap_plan && return 0
      die "REPOSITORY_UNAVAILABLE: ${system_id} has no verified signed source for the selected ClamAV runtime packages."
      ;;
    apt) die "REPOSITORY_UNAVAILABLE: ${system_id} has no signed cached source for the selected ClamAV runtime packages." ;;
    zypper) die "REPOSITORY_UNAVAILABLE: ${system_id} has no signed configured source for the selected ClamAV runtime packages." ;;
  esac
}

record_repository_bootstrap() {
  local kind="$1" value="$2"
  install -d -m 0750 -- "${state_dir}"
  printf '%s|%s\n' "${kind}" "${value}" >>"${repository_bootstrap_file}"
}

epel_signing_key_file() {
  local major="${system_version%%.*}"
  case "${major}" in
    7) printf '%s/keys/RPM-GPG-KEY-EPEL-7\n' "${script_dir}" ;;
    8) printf '%s/keys/RPM-GPG-KEY-EPEL-8\n' "${script_dir}" ;;
    9) printf '%s/keys/RPM-GPG-KEY-EPEL-9\n' "${script_dir}" ;;
    10) printf '%s/keys/RPM-GPG-KEY-EPEL-10\n' "${script_dir}" ;;
    *) die "HOST_PROFILE_UNAVAILABLE: EPEL key profile is unavailable for EL${major}." ;;
  esac
}

verify_and_import_epel_signing_key() {
  local key expected_sha256 expected_fingerprint expected_key_id actual_sha256 prior_key current_key major="${system_version%%.*}"
  key="$(epel_signing_key_file)"
  case "${major}" in
    7)
      expected_sha256=028b9accc59bab1d21f2f3f544df5469910581e728a64fd8c411a725a82300c2
      expected_fingerprint=91E97D7C4A5E96F17F3E888F6A2FAEA2352C64E5
      expected_key_id=352c64e5
      ;;
    8)
      expected_sha256=cd1db21a863185127f2e3b264c97fb1c6c44c316385707999041ea475c110d1c
      expected_fingerprint=94E279EB8D8F25B21810ADF121EA45AB2F86D6A1
      expected_key_id=2f86d6a1
      ;;
    9)
      expected_sha256=fcf0eab4f05a1c0de6363ac4b707600a27a9d774e9b491059e59e6921b255a84
      expected_fingerprint=FF8AD1344597106ECE813B918A3872BF3228467C
      expected_key_id=3228467c
      ;;
    10)
      expected_sha256=de390fc168eae5ab2852e9e93d34a0b9ddf05cf9ce90ee28d97de26a4b1f6b93
      expected_fingerprint=7D8D15CBFC4E62688591FB2633D98517E37ED158
      expected_key_id=e37ed158
      ;;
  esac
  [[ -r "${key}" ]] || die "REPOSITORY_TRUST_MISSING: packaged EPEL ${major} signing key is unavailable."
  actual_sha256="$(sha256sum "${key}" | awk '{print $1}')"
  [[ "${actual_sha256}" == "${expected_sha256}" ]] || die "REPOSITORY_TRUST_MISMATCH: packaged EPEL ${major} signing key digest differs from its pinned fingerprint profile."
  command -v rpm >/dev/null 2>&1 || die 'HOST_DEPENDENCY_UNSUPPORTED: rpm is required to import the verified EPEL signing key.'
  prior_key="$(rpm -q --qf '%{NAME}-%{VERSION}-%{RELEASE}' "gpg-pubkey-${expected_key_id}" 2>/dev/null || true)"
  rpm --import "${key}" || die "REPOSITORY_TRUST_MISMATCH: cannot import the verified EPEL ${major} signing key."
  if [[ -z "${prior_key}" ]]; then
    current_key="$(rpm -q --qf '%{NAME}-%{VERSION}-%{RELEASE}' "gpg-pubkey-${expected_key_id}" 2>/dev/null || true)"
    [[ -n "${current_key}" ]] || die "REPOSITORY_TRUST_MISMATCH: imported EPEL ${major} key is not present in the RPM keyring."
    record_repository_bootstrap imported_key "${current_key}"
  fi
  printf 'repository_epel_key_fingerprint=%s\n' "${expected_fingerprint}"
}

centos_vault_signing_key_file() {
  case "${system_version%%.*}" in
    7) printf '%s/keys/RPM-GPG-KEY-CentOS-7\n' "${script_dir}" ;;
    8) printf '%s/keys/RPM-GPG-KEY-CentOS-Official\n' "${script_dir}" ;;
    *) die "HOST_PROFILE_UNAVAILABLE: CentOS ${system_version} has no verified Vault signing-key profile." ;;
  esac
}

verify_and_import_centos_vault_signing_key() {
  local key expected_sha256 expected_fingerprint expected_key_id actual_sha256 prior_key current_key major="${system_version%%.*}"
  key="$(centos_vault_signing_key_file)"
  case "${major}" in
    7)
      expected_sha256=8b48b04b336bd725b9e611c441c65456a4168083c4febc28e88828d8ec14827f
      expected_fingerprint=6341AB2753D78A78A7C27BB124C6A8A7F4A80EB5
      expected_key_id=f4a80eb5
      ;;
    8)
      expected_sha256=146059788b214d7ba0dd70c1cf21111e594c6cfde201da8a9a88fe7101be8a78
      expected_fingerprint=99DB70FAE1D7CE227FB6488205B555B38483C65D
      expected_key_id=8483c65d
      ;;
  esac
  [[ -r "${key}" ]] || die "REPOSITORY_TRUST_MISSING: packaged CentOS ${major} Vault signing key is unavailable."
  actual_sha256="$(sha256sum "${key}" | awk '{print $1}')"
  [[ "${actual_sha256}" == "${expected_sha256}" ]] || die "REPOSITORY_TRUST_MISMATCH: packaged CentOS ${major} Vault signing-key digest differs from its pinned fingerprint profile."
  command -v rpm >/dev/null 2>&1 || die 'HOST_DEPENDENCY_UNSUPPORTED: rpm is required to import the verified CentOS Vault signing key.'
  prior_key="$(rpm -q --qf '%{NAME}-%{VERSION}-%{RELEASE}' "gpg-pubkey-${expected_key_id}" 2>/dev/null || true)"
  rpm --import "${key}" || die "REPOSITORY_TRUST_MISMATCH: cannot import the verified CentOS ${major} Vault signing key."
  if [[ -z "${prior_key}" ]]; then
    current_key="$(rpm -q --qf '%{NAME}-%{VERSION}-%{RELEASE}' "gpg-pubkey-${expected_key_id}" 2>/dev/null || true)"
    [[ -n "${current_key}" ]] || die "REPOSITORY_TRUST_MISMATCH: imported CentOS ${major} Vault key is not present in the RPM keyring."
    record_repository_bootstrap imported_key "${current_key}"
  fi
  printf 'repository_centos_vault_key_fingerprint=%s\n' "${expected_fingerprint}"
}

install_pinned_repository_key_file() {
  local source="$1" name="$2" destination
  destination="/etc/pki/rpm-gpg/oneinstack-clamav-${name}.key"
  install -d -m 0755 /etc/pki/rpm-gpg
  if [[ -e "${destination}" || -L "${destination}" ]]; then
    [[ -f "${destination}" && ! -L "${destination}" ]] || die "REPOSITORY_KEY_CONFLICT: ${destination} is not a regular file."
    cmp -s "${source}" "${destination}" || die "REPOSITORY_KEY_CONFLICT: ${destination} is managed by another source."
  else
    install -m 0644 "${source}" "${destination}"
    record_repository_bootstrap repository_key_file "${destination}"
  fi
  printf '%s\n' "${destination}"
}

write_centos_legacy_repository_file() {
  local centos_key="$1" epel_key="$2" major="${system_version%%.*}" destination temporary vault_root
  destination="/etc/yum.repos.d/oneinstack-clamav-centos${major}-vault.repo"
  install -d -m 0755 /etc/yum.repos.d
  temporary="$(mktemp "/etc/yum.repos.d/.oneinstack-clamav-centos${major}-vault.XXXXXX")"
  case "${major}" in
    7)
      {
        printf '[oneinstack-clamav-centos7-vault-base]\nname=Oneinstack ClamAV CentOS 7 Vault - Base\n'
        write_yum_baseurl_list centos7_vault_archive_roots 'os/$basearch/'
        printf 'enabled=0\ngpgcheck=1\ngpgkey=file://%s\n\n' "${centos_key}"
        printf '[oneinstack-clamav-centos7-vault-updates]\nname=Oneinstack ClamAV CentOS 7 Vault - Updates\n'
        write_yum_baseurl_list centos7_vault_archive_roots 'updates/$basearch/'
        printf 'enabled=0\ngpgcheck=1\ngpgkey=file://%s\n\n' "${centos_key}"
        printf '[oneinstack-clamav-centos7-vault-extras]\nname=Oneinstack ClamAV CentOS 7 Vault - Extras\n'
        write_yum_baseurl_list centos7_vault_archive_roots 'extras/$basearch/'
        printf 'enabled=0\ngpgcheck=1\ngpgkey=file://%s\n\n' "${centos_key}"
        printf '[oneinstack-clamav-epel7-archive]\nname=Oneinstack ClamAV EPEL 7 Archive\n'
        write_yum_baseurl_list centos7_epel_archive_roots '$basearch/'
        printf 'enabled=0\ngpgcheck=1\ngpgkey=file://%s\n' "${epel_key}"
      } >"${temporary}"
      ;;
    8)
      {
        printf '[oneinstack-clamav-centos8-vault-baseos]\nname=Oneinstack ClamAV CentOS 8 Vault - BaseOS\nbaseurl=https://vault.centos.org/8.5.2111/BaseOS/$basearch/os/\nenabled=0\ngpgcheck=1\ngpgkey=file://%s\n\n' "${centos_key}"
        printf '[oneinstack-clamav-centos8-vault-appstream]\nname=Oneinstack ClamAV CentOS 8 Vault - AppStream\nbaseurl=https://vault.centos.org/8.5.2111/AppStream/$basearch/os/\nenabled=0\ngpgcheck=1\ngpgkey=file://%s\n\n' "${centos_key}"
        printf '[oneinstack-clamav-centos8-vault-extras]\nname=Oneinstack ClamAV CentOS 8 Vault - Extras\nbaseurl=https://vault.centos.org/8.5.2111/extras/$basearch/os/\nenabled=0\ngpgcheck=1\ngpgkey=file://%s\n\n' "${centos_key}"
        printf '[oneinstack-clamav-centos8-vault-powertools]\nname=Oneinstack ClamAV CentOS 8 Vault - PowerTools\nbaseurl=https://vault.centos.org/8.5.2111/PowerTools/$basearch/os/\nenabled=0\ngpgcheck=1\ngpgkey=file://%s\n\n' "${centos_key}"
        printf '[oneinstack-clamav-epel8]\nname=Oneinstack ClamAV EPEL 8\nbaseurl=https://dl.fedoraproject.org/pub/epel/8/Everything/$basearch/\nenabled=0\ngpgcheck=1\ngpgkey=file://%s\n' "${epel_key}"
      } >"${temporary}"
      ;;
    *) rm -f -- "${temporary}"; die "HOST_PROFILE_UNAVAILABLE: CentOS ${system_version} has no verified Vault repository profile." ;;
  esac
  chmod 0644 "${temporary}"
  if [[ -e "${destination}" || -L "${destination}" ]]; then
    [[ -f "${destination}" && ! -L "${destination}" ]] || die "REPOSITORY_CONFIGURATION_CONFLICT: ${destination} is not a regular file."
    cmp -s "${temporary}" "${destination}" || die "REPOSITORY_CONFIGURATION_CONFLICT: ${destination} is managed by another source."
    rm -f -- "${temporary}"
  else
    mv -f -- "${temporary}" "${destination}"
    record_repository_bootstrap repository_file "${destination}"
  fi
}

bootstrap_centos_legacy_vault() {
  local centos_key epel_key
  verify_and_import_centos_vault_signing_key
  verify_and_import_epel_signing_key
  centos_key="$(install_pinned_repository_key_file "$(centos_vault_signing_key_file)" "centos${system_version%%.*}")"
  epel_key="$(install_pinned_repository_key_file "$(epel_signing_key_file)" "epel${system_version%%.*}")"
  write_centos_legacy_repository_file "${centos_key}" "${epel_key}"
}

prepare_centos_legacy_repository_cache() {
  local cache_dir="${state_dir}/yum-cache"
  if [[ -e "${cache_dir}" || -L "${cache_dir}" ]]; then
    [[ -d "${cache_dir}" && ! -L "${cache_dir}" ]] || die "REPOSITORY_CACHE_CONFLICT: ${cache_dir} is not a regular directory."
  else
    install -d -m 0750 -- "${cache_dir}"
    record_repository_bootstrap repository_cache_dir "${cache_dir}"
  fi
  legacy_repository_cache_dir="${cache_dir}"
}

enable_dnf_extension_repository() {
  local repository="$1" helper=dnf-plugins-core helper_was_installed=false
  [[ -n "${repository}" ]] || return 0
  if "${package_manager}" -q repolist --enabled "${repository}" 2>/dev/null | awk -v repository="${repository}" '$1 == repository { found=1 } END { exit !found }'; then
    return 0
  fi
  if ! "${package_manager}" config-manager --help >/dev/null 2>&1; then
    rpm -q "${helper}" >/dev/null 2>&1 && helper_was_installed=true
    "${package_manager}" install -y "${helper}" || die "REPOSITORY_BOOTSTRAP_FAILED: ${system_id} requires ${helper} to enable ${repository}."
    [[ "${helper_was_installed}" == true ]] || record_repository_bootstrap helper_package "${helper}"
  fi
  "${package_manager}" config-manager --set-enabled "${repository}" || die "REPOSITORY_BOOTSTRAP_FAILED: cannot enable the signed ${repository} repository for ${system_id}."
  if ! "${package_manager}" -q repolist --enabled "${repository}" 2>/dev/null | awk -v repository="${repository}" '$1 == repository { found=1 } END { exit !found }'; then
    die "REPOSITORY_BOOTSTRAP_FAILED: ${repository} is not enabled after the requested repository transition."
  fi
  record_repository_bootstrap repository "${repository}"
}

enable_rhel_codeready_builder() {
  local major="${system_version%%.*}" rpm_arch repository
  command -v subscription-manager >/dev/null 2>&1 || die 'REPOSITORY_BOOTSTRAP_FAILED: registered RHEL with subscription-manager is required to enable CodeReady Builder.'
  rpm_arch="$(rpm --eval '%{_arch}' 2>/dev/null || true)"
  [[ -n "${rpm_arch}" && "${rpm_arch}" != '%{_arch}' ]] || die 'HOST_ARCH_UNSUPPORTED: cannot determine the RPM architecture for the RHEL CodeReady Builder profile.'
  repository="codeready-builder-for-rhel-${major}-${rpm_arch}-rpms"
  subscription-manager repos --enable "${repository}" || die "REPOSITORY_BOOTSTRAP_FAILED: RHEL subscription access is required to enable ${repository}."
  record_repository_bootstrap subscription_repository "${repository}"
}

epel_release_urls() {
  local major="${system_version%%.*}"
  printf 'https://dl.fedoraproject.org/pub/epel/epel-release-latest-%s.noarch.rpm\n' "${major}"
  [[ "${repository_requires_epel_next}" != true ]] || printf 'https://dl.fedoraproject.org/pub/epel/epel-next-release-latest-9.noarch.rpm\n'
}

install_verified_epel_release() {
  local package url
  local -a urls=()
  verify_and_import_epel_signing_key
  mapfile -t urls < <(epel_release_urls)
  for url in "${urls[@]}"; do
    package=epel-release
    [[ "${url}" == *epel-next-release* ]] && package=epel-next-release
    rpm -q "${package}" >/dev/null 2>&1 && continue
    "${package_manager}" --setopt=gpgcheck=1 --setopt=repo_gpgcheck=1 --setopt=localpkg_gpgcheck=1 install -y "${url}" ||
      die "REPOSITORY_BOOTSTRAP_FAILED: cannot install the verified ${package} release package for ${system_id}."
    rpm -q "${package}" >/dev/null 2>&1 || die "REPOSITORY_BOOTSTRAP_FAILED: ${package} was not installed."
    record_repository_bootstrap release_package "${package}"
  done
}

bootstrap_online_repositories() {
  [[ -n "${repository_bootstrap_profile}" ]] || return 1
  case "${repository_bootstrap_profile}" in
    epel-compatible|epel-stream|epel-oracle) enable_dnf_extension_repository "${repository_extension_repo}" ;;
    epel-rhel) enable_rhel_codeready_builder ;;
    centos-vault) bootstrap_centos_legacy_vault ;;
    *) die "HOST_PROFILE_UNAVAILABLE: unknown repository bootstrap profile ${repository_bootstrap_profile}." ;;
  esac
  [[ "${repository_bootstrap_profile}" == centos-vault ]] || install_verified_epel_release
  [[ "${repository_bootstrap_profile}" != centos-vault ]] || prepare_centos_legacy_repository_cache
  printf 'repository_bootstrap_completed=%s\n' "${repository_bootstrap_profile}"
}

refresh_centos_legacy_repository_metadata() {
  [[ "${repository_bootstrap_profile}" == centos-vault ]] || return 0
  # Yum 3 makecache eagerly downloads optional comps/filelists/updateinfo data.
  # A damaged optional EPEL 7 archive response can then block installation even
  # when primary metadata and packages are usable. Reset only this component's
  # isolated cache; the subsequent package probe fetches just what it needs.
  case "${package_manager}" in
    yum) run_online_package_manager -q clean metadata ;;
    dnf) run_online_package_manager -q makecache --refresh ;;
    *) return 0 ;;
  esac || die 'REPOSITORY_METADATA_UNAVAILABLE: CentOS Vault/EPEL archive metadata could not be refreshed; check DNS, HTTPS, proxy, and the configured signed archive endpoints.'
  printf 'repository_metadata_cache_reset=centos-vault\n'
}

prepare_online_repositories() {
  [[ "${install_mode}" == center ]] || return 0
  online_runtime_packages_available && { printf 'repository_profile=preconfigured\n'; return 0; }
  case "${package_manager}" in
    dnf|yum)
      bootstrap_online_repositories || die "REPOSITORY_UNAVAILABLE: ${system_id} has no verified signed source for the selected ClamAV runtime packages."
      refresh_centos_legacy_repository_metadata
      online_runtime_packages_available true || die "REPOSITORY_UNAVAILABLE: ${system_id} repository bootstrap completed but the selected ClamAV packages remain unavailable."
      ;;
    apt|zypper) validate_online_repositories ;;
  esac
}

capture_package_ownership() {
  install -d -m 0750 -- "${state_dir}"
  : >"${state_dir}/preinstall-packages"
  : >"${runtime_package_state_before_file}"
  package_inventory >"${package_inventory_before_file}"
  local package state
  for package in "${runtime_packages[@]}"; do
    state="$(package_transaction_state "${package}")"
    printf '%s|%s\n' "${package}" "${state}" >>"${runtime_package_state_before_file}"
    package_is_installed "${package}" && printf '%s\n' "${package}" >>"${state_dir}/preinstall-packages" || true
  done
}

package_transaction_state() {
  local package="$1" state
  case "${package_manager}" in
    apt)
      state="$(dpkg-query -W -f='${db:Status-Status}' "${package}" 2>/dev/null || true)"
      printf '%s' "${state:-absent}"
      ;;
    dnf|yum|zypper)
      rpm -q "${package}" >/dev/null 2>&1 && printf installed || printf absent
      ;;
  esac
}

package_inventory() {
  case "${package_manager}" in
    apt)
      dpkg-query -W -f='${binary:Package}|${Version}|${Architecture}|${db:Status-Status}\n' 2>/dev/null |
        awk -F'|' '$4 == "installed" {print $1 "|" $2 "|" $3}' | sort
      ;;
    dnf|yum|zypper)
      rpm -qa --qf '%{NAME}|%{EPOCHNUM}:%{VERSION}-%{RELEASE}|%{ARCH}\n' 2>/dev/null | sort
      ;;
  esac
}

record_owned_packages() {
  [[ -r "${package_inventory_before_file}" ]] || die 'PACKAGE_OWNERSHIP_UNAVAILABLE: pre-install package inventory is missing.'
  package_inventory >"${package_inventory_after_file}"
  : >"${package_ownership_file}"
  : >"${package_version_changes_file}"
  awk -F'|' -v owned="${package_ownership_file}" -v changed="${package_version_changes_file}" '
    NR == FNR { before[$1 SUBSEP $3] = $2; next }
    {
      key = $1 SUBSEP $3
      if (!(key in before)) {
        print $1 "|" $2 "|" $3 >> owned
      } else if (before[key] != $2) {
        print $1 "|" before[key] "|" $2 "|" $3 >> changed
      }
    }
  ' "${package_inventory_before_file}" "${package_inventory_after_file}"
  chmod 0640 "${package_ownership_file}" "${package_version_changes_file}"
  # Keep task logs auditable without dumping an unrestricted dpkg/rpm inventory.
  # This makes rollback behavior distinguishable from unrelated packages that
  # were already present on the host and therefore must be preserved.
  printf 'package_ownership_added=%s\npackage_ownership_preexisting_version_changes=%s\n' \
    "$(awk 'END {print NR + 0}' "${package_ownership_file}")" \
    "$(awk 'END {print NR + 0}' "${package_version_changes_file}")"
}

record_runtime_package_lock() {
  local destination="${state_dir}/runtime-package-lock" package
  : >"${destination}"
  printf 'installMode=%s\npackageManager=%s\n' "${install_mode}" "${package_manager}" >>"${destination}"
  for package in "${runtime_packages[@]}"; do
    package_is_installed "${package}" || continue
    case "${package_manager}" in
      apt) dpkg-query -W -f='package=%{binary:Package}|%{Version}|%{Architecture}\n' "${package}" >>"${destination}" ;;
      dnf|yum|zypper) rpm -q --qf 'package=%{NAME}|%{VERSION}-%{RELEASE}|%{ARCH}|%{SIGPGP:pgpsig}\n' "${package}" >>"${destination}" ;;
    esac
  done
  chmod 0640 "${destination}"
}

prepare_native_package_data_directory() {
  # Ubuntu's clamav-freshclam maintainer script creates a child below this
  # native path but does not recover a missing parent after a package purge.
  # Create only the package-owned parent, before either online or offline dpkg
  # runs; other package managers retain their vendor-managed directory flow.
  [[ "${package_manager}" == apt ]] || return 0
  local native_data_dir=/var/lib/clamav
  if [[ -e "${native_data_dir}" || -L "${native_data_dir}" ]]; then
    [[ -d "${native_data_dir}" && ! -L "${native_data_dir}" ]] ||
      die "NATIVE_DATA_DIR_CONFLICT: ${native_data_dir} must be a real directory for the selected APT ClamAV packages."
    printf 'native_package_data_dir=present\n'
    return 0
  fi
  install -d -m 0755 -- "${native_data_dir}"
  printf 'native_package_data_dir=created\n'
}

install_packages() {
  prepare_native_package_data_directory
  if [[ "${install_mode}" == offline ]]; then
    capture_package_ownership
    install_offline_packages
  else
    prepare_online_repositories
    capture_package_ownership
    case "${package_manager}" in
      apt) export DEBIAN_FRONTEND=noninteractive; apt-get update; apt-get install -y --no-install-recommends --no-upgrade "${runtime_packages[@]}" ;;
      dnf) run_online_package_manager --setopt=best=False install -y "${runtime_packages[@]}" ;;
      yum) run_online_package_manager install -y "${runtime_packages[@]}" ;;
      zypper) zypper --non-interactive --gpg-auto-import-keys --no-allow-downgrade --no-allow-vendor-change install --no-recommends "${runtime_packages[@]}" ;;
    esac
  fi
  record_owned_packages
  record_runtime_package_lock
}

remove_owned_packages() {
  local -a packages=()
  local package before_state current_state existing partial_count=0 known
  if [[ -r "${package_ownership_file}" ]]; then
    mapfile -t packages < <(awk -F'|' 'NF {print $1}' "${package_ownership_file}")
  fi
  # `dpkg-query` inventory intentionally records only configured packages.
  # After a failed apt transaction, direct ClamAV packages can be unpacked or
  # half-configured and must still be removed when they were absent beforehand.
  if [[ "${package_manager}" == apt && -r "${runtime_package_state_before_file}" ]]; then
    while IFS='|' read -r package before_state; do
      [[ "${before_state}" == absent ]] || continue
      current_state="$(package_transaction_state "${package}")"
      case "${current_state}" in absent|not-installed|config-files) continue ;; esac
      known=false
      if ((${#packages[@]} > 0)); then
        for existing in "${packages[@]}"; do
          if [[ "${existing}" == "${package}" ]]; then
            known=true
            break
          fi
        done
      fi
      if [[ "${known}" == false ]]; then
        packages+=("${package}")
        ((++partial_count))
      fi
    done <"${runtime_package_state_before_file}"
  fi
  ((${#packages[@]} > 0)) || return 0
  ((partial_count == 0)) || printf 'package_ownership_partial_runtime=%s\n' "${partial_count}"
  case "${package_manager}" in
    apt) DEBIAN_FRONTEND=noninteractive apt-get purge -y "${packages[@]}" ;;
    dnf|yum) "${package_manager}" remove -y "${packages[@]}" ;;
    zypper) zypper --non-interactive remove "${packages[@]}" ;;
  esac
}

report_preexisting_package_version_changes() {
  [[ -s "${package_version_changes_file}" ]] || return 0
  printf 'WARNING: PACKAGE_VERSION_CHANGED: pre-existing packages changed during the package-manager transaction; preserving them instead of attempting an unsafe downgrade:\n' >&2
  sed 's/^/  /' "${package_version_changes_file}" >&2
}

rollback_repository_bootstrap() {
  local kind value
  [[ -r "${repository_bootstrap_file}" ]] || return 0
  while IFS='|' read -r kind value; do
    case "${kind}" in
      release_package|helper_package)
        rpm -q "${value}" >/dev/null 2>&1 && "${package_manager}" remove -y "${value}" || true
        ;;
      imported_key) rpm -e "${value}" >/dev/null 2>&1 || true ;;
      repository)
        "${package_manager}" config-manager --set-disabled "${value}" >/dev/null 2>&1 || true
        ;;
      repository_file)
        [[ "${value}" == /etc/yum.repos.d/oneinstack-clamav-centos*-vault.repo && -f "${value}" && ! -L "${value}" ]] && rm -f -- "${value}" || true
        ;;
      repository_key_file)
        [[ "${value}" == /etc/pki/rpm-gpg/oneinstack-clamav-*.key && -f "${value}" && ! -L "${value}" ]] && rm -f -- "${value}" || true
        ;;
      repository_cache_dir)
        [[ "${value}" == "${state_dir}/yum-cache" && -d "${value}" && ! -L "${value}" ]] && rm -rf -- "${value}" || true
        ;;
      subscription_repository)
        subscription-manager repos --disable "${value}" >/dev/null 2>&1 || true
        ;;
    esac
  done < <(tac "${repository_bootstrap_file}")
}

resolve_runtime_identity() {
  local candidate
  # Fedora/EPEL ClamAV 1.4 creates the scanner account as `clamscan` via
  # sysusers.d.  It is the daemon account in that profile; `clamupdate` is
  # reserved for the native database layout and must not be selected for the
  # Oneinstack daemon.  Keep the historical Debian/older EL names as fallback.
  for candidate in clamscan clamav clamd; do
    if id "${candidate}" >/dev/null 2>&1; then runtime_user="${candidate}"; runtime_group="$(id -gn "${candidate}")"; break; fi
  done
  [[ -n "${runtime_user}" && -n "${runtime_group}" ]] ||
    die 'RUNTIME_ACCOUNT_UNAVAILABLE: no native ClamAV scanner account (clamscan, clamav, or clamd) is available after package installation.'
  clamd_binary="$(command -v clamd 2>/dev/null || true)"
  clamdscan_binary="$(command -v clamdscan 2>/dev/null || true)"
  freshclam_binary="$(command -v freshclam 2>/dev/null || true)"
  sigtool_binary="$(command -v sigtool 2>/dev/null || true)"
  [[ -x "${clamd_binary}" && -x "${clamdscan_binary}" && -x "${freshclam_binary}" && -x "${sigtool_binary}" ]] || die 'RUNTIME_BINARY_UNAVAILABLE: clamd, clamdscan, freshclam, and sigtool must be supplied by the selected profile.'
  clamav_runtime_version="$(runtime_engine_version)"
  ensure_cvd_certificates
}

as_runtime_user() {
  if [[ "$(id -u)" -eq 0 ]] && command -v runuser >/dev/null 2>&1; then runuser -u "${runtime_user}" -- "$@"; else "$@"; fi
}

runtime_engine_version() {
  clamscan --version 2>/dev/null | sed -nE 's/^ClamAV ([0-9]+(\.[0-9]+){1,3}).*/\1/p' | head -n1 || true
}

requires_cvd_certificates() {
  local version="${clamav_runtime_version:-}"
  [[ "${version}" =~ ^1\.([5-9]|[1-9][0-9]+)\. ]] || [[ "${version}" =~ ^[2-9][0-9]*\. ]]
}

package_file_list() {
  local package="$1"
  case "${package_manager}" in
    apt) dpkg-query -L "${package}" 2>/dev/null || true ;;
    dnf|yum|zypper) rpm -ql "${package}" 2>/dev/null || true ;;
  esac
}

find_cvd_certs_dir() {
  local candidate package entry
  for candidate in /etc/clamav/certs /etc/clamd/certs /etc/clamd.d/certs; do
    [[ -f "${candidate}/clamav.crt" ]] && { printf '%s\n' "${candidate}"; return 0; }
  done
  for package in clamav clamav-base clamav-daemon clamd clamav-filesystem; do
    package_is_installed "${package}" || continue
    while IFS= read -r entry; do
      [[ "${entry}" == */clamav.crt && -f "${entry}" ]] || continue
      candidate="$(dirname -- "${entry}")"
      printf '%s\n' "${candidate}"
      return 0
    done < <(package_file_list "${package}")
  done
  return 1
}

ensure_cvd_certificates() {
  requires_cvd_certificates || return 0
  cvd_certs_dir="$(find_cvd_certs_dir || true)"
  [[ -n "${cvd_certs_dir}" && -r "${cvd_certs_dir}/clamav.crt" ]] ||
    die "CVD_CERTS_UNAVAILABLE: ClamAV ${clamav_runtime_version} requires a readable clamav.crt supplied by the verified ${package_manager} package profile."
  as_runtime_user test -r "${cvd_certs_dir}/clamav.crt" ||
    die "CVD_CERTS_PERMISSION_DENIED: runtime user ${runtime_user} cannot read ${cvd_certs_dir}/clamav.crt."
}

cvd_certificates_state() {
  requires_cvd_certificates || { printf not-required; return; }
  [[ -n "${cvd_certs_dir}" && -r "${cvd_certs_dir}/clamav.crt" ]] || { printf missing; return; }
  as_runtime_user test -r "${cvd_certs_dir}/clamav.crt" && printf present || printf permission-denied
}

setting_value() {
  local key="$1" fallback="$2" value
  value="$(awk -F= -v key="${key}" '$1 == key {print substr($0, index($0, "=") + 1); exit}' "${settings_file}" 2>/dev/null || true)"
  printf '%s' "${value:-${fallback}}"
}
current_update_mode() { setting_value update_mode auto; }
current_checks_per_day() { setting_value checks_per_day 12; }
current_database_mirror() { setting_value database_mirror database.clamav.net; }
current_max_threads() { setting_value max_threads 10; }
current_max_queue() { setting_value max_queue 100; }
current_max_scan_size() { setting_value max_scan_size_mb 400; }
current_max_file_size() { setting_value max_file_size_mb 100; }
current_max_recursion() { setting_value max_recursion 16; }
current_max_files() { setting_value max_files 10000; }
current_log_verbose() { setting_value log_verbose no; }

write_settings() {
  local destination="$1" update_mode="$2" checks="$3" mirror="$4" threads="$5" queue="$6" scan_size="$7" file_size="$8" recursion="$9" files="${10}" verbose="${11}"
  cat >"${destination}" <<EOF
install_mode=${install_mode}
update_mode=${update_mode}
checks_per_day=${checks}
database_mirror=${mirror}
max_threads=${threads}
max_queue=${queue}
max_scan_size_mb=${scan_size}
max_file_size_mb=${file_size}
max_recursion=${recursion}
max_files=${files}
log_verbose=${verbose}
EOF
  chmod 0640 "${destination}"
}

write_clamd_config() {
  local destination="$1" target_socket="$2" threads="$3" queue="$4" scan_size="$5" file_size="$6" recursion="$7" files="$8" verbose="$9"
  cat >"${destination}" <<EOF
# Managed by Oneinstack ClamAV component. Do not edit while Panel management is enabled.
DatabaseDirectory ${data_dir}
${cvd_certs_dir:+CVDCertsDirectory ${cvd_certs_dir}}
LocalSocket ${target_socket}
LocalSocketMode 0660
LocalSocketGroup ${runtime_group}
LogFile ${log_dir}/clamd.log
LogTime yes
LogVerbose ${verbose}
Foreground yes
MaxThreads ${threads}
MaxQueue ${queue}
MaxScanSize ${scan_size}M
MaxFileSize ${file_size}M
MaxRecursion ${recursion}
MaxFiles ${files}
EOF
  chmod 0640 "${destination}"
}

write_freshclam_config() {
  local destination="$1" checks="$2" mirror="$3"
  cat >"${destination}" <<EOF
# Managed by Oneinstack ClamAV component. Do not edit while Panel management is enabled.
DatabaseDirectory ${data_dir}
${cvd_certs_dir:+CVDCertsDirectory ${cvd_certs_dir}}
DatabaseOwner ${runtime_user}
UpdateLogFile ${log_dir}/freshclam.log
LogTime yes
Checks ${checks}
DatabaseMirror ${mirror}
EOF
  chmod 0640 "${destination}"
}

write_systemd_units() {
  local update_mode checks interval
  update_mode="${1:-$(current_update_mode)}"
  checks="${2:-$(current_checks_per_day)}"
  interval=$((86400 / checks))
  install -d -m 0755 /etc/systemd/system
  cat >"/etc/systemd/system/${service_name}.service" <<EOF
[Unit]
Description=Oneinstack ClamAV daemon
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=${runtime_user}
Group=${runtime_group}
RuntimeDirectory=oneinstack-clamav
RuntimeDirectoryMode=0750
ExecStart=${clamd_binary} --foreground --config-file=${clamd_config}
Restart=on-failure
RestartSec=5
PrivateTmp=true
NoNewPrivileges=true

[Install]
WantedBy=multi-user.target
EOF
  if [[ "${install_mode}" == center && "${update_mode}" == auto ]]; then
    cat >"/etc/systemd/system/${update_service_name}.service" <<EOF
[Unit]
Description=Oneinstack ClamAV database update
After=network-online.target
Wants=network-online.target

[Service]
Type=oneshot
ExecStart=${freshclam_binary} --config-file=${freshclam_config}
EOF
    cat >"/etc/systemd/system/${update_timer_name}" <<EOF
[Unit]
Description=Oneinstack ClamAV database update schedule

[Timer]
OnBootSec=5m
OnUnitActiveSec=${interval}s
Persistent=true
Unit=${update_service_name}.service

[Install]
WantedBy=timers.target
EOF
  else
    systemctl disable --now "${update_timer_name}" 2>/dev/null || true
    rm -f -- "/etc/systemd/system/${update_service_name}.service" "/etc/systemd/system/${update_timer_name}"
  fi
  systemctl daemon-reload
}

configure_runtime_permissions() {
  install -d -m 0750 -o "${runtime_user}" -g "${runtime_group}" -- "${data_dir}" "${log_dir}"
  install -d -m 0750 -o root -g "${runtime_group}" -- "${config_dir}"
  chown root:"${runtime_group}" "${config_dir}" "${clamd_config}"
  # FreshClam parses its configuration before dropping to DatabaseOwner.
  # Keep this file root-only and run the oneshot as root so 1.5+ behaves like
  # the native distro service while database/log access remains clamav-owned.
  chown root:root "${freshclam_config}"
  chmod 0750 "${config_dir}"
  chmod 0640 "${clamd_config}"
  chmod 0600 "${freshclam_config}"
  chown -R "${runtime_user}:${runtime_group}" "${data_dir}" "${log_dir}"
  configure_mandatory_access
}

validate_freshclam_config_file() {
  local config_file="$1" output mode owner
  [[ -f "${config_file}" ]] || die "FRESHCLAM_CONFIG_INVALID: ${config_file} is missing."
  mode="$(stat -c '%a' "${config_file}" 2>/dev/null || true)"
  owner="$(stat -c '%U' "${config_file}" 2>/dev/null || true)"
  [[ "${mode}" == 600 && "${owner}" == root ]] ||
    die "FRESHCLAM_CONFIG_PERMISSION_INVALID: ${config_file} must be owned by root with mode 0600."
  output="$("${freshclam_binary}" --debug --config-file="${config_file}" --version 2>&1)" || {
    printf '%s\n' "${output}" | sed -E 's#(https?|ftp)://[^/@[:space:]]+:[^/@[:space:]]+@#\1://[REDACTED]@#g' >&2
    die 'FRESHCLAM_CONFIG_INVALID: FreshClam cannot read or parse the managed configuration.'
  }
}

validate_freshclam_config() { validate_freshclam_config_file "${freshclam_config}"; }

database_file_present() {
  local directory="$1" stem="$2"
  compgen -G "${directory}/${stem}.cvd" >/dev/null || compgen -G "${directory}/${stem}.cld" >/dev/null
}

validate_database_files() {
  local directory="$1" stem file
  for stem in main daily bytecode; do
    database_file_present "${directory}" "${stem}" || die "DATABASE_MISSING: ${stem} CVD/CLD is missing from ${directory}."
    if [[ -n "${sigtool_binary}" ]]; then
      file="$(find "${directory}" -maxdepth 1 -type f \( -name "${stem}.cvd" -o -name "${stem}.cld" \) -print -quit)"
      "${sigtool_binary}" --info "${file}" >/dev/null 2>&1 || die "DATABASE_INVALID: ${file} is not a valid ClamAV CVD/CLD file."
    fi
  done
}

has_virus_database() { database_file_present "${data_dir}" main && database_file_present "${data_dir}" daily && database_file_present "${data_dir}" bytecode; }

copy_offline_database() {
  local database_root="${offline_package_path}/database" file
  [[ -d "${database_root}" ]] || die 'OFFLINE_DATABASE_MISSING: Bundle database directory is missing.'
  validate_database_files "${database_root}"
  install -d -m 0750 -o "${runtime_user}" -g "${runtime_group}" -- "${data_dir}"
  rm -f -- "${data_dir}/main.cvd" "${data_dir}/main.cld" "${data_dir}/daily.cvd" "${data_dir}/daily.cld" "${data_dir}/bytecode.cvd" "${data_dir}/bytecode.cld"
  for file in main daily bytecode; do find "${database_root}" -maxdepth 1 -type f \( -name "${file}.cvd" -o -name "${file}.cld" \) -exec install -m 0640 -o "${runtime_user}" -g "${runtime_group}" -- '{}' "${data_dir}/" \; ; done
}

database_age_hours() {
  local timestamp now
  timestamp="$(awk -F= '$1 == "databaseGeneratedAtUnix" {print $2; exit}' "${offline_package_path}/bundle-info" 2>/dev/null || true)"
  [[ "${timestamp}" =~ ^[0-9]+$ ]] || { printf unknown; return; }
  now="$(date -u +%s)"
  ((now >= timestamp)) || { printf unknown; return; }
  printf '%s' "$(((now - timestamp) / 3600))"
}

database_age_hours_from_data() {
  local newest now timestamp="${database_generated_at_unix:-}"
  now="$(date -u +%s)"
  if [[ "${install_mode}" == offline && "${timestamp}" =~ ^[0-9]+$ ]]; then
    ((now >= timestamp)) || { printf unknown; return; }
    printf '%s' "$(((now - timestamp) / 3600))"
    return
  fi
  newest="$(find "${data_dir}" -maxdepth 1 -type f \( -name 'main.cvd' -o -name 'main.cld' -o -name 'daily.cvd' -o -name 'daily.cld' -o -name 'bytecode.cvd' -o -name 'bytecode.cld' \) -printf '%T@\n' 2>/dev/null | sort -nr | head -n1 | cut -d. -f1)"
  [[ "${newest}" =~ ^[0-9]+$ ]] || { printf unknown; return; }
  ((now >= newest)) || { printf unknown; return; }
  printf '%s' "$(((now - newest) / 3600))"
}

validate_database_age() {
  local age
  age="$(database_age_hours_from_data)"
  [[ "${age}" =~ ^[0-9]+$ ]] || die 'DATABASE_AGE_UNKNOWN: no readable ClamAV database timestamp is available.'
  ((age <= offline_database_max_age_hours)) || die "DATABASE_STALE: ClamAV database is older than ${offline_database_max_age_hours} hours."
}

validate_offline_database_age() {
  local timestamp now max_age
  timestamp="$(awk -F= '$1 == "databaseGeneratedAtUnix" {print $2; exit}' "${offline_package_path}/bundle-info")"
  [[ "${timestamp}" =~ ^[0-9]+$ ]] || die 'OFFLINE_DATABASE_INVALID: Bundle databaseGeneratedAtUnix is missing.'
  now="$(date -u +%s)"; max_age=$((offline_database_max_age_hours * 3600))
  ((timestamp <= now && now - timestamp <= max_age)) || die "OFFLINE_DATABASE_EXPIRED: bundled virus database must be no older than ${offline_database_max_age_hours} hours."
}

validate_bundle_inventory() {
  local inventory listed digest relative
  inventory="$(mktemp)"; listed="$(mktemp)"
  find "${offline_package_path}" ! -type d ! -type f -print -quit | grep -q . && die 'OFFLINE_BUNDLE_INVALID: special files are forbidden.'
  find "${offline_package_path}" -type l -print -quit | grep -q . && die 'OFFLINE_BUNDLE_INVALID: symbolic links are forbidden.'
  (cd -- "${offline_package_path}" && find . -type f ! -name files.sha256 -print | sed 's#^./##' | sort) >"${inventory}"
  while read -r digest relative; do
    [[ "${digest}" =~ ^[0-9a-f]{64}$ && -n "${relative}" && "${relative}" != /* && "${relative}" != ../* && "${relative}" != */../* ]] || die 'OFFLINE_BUNDLE_INVALID: files.sha256 contains an unsafe entry.'
    printf '%s\n' "${relative}"
  done <"${offline_package_path}/files.sha256" | sort >"${listed}"
  cmp -s "${inventory}" "${listed}" || die 'OFFLINE_BUNDLE_INVALID: files.sha256 inventory does not match Bundle contents.'
  rm -f -- "${inventory}" "${listed}"
}

validate_package_lock() {
  local package_root="$1" inventory listed digest relative
  inventory="$(mktemp)"; listed="$(mktemp)"
  (cd -- "${offline_package_path}" && find "${package_root#"${offline_package_path}"/}" -maxdepth 1 -type f \( -name '*.deb' -o -name '*.rpm' \) -print | sed 's#^./##' | sort) >"${inventory}"
  while read -r digest relative; do
    [[ "${digest}" =~ ^[0-9a-f]{64}$ && "${relative}" == packages/* && "${relative}" != */../* ]] || die 'OFFLINE_BUNDLE_INVALID: package-lock.sha256 contains an unsafe entry.'
    printf '%s\n' "${relative}"
  done <"${offline_package_path}/package-lock.sha256" | sort >"${listed}"
  cmp -s "${inventory}" "${listed}" || die 'OFFLINE_DEPENDENCY_MISSING: package lock does not cover the complete local package closure.'
  rm -f -- "${inventory}" "${listed}"
  (cd -- "${offline_package_path}" && sha256sum --check --strict package-lock.sha256) >/dev/null || die 'OFFLINE_BUNDLE_CHECKSUM_MISMATCH: package lock verification failed.'
}

validate_offline_runtime_packages() {
  local package_root="$1" package found file
  for package in "${runtime_packages[@]}"; do
    found=false
    case "${package_manager}" in
      apt)
        while IFS= read -r file; do
          [[ "$(dpkg-deb -f "${file}" Package 2>/dev/null || true)" == "${package}" ]] && { found=true; break; }
        done < <(find "${package_root}" -maxdepth 1 -type f -name '*.deb' -print)
        ;;
      dnf|yum|zypper)
        while IFS= read -r file; do
          [[ "$(rpm -qp --qf '%{NAME}' "${file}" 2>/dev/null || true)" == "${package}" ]] && { found=true; break; }
        done < <(find "${package_root}" -maxdepth 1 -type f -name '*.rpm' -print)
        ;;
    esac
    [[ "${found}" == true ]] || die "OFFLINE_DEPENDENCY_MISSING: Bundle does not contain required runtime package ${package}."
  done
}

validate_offline_dependency_closure() {
  local package_root="$1"
  local -a packages=()
  case "${package_manager}" in
    apt)
      mapfile -t packages < <(find "${package_root}" -maxdepth 1 -type f -name '*.deb' -print | sort)
      ((${#packages[@]} > 0)) || die 'OFFLINE_DEPENDENCY_MISSING: no local DEB packages were bundled.'
      dpkg --dry-run -i "${packages[@]}" >/dev/null 2>&1 ||
        die 'OFFLINE_DEPENDENCY_MISSING: the local DEB Bundle does not satisfy the target host dependency closure.'
      ;;
    dnf|yum)
      mapfile -t packages < <(find "${package_root}" -maxdepth 1 -type f -name '*.rpm' -print | sort)
      ((${#packages[@]} > 0)) || die 'OFFLINE_DEPENDENCY_MISSING: no local RPM packages were bundled.'
      "${package_manager}" --disablerepo='*' --setopt=install_weak_deps=False --setopt=tsflags=test install -y "${packages[@]}" >/dev/null 2>&1 ||
        die 'OFFLINE_DEPENDENCY_MISSING: the local RPM Bundle does not satisfy the target host dependency closure.'
      ;;
    zypper)
      mapfile -t packages < <(find "${package_root}" -maxdepth 1 -type f -name '*.rpm' -print | sort)
      ((${#packages[@]} > 0)) || die 'OFFLINE_DEPENDENCY_MISSING: no local RPM packages were bundled.'
      zypper --non-interactive --disable-repositories --no-refresh --dry-run install --no-recommends "${packages[@]}" >/dev/null 2>&1 ||
        die 'OFFLINE_DEPENDENCY_MISSING: the local RPM Bundle does not satisfy the target host dependency closure.'
      ;;
  esac
}

validate_offline_bundle() {
  [[ "${install_mode}" == offline ]] || return 0
  [[ -d "${offline_package_path}" && ! -L "${offline_package_path}" && -f "${offline_package_path}/manifest.yaml" && -f "${offline_package_path}/bundle-info" && -f "${offline_package_path}/files.sha256" && -f "${offline_package_path}/package-lock.sha256" && -f "${offline_package_path}/repository-lock" ]] || die 'OFFLINE_BUNDLE_INVALID: Bundle metadata is missing.'
  validate_bundle_inventory
  (cd -- "${offline_package_path}" && sha256sum --check --strict files.sha256) >/dev/null || die 'OFFLINE_BUNDLE_CHECKSUM_MISMATCH: Bundle content verification failed.'
  grep -Eq '^[[:space:]]+id:[[:space:]]+clamav[[:space:]]*$' "${offline_package_path}/manifest.yaml" || die 'OFFLINE_BUNDLE_IDENTITY_MISMATCH: Bundle manifest component is not ClamAV.'
  grep -Eq "^[[:space:]]+version:[[:space:]]+${package_version//./\\.}[[:space:]]*$" "${offline_package_path}/manifest.yaml" || die 'OFFLINE_BUNDLE_IDENTITY_MISMATCH: Bundle manifest package version does not match.'
  local key value component='' bundle_package='' bundle_software='' bundle_os='' bundle_version='' bundle_arch='' database_timestamp=''
  while IFS='=' read -r key value; do
    case "${key}" in component) component="${value}" ;; packageVersion) bundle_package="${value}" ;; softwareVersion) bundle_software="${value}" ;; osId) bundle_os="${value}" ;; osVersion) bundle_version="${value}" ;; architecture) bundle_arch="${value}" ;; databaseGeneratedAtUnix) database_timestamp="${value}" ;; *) die "OFFLINE_BUNDLE_INVALID: unknown bundle-info field ${key}." ;; esac
  done <"${offline_package_path}/bundle-info"
  [[ "${component}" == "${component_id}" && "${bundle_package}" == "${package_version}" && "${bundle_software}" == "${software_version}" && "${bundle_os}" == "${system_id}" && "${bundle_version}" == "${system_version}" && "${bundle_arch}" == "${host_arch}" ]] || die 'OFFLINE_BUNDLE_PLATFORM_MISMATCH: Bundle does not match this component, version, system, or architecture.'
  [[ "${database_timestamp}" =~ ^[0-9]+$ ]] || die 'OFFLINE_DATABASE_INVALID: Bundle database timestamp is invalid.'
  validate_offline_database_age
  database_generated_at_unix="${database_timestamp}"
  local package_root="${offline_package_path}/packages/${system_id}/${system_version}/${host_arch}"
  [[ -d "${package_root}" ]] || die 'OFFLINE_DEPENDENCY_MISSING: package directory is missing.'
  grep -Fxq "packageManager=${package_manager}" "${offline_package_path}/repository-lock" || die 'OFFLINE_BUNDLE_PLATFORM_MISMATCH: repository lock package manager does not match the host profile.'
  grep -Eq '^sourceConfigSha256=[0-9a-f]{64}$' "${offline_package_path}/repository-lock" || die 'OFFLINE_REPOSITORY_TRUST_MISSING: Bundle lacks a locked repository source configuration.'
  grep -Eq '^(gpgFingerprint|keySha256)=[0-9A-Fa-f]{40,64}$' "${offline_package_path}/repository-lock" || die 'OFFLINE_REPOSITORY_TRUST_MISSING: Bundle lacks verified repository GPG/key metadata.'
  validate_package_lock "${package_root}"
  case "${package_manager}" in
    apt) find "${package_root}" -maxdepth 1 -type f -name '*.deb' -print -quit | grep -q . || die 'OFFLINE_DEPENDENCY_MISSING: no local DEB packages were bundled.' ;;
    dnf|yum|zypper) find "${package_root}" -maxdepth 1 -type f -name '*.rpm' -print -quit | grep -q . || die 'OFFLINE_DEPENDENCY_MISSING: no local RPM packages were bundled.' ;;
  esac
  validate_offline_runtime_packages "${package_root}"
  validate_offline_dependency_closure "${package_root}"
  validate_database_files "${offline_package_path}/database"
}

install_offline_packages() {
  local package_root="${offline_package_path}/packages/${system_id}/${system_version}/${host_arch}"
  local -a packages=()
  case "${package_manager}" in
    apt) mapfile -t packages < <(find "${package_root}" -maxdepth 1 -type f -name '*.deb' -print | sort); ((${#packages[@]} > 0)) || die 'OFFLINE_DEPENDENCY_MISSING: no DEB packages were bundled.'; DEBIAN_FRONTEND=noninteractive dpkg -i "${packages[@]}" || die 'OFFLINE_DEPENDENCY_MISSING: local DEB dependency installation failed.' ;;
    dnf|yum) mapfile -t packages < <(find "${package_root}" -maxdepth 1 -type f -name '*.rpm' -print | sort); ((${#packages[@]} > 0)) || die 'OFFLINE_DEPENDENCY_MISSING: no RPM packages were bundled.'; "${package_manager}" --disablerepo='*' --setopt=install_weak_deps=False install -y "${packages[@]}" || die 'OFFLINE_DEPENDENCY_MISSING: local RPM dependency installation failed.' ;;
    zypper) mapfile -t packages < <(find "${package_root}" -maxdepth 1 -type f -name '*.rpm' -print | sort); ((${#packages[@]} > 0)) || die 'OFFLINE_DEPENDENCY_MISSING: no RPM packages were bundled.'; zypper --non-interactive --disable-repositories --no-refresh install --no-recommends "${packages[@]}" || die 'OFFLINE_DEPENDENCY_MISSING: local SLES/openSUSE dependency installation failed.' ;;
  esac
}

run_initial_database_update() {
  [[ "${install_mode}" == center ]] || return 0
  systemctl start "${update_service_name}.service" || { freshclam_failure_detail; die 'DATABASE_UPDATE_FAILED: initial FreshClam update failed.'; }
  has_virus_database || die 'DATABASE_UPDATE_FAILED: FreshClam completed without main, daily, and bytecode databases.'
}

wait_for_healthy() {
  local attempts=30
  while ((attempts > 0)); do is_healthy && return 0; sleep 2; ((attempts--)); done
  return 1
}

is_healthy() {
  systemctl is-active --quiet "${service_name}.service" 2>/dev/null && [[ -S "${socket_path}" ]] && has_virus_database && "${clamdscan_binary:-/usr/bin/false}" --config-file="${clamd_config}" --ping 1 >/dev/null 2>&1
}

health_failure_detail() {
  printf 'service=%s socket=%s clamdscan=%s database=%s update_timer=%s\n' "$(systemctl is-active "${service_name}.service" 2>/dev/null || true)" "$([[ -S "${socket_path}" ]] && printf present || printf missing)" "$([[ -x "${clamdscan_binary}" ]] && printf present || printf missing)" "$(has_virus_database && printf present || printf missing)" "$(systemctl is-active "${update_timer_name}" 2>/dev/null || true)" >&2
  systemctl show --no-pager "${service_name}.service" -p ActiveState -p SubState -p Result -p ExecMainStatus 2>/dev/null >&2 || true
  [[ ! -r "${log_dir}/clamd.log" ]] || tail -n 50 "${log_dir}/clamd.log" >&2 || true
  [[ ! -r "${log_dir}/freshclam.log" ]] || tail -n 50 "${log_dir}/freshclam.log" >&2 || true
}

freshclam_failure_detail() {
  printf 'freshclam_service=%s runtime=%s cvd_certificates=%s\n' \
    "$(systemctl is-active "${update_service_name}.service" 2>/dev/null || true)" "${clamav_runtime_version:-unknown}" "$(cvd_certificates_state)" >&2
  systemctl show --no-pager "${update_service_name}.service" -p ActiveState -p SubState -p Result -p ExecMainStatus 2>/dev/null >&2 || true
  if command -v journalctl >/dev/null 2>&1; then
    journalctl --no-pager --output=cat -u "${update_service_name}.service" -n 80 2>/dev/null |
      sed -E 's#(https?|ftp)://[^/@[:space:]]+:[^/@[:space:]]+@#\1://[REDACTED]@#g' >&2 || true
  fi
  [[ ! -r "${log_dir}/freshclam.log" ]] || tail -n 80 "${log_dir}/freshclam.log" >&2 || true
}

write_install_parameters() { printf 'DATA_DIR=%s\nINSTALL_MODE=%s\nDATABASE_GENERATED_AT_UNIX=%s\n' "${data_dir}" "${install_mode}" "${database_generated_at_unix}" >"${install_parameters_file}"; chmod 0640 "${install_parameters_file}"; }

write_runtime_profile() {
  cat >"${state_dir}/runtime-profile" <<EOF
runtime_user=${runtime_user}
runtime_group=${runtime_group}
clamd_binary=${clamd_binary}
clamdscan_binary=${clamdscan_binary}
freshclam_binary=${freshclam_binary}
clamav_runtime_version=${clamav_runtime_version}
cvd_certs_dir=${cvd_certs_dir}
EOF
  chmod 0640 "${state_dir}/runtime-profile"
}

commit_installation() {
  install -d -m 0750 -- "${state_dir}"
  [[ ! -d "${migration_dir}" ]] || { rm -rf -- "${external_dir}"; mv -- "${migration_dir}" "${external_dir}"; }
  printf '%s\n' "${package_version}" >"${state_dir}/version"
  printf '%s\n' "${software_version}" >"${state_dir}/software-version"
  printf '%s\n' "${system_id}" >"${state_dir}/system-id"
  printf '%s\n' "${system_version}" >"${state_dir}/system-version"
  printf '%s\n' "${host_arch}" >"${state_dir}/architecture"
  printf '%s\n' "${service_name}" >"${state_dir}/service"
  write_install_parameters
  write_runtime_profile
  : >"${state_dir}/installed"
}

remove_managed_units_and_config() {
  systemctl disable --now "${service_name}.service" "${update_timer_name}" 2>/dev/null || true
  rm -f -- "/etc/systemd/system/${service_name}.service" "/etc/systemd/system/${update_service_name}.service" "/etc/systemd/system/${update_timer_name}"
  systemctl daemon-reload 2>/dev/null || true
  remove_apparmor_overrides
  rm -rf -- "${config_dir}" "${log_dir}"
}

restore_external_state() {
  local snapshot="${external_dir}"
  [[ -d "${snapshot}" ]] || snapshot="${migration_dir}"
  [[ -d "${snapshot}" ]] || return 0
  if [[ -d "${snapshot}/config/clamav" ]]; then rm -rf -- /etc/clamav; cp -a -- "${snapshot}/config/clamav" /etc/clamav; fi
  if [[ -d "${snapshot}/config/clamd.d" ]]; then rm -rf -- /etc/clamd.d; cp -a -- "${snapshot}/config/clamd.d" /etc/clamd.d; fi
  if [[ -d "${snapshot}/data" ]]; then rm -rf -- "${data_dir}"; cp -a -- "${snapshot}/data" "${data_dir}"; fi
  restore_native_unit_enablement "${snapshot}/enabled-native-units"
  restore_native_units "${snapshot}/active-native-units"
}

rollback_installation() {
  remove_managed_units_and_config
  # A package manager may fail after installing part of a transaction. Rebuild
  # the closure from the pre-install inventory before removing anything.
  [[ ! -r "${package_inventory_before_file}" ]] || record_owned_packages || true
  remove_owned_packages || true
  rollback_repository_bootstrap || true
  report_preexisting_package_version_changes
  restore_external_state
  rm -f -- "${state_dir}/installed" "${state_dir}/version" "${state_dir}/software-version" "${package_ownership_file}" "${package_inventory_before_file}" "${package_inventory_after_file}" "${package_version_changes_file}" "${runtime_package_state_before_file}" "${repository_bootstrap_file}" "${state_dir}/runtime-package-lock" "${install_parameters_file}" "${settings_file}"
}

persisted_data_dir() {
  local value
  value="$(awk -F= '$1 == "DATA_DIR" {print substr($0, index($0, "=") + 1); exit}' "${install_parameters_file}" 2>/dev/null || true)"
  [[ -z "${value}" ]] || data_dir="${value}"
}

load_managed_state() {
  persisted_data_dir
  local persisted_mode
  persisted_mode="$(awk -F= '$1 == "INSTALL_MODE" {print substr($0, index($0, "=") + 1); exit}' "${install_parameters_file}" 2>/dev/null || true)"
  [[ -z "${persisted_mode}" ]] || install_mode="${persisted_mode}"
  database_generated_at_unix="$(awk -F= '$1 == "DATABASE_GENERATED_AT_UNIX" {print substr($0, index($0, "=") + 1); exit}' "${install_parameters_file}" 2>/dev/null || true)"
  runtime_user="$(awk -F= '$1 == "runtime_user" {print $2; exit}' "${state_dir}/runtime-profile" 2>/dev/null || true)"
  runtime_group="$(awk -F= '$1 == "runtime_group" {print $2; exit}' "${state_dir}/runtime-profile" 2>/dev/null || true)"
  clamd_binary="$(awk -F= '$1 == "clamd_binary" {print substr($0, index($0, "=") + 1); exit}' "${state_dir}/runtime-profile" 2>/dev/null || true)"
  clamdscan_binary="$(command -v clamdscan 2>/dev/null || true)"
  freshclam_binary="$(command -v freshclam 2>/dev/null || true)"
  sigtool_binary="$(command -v sigtool 2>/dev/null || true)"
  clamav_runtime_version="$(runtime_engine_version)"
  cvd_certs_dir="$(find_cvd_certs_dir || true)"
}
