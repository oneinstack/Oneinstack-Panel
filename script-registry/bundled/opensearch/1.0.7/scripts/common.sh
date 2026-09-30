#!/usr/bin/env bash
# shellcheck disable=SC2034
set -Eeuo pipefail
umask 027

component_id="opensearch"
package_version="1.0.7"
software_version="${SOFTWARE_VERSION:-3.7.0}"
admin_password="${OPENSEARCH_INITIAL_ADMIN_PASSWORD:-}"
opensearch_port="${OPENSEARCH_PORT:-9200}"
bind_address="${OPENSEARCH_BIND_ADDRESS:-127.0.0.1}"
cluster_name="${OPENSEARCH_CLUSTER_NAME:-oneinstack-opensearch}"
node_name="${OPENSEARCH_NODE_NAME:-oneinstack-opensearch-1}"
heap_size_mb="${OPENSEARCH_HEAP_SIZE_MB:-1024}"
memory_lock="${OPENSEARCH_MEMORY_LOCK:-false}"
install_dir="${INSTALL_DIR:-/opt/oneinstack/opensearch}"
data_dir="${DATA_DIR:-/var/lib/opensearch}"
log_dir="${LOG_DIR:-/var/log/opensearch}"
run_user="opensearch"
run_group="opensearch"
service_name="opensearch"
install_mode="${ONEINSTACK_INSTALL_MODE:-center}"
offline_package_path="${ONEINSTACK_OFFLINE_PACKAGE_PATH:-}"
state_root="${ONEINSTACK_COMPONENT_STATE:-/var/lib/oneinstack/components}"
state_dir="${state_root}/${component_id}"
rollback_dir="${state_dir}/rollback"
downloads_dir="${state_root}/downloads"
install_parameters_file="${state_dir}/install-parameters"
unit_file="/etc/systemd/system/${service_name}.service"
config_file="${install_dir}/config/opensearch.yml"
jvm_options_file="${install_dir}/config/jvm.options.d/oneinstack.options"
component_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
release_key_fingerprint="A8B2D9E04CD51FEF6AA2DB53BA81D99981191457"
release_key_sha256="6571e52a5da71e18bd1ca4cdef4eae9c6482b50e146e03a611b15e85bfbead29"
release_key_url="https://artifacts.opensearch.org/publickeys/opensearch-release.pgp"
release_archive=""
release_url=""
release_signature_url=""
release_sha512=""
host_architecture=""
release_architecture=""
os_id=""
os_version=""
package_manager=""

die() { printf 'ERROR: %s\n' "$*" >&2; exit 1; }
emit_progress() {
  local percent="$1" code="$2" message="$3" fd="${ONEINSTACK_PROGRESS_FD:-}"
  [[ "${fd}" =~ ^[0-9]+$ ]] || return 0
  message="${message//\\/\\\\}"; message="${message//\"/\\\"}"; message="${message//$'\n'/ }"
  printf '{"type":"progress","percent":%s,"code":"%s","message":"%s"}\n' \
    "${percent}" "${code}" "${message}" >&"${fd}" || true
}
require_root() { [[ "$(id -u)" -eq 0 ]] || die "This action must run as root."; }
require_command() { command -v "$1" >/dev/null 2>&1 || die "Required command not found: $1"; }
gpg_binary() { command -v gpg 2>/dev/null || command -v gpg2 2>/dev/null; }
validate_path() {
  local value="$1" label="$2"
  [[ "${value}" == /* && "$(realpath -m -- "${value}")" == "${value}" ]] || die "${label} must be a normalized absolute path."
  [[ "${value}" =~ ^/[A-Za-z0-9._/-]+$ ]] || die "${label} contains unsupported characters."
  case "${value}" in /|/usr|/usr/local|/etc|/opt|/var|/data|/home|/root) die "${label} is too broad: ${value}" ;; esac
}
validate_name() {
  local value="$1" label="$2"
  [[ "${value}" =~ ^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$ ]] || die "${label} contains unsupported characters."
}
validate_bind_address() {
  local value="$1"
  [[ -n "${value}" && ${#value} -le 253 && ! "${value}" =~ [[:space:]#\{\}\[\],\"\'] ]] || die "OPENSEARCH_BIND_ADDRESS is invalid."
  [[ "${value}" =~ ^[A-Za-z0-9:.%-]+$ ]] || die "OPENSEARCH_BIND_ADDRESS is invalid."
}
validate_password() {
  [[ -n "${admin_password}" ]] || die "OPENSEARCH_INITIAL_ADMIN_PASSWORD is required."
  [[ "${admin_password}" =~ ^[A-Za-z0-9_@%+=:,.!#?-]{12,128}$ ]] ||
    die "OPENSEARCH_INITIAL_ADMIN_PASSWORD must be 12-128 characters and use only letters, digits, or _ @ % + = : , . ! # ? -."
  [[ "${admin_password}" =~ [A-Z] && "${admin_password}" =~ [a-z] && "${admin_password}" =~ [0-9] && "${admin_password}" =~ [_@%+=:,.!#?-] ]] ||
    die "OPENSEARCH_INITIAL_ADMIN_PASSWORD must contain uppercase, lowercase, numeric, and symbol characters."
}
select_release() {
  [[ "${software_version}" == "3.7.0" ]] || die "Unsupported OpenSearch version: ${software_version}; select 3.7.0."
  case "$(uname -m)" in
    x86_64|amd64)
      host_architecture="amd64"; release_architecture="x64"
      release_sha512="9e481db6495cbdacd6704a19c6ed7679e1138d40dbe283642c599bf70441c968bbd7f6faf7ecfa6c9ecf0685141037079b53647c5df3595bd5b9647bcbad1a04"
      ;;
    aarch64|arm64)
      host_architecture="arm64"; release_architecture="arm64"
      release_sha512="fbc2dcb98ab909c113f70473861f9a7e62d9b3c59c6c06201617a3e3d1ce7ec9707ecba217ead62ea553f2486bb91b87f2d34a99d498dc5c5d4f0659fe01db50"
      ;;
    *) die "Unsupported OpenSearch architecture: $(uname -m)" ;;
  esac
  release_archive="opensearch-${software_version}-linux-${release_architecture}.tar.gz"
  release_url="https://artifacts.opensearch.org/releases/bundle/opensearch/${software_version}/${release_archive}"
  release_signature_url="${release_url}.sig"
}
detect_host() {
  [[ -r /etc/os-release ]] || die "Unsupported host: /etc/os-release is unavailable."
  local detected_id detected_version detected_name
  # shellcheck disable=SC1091
  detected_id="$(. /etc/os-release; printf '%s' "${ID:-}")"
  # shellcheck disable=SC1091
  detected_version="$(. /etc/os-release; printf '%s' "${VERSION_ID:-}")"
  # shellcheck disable=SC1091
  detected_name="$(. /etc/os-release; printf '%s' "${NAME:-}")"
  os_id="${detected_id,,}"; os_version="${detected_version%%.*}"
  case "${detected_id,,}" in
    ubuntu) os_id=ubuntu; os_version="${detected_version}"; package_manager=apt ;;
    debian) os_id=debian; package_manager=apt ;;
    rhel|rocky|almalinux|ol) os_id="${detected_id,,}"; package_manager=dnf ;;
    centos)
      [[ "${detected_name,,}" == *stream* ]] || die "Unsupported host: ordinary CentOS is not in the OpenSearch matrix."
      os_id=centos-stream; package_manager=dnf
      ;;
    amzn) [[ "${detected_version}" == 2023* ]] || die "Unsupported Amazon Linux version: ${detected_version}."; os_id=amzn; os_version=2023; package_manager=dnf ;;
    sles) os_id=sles; package_manager=zypper ;;
    *) die "Unsupported OpenSearch host: ${detected_id} ${detected_version}." ;;
  esac
  case "${os_id}:${os_version}" in
    ubuntu:22.04|ubuntu:24.04|ubuntu:26.04|debian:11|debian:12|debian:13|\
    rhel:8|rhel:9|rhel:10|rocky:8|rocky:9|rocky:10|almalinux:8|almalinux:9|almalinux:10|\
    ol:8|ol:9|ol:10|centos-stream:8|centos-stream:9|centos-stream:10|amzn:2023|sles:15|sles:16) ;;
    *) die "Unsupported OpenSearch host matrix entry: ${os_id} ${detected_version}." ;;
  esac
  command -v "${package_manager}" >/dev/null 2>&1 || die "Required package manager is unavailable: ${package_manager}."
}
validate_install_mode() {
  [[ "${install_mode}" == center || "${install_mode}" == offline ]] || die "ONEINSTACK_INSTALL_MODE must be center or offline."
  if [[ "${install_mode}" == offline ]]; then
    [[ -n "${offline_package_path}" && "${offline_package_path}" == /* && "$(realpath -m -- "${offline_package_path}")" == "${offline_package_path}" ]] ||
      die "Offline installation requires a normalized absolute Bundle path."
  elif [[ -n "${offline_package_path}" ]]; then
    die "ONEINSTACK_OFFLINE_PACKAGE_PATH is only valid in offline mode."
  fi
}
validate_inputs() {
  select_release; detect_host; validate_install_mode
  [[ "${opensearch_port}" =~ ^[0-9]+$ && "${opensearch_port}" -ge 1 && "${opensearch_port}" -le 65535 ]] || die "OPENSEARCH_PORT must be a valid TCP port."
  [[ "${heap_size_mb}" =~ ^[0-9]+$ && "${heap_size_mb}" -ge 512 && "${heap_size_mb}" -le 1048576 ]] || die "OPENSEARCH_HEAP_SIZE_MB must be 512-1048576."
  [[ "${memory_lock}" == true || "${memory_lock}" == false ]] || die "OPENSEARCH_MEMORY_LOCK must be true or false."
  validate_bind_address "${bind_address}"; validate_name "${cluster_name}" OPENSEARCH_CLUSTER_NAME; validate_name "${node_name}" OPENSEARCH_NODE_NAME
  validate_path "${install_dir}" INSTALL_DIR; validate_path "${data_dir}" DATA_DIR; validate_path "${log_dir}" LOG_DIR; validate_path "${state_root}" ONEINSTACK_COMPONENT_STATE
  [[ "${install_dir}" != "${data_dir}" && "${install_dir}" != "${log_dir}" && "${data_dir}" != "${log_dir}" ]] || die "INSTALL_DIR, DATA_DIR, and LOG_DIR must be different."
}
validate_kernel_settings() {
  require_command sysctl
  local value; value="$(sysctl -n vm.max_map_count 2>/dev/null || true)"
  [[ "${value}" =~ ^[0-9]+$ && "${value}" -ge 262144 ]] || die "vm.max_map_count must be at least 262144 before installing OpenSearch."
}
port_is_listening() {
  require_command ss
  ss -H -ltn "sport = :$1" 2>/dev/null | grep -q .
}
persist_install_parameters() {
  install -d -m 0750 -- "${state_dir}"
  local temporary; temporary="$(mktemp "${state_dir}/.install-parameters.XXXXXX")"
  {
    printf 'OPENSEARCH_PORT=%s\n' "${opensearch_port}"
    printf 'OPENSEARCH_BIND_ADDRESS=%s\n' "${bind_address}"
    printf 'OPENSEARCH_CLUSTER_NAME=%s\n' "${cluster_name}"
    printf 'OPENSEARCH_NODE_NAME=%s\n' "${node_name}"
    printf 'OPENSEARCH_HEAP_SIZE_MB=%s\n' "${heap_size_mb}"
    printf 'OPENSEARCH_MEMORY_LOCK=%s\n' "${memory_lock}"
    printf 'INSTALL_DIR=%s\n' "${install_dir}"
    printf 'DATA_DIR=%s\n' "${data_dir}"
    printf 'LOG_DIR=%s\n' "${log_dir}"
  } >"${temporary}"
  chmod 0600 "${temporary}"; mv -f -- "${temporary}" "${install_parameters_file}"
}
load_install_parameters() {
  [[ -r "${install_parameters_file}" ]] || return 0
  local requested_port="${opensearch_port}" requested_bind="${bind_address}" requested_cluster="${cluster_name}" requested_node="${node_name}"
  local requested_heap="${heap_size_mb}" requested_memory_lock="${memory_lock}" requested_install="${install_dir}" requested_data="${data_dir}" requested_log="${log_dir}"
  local key value marker
  while IFS='=' read -r key value; do
    case "${key}" in
      OPENSEARCH_PORT) opensearch_port="${value}" ;; OPENSEARCH_BIND_ADDRESS) bind_address="${value}" ;;
      OPENSEARCH_CLUSTER_NAME) cluster_name="${value}" ;; OPENSEARCH_NODE_NAME) node_name="${value}" ;;
      OPENSEARCH_HEAP_SIZE_MB) heap_size_mb="${value}" ;; OPENSEARCH_MEMORY_LOCK) memory_lock="${value}" ;;
      INSTALL_DIR) install_dir="${value}" ;; DATA_DIR) data_dir="${value}" ;; LOG_DIR) log_dir="${value}" ;;
    esac
  done <"${install_parameters_file}"
  for key in OPENSEARCH_PORT OPENSEARCH_BIND_ADDRESS OPENSEARCH_CLUSTER_NAME OPENSEARCH_NODE_NAME OPENSEARCH_HEAP_SIZE_MB INSTALL_DIR DATA_DIR LOG_DIR; do
    marker="ONEINSTACK_PARAMETER_${key}_EXPLICIT"; [[ "${!marker:-false}" == true ]] || continue
    case "${key}" in
      OPENSEARCH_PORT) opensearch_port="${requested_port}" ;; OPENSEARCH_BIND_ADDRESS) bind_address="${requested_bind}" ;;
      OPENSEARCH_CLUSTER_NAME) cluster_name="${requested_cluster}" ;; OPENSEARCH_NODE_NAME) node_name="${requested_node}" ;;
      OPENSEARCH_HEAP_SIZE_MB) heap_size_mb="${requested_heap}" ;; INSTALL_DIR) install_dir="${requested_install}" ;;
      DATA_DIR) data_dir="${requested_data}" ;; LOG_DIR) log_dir="${requested_log}" ;;
    esac
  done
  memory_lock="${requested_memory_lock}"
  config_file="${install_dir}/config/opensearch.yml"; jvm_options_file="${install_dir}/config/jvm.options.d/oneinstack.options"
}
offline_package_dir() { printf '%s/packages/%s/%s/%s' "${offline_package_path}" "${os_id}" "${os_version}" "${host_architecture}"; }
validate_offline_bundle() {
  [[ "${install_mode}" == offline ]] || return 0
  local package_dir required_script
  package_dir="$(offline_package_dir)"
  [[ -f "${offline_package_path}/manifest.yaml" && -f "${offline_package_path}/bundle-info" && -f "${offline_package_path}/files.sha256" ]] || die "Offline OpenSearch Bundle metadata is incomplete."
  grep -Fxq 'component=opensearch' "${offline_package_path}/bundle-info" || die "Offline Bundle component metadata is invalid."
  grep -Fxq "packageVersion=${package_version}" "${offline_package_path}/bundle-info" || die "Offline Bundle package version is not ${package_version}."
  grep -Fxq "softwareVersion=${software_version}" "${offline_package_path}/bundle-info" || die "Offline Bundle software version is not ${software_version}."
  grep -Fxq "osId=${os_id}" "${offline_package_path}/bundle-info" || die "Offline Bundle OS does not match this host."
  grep -Fxq "osVersion=${os_version}" "${offline_package_path}/bundle-info" || die "Offline Bundle OS version does not match this host."
  grep -Fxq "architecture=${host_architecture}" "${offline_package_path}/bundle-info" || die "Offline Bundle architecture does not match this host."
  [[ -f "${offline_package_path}/artifacts/${host_architecture}/${release_archive}" && -f "${offline_package_path}/artifacts/${host_architecture}/${release_archive}.sig" ]] || die "Offline OpenSearch artifact or signature is missing."
  [[ -f "${offline_package_path}/keys/opensearch-release.pgp" && -d "${package_dir}" ]] || die "Offline OpenSearch key or dependency directory is missing."
  for required_script in precheck.sh install.sh configure.sh verify.sh status.sh start.sh stop.sh restart.sh rollback.sh uninstall.sh config.sh; do
    [[ -x "${offline_package_path}/scripts/${required_script}" ]] || die "Offline lifecycle script is missing: ${required_script}."
  done
  find "${package_dir}" -maxdepth 1 -type f \( -name '*.deb' -o -name '*.rpm' \) -print -quit | grep -q . || die "Offline dependency package closure is empty."
  (cd -- "${offline_package_path}" && sha256sum --check --strict files.sha256 >/dev/null) || die "Offline Bundle checksum verification failed."
  grep -Eq '^[[:space:]]+id:[[:space:]]+opensearch[[:space:]]*$' "${offline_package_path}/manifest.yaml" || die "Offline Bundle manifest component is invalid."
  grep -Eq '^[[:space:]]+version:[[:space:]]+1\.0\.7[[:space:]]*$' "${offline_package_path}/manifest.yaml" || die "Offline Bundle manifest package version is invalid."
}
install_dependencies_online() {
  case "${package_manager}" in
    apt) DEBIAN_FRONTEND=noninteractive apt-get update; DEBIAN_FRONTEND=noninteractive apt-get install -y ca-certificates curl gnupg iproute2 procps tar gzip ;;
    dnf) dnf install -y ca-certificates curl gnupg2 iproute procps-ng tar gzip ;;
    zypper) zypper --non-interactive refresh; zypper --non-interactive install ca-certificates curl gpg2 iproute2 procps tar gzip ;;
  esac
}
install_dependencies_offline() {
  local package_dir; package_dir="$(offline_package_dir)"
  local -a packages=(); mapfile -t packages < <(find "${package_dir}" -maxdepth 1 -type f \( -name '*.deb' -o -name '*.rpm' \) -print | sort)
  ((${#packages[@]} > 0)) || die "Offline dependency package closure is empty."
  case "${package_manager}" in
    apt) DEBIAN_FRONTEND=noninteractive dpkg -i "${packages[@]}" || DEBIAN_FRONTEND=noninteractive apt-get --no-download -f install -y ;;
    dnf) dnf --disablerepo='*' install -y "${packages[@]}" ;;
    zypper) rpm -Uvh --replacepkgs "${packages[@]}" ;;
  esac
}
install_dependencies() { if [[ "${install_mode}" == offline ]]; then install_dependencies_offline; else install_dependencies_online; fi; }
verify_sha512() { printf '%s  %s\n' "${release_sha512}" "$1" | sha512sum --check --status; }
verify_release_signature() {
  local archive="$1" signature="$2" key="$3" gpg_home fingerprint gpg_command
  gpg_command="$(gpg_binary)" || die "Required command not found: gpg or gpg2"
  printf '%s  %s\n' "${release_key_sha256}" "${key}" | sha256sum --check --status || die "OpenSearch release key checksum verification failed."
  gpg_home="$(mktemp -d)"; chmod 0700 "${gpg_home}"
  GNUPGHOME="${gpg_home}" "${gpg_command}" --batch --quiet --import "${key}" >/dev/null 2>&1 || { rm -rf -- "${gpg_home}"; die "OpenSearch release key import failed."; }
  fingerprint="$(GNUPGHOME="${gpg_home}" "${gpg_command}" --batch --with-colons --fingerprint 2>/dev/null | awk -F: '$1 == "fpr" { print toupper($10); exit }')"
  [[ "${fingerprint}" == "${release_key_fingerprint}" ]] || { rm -rf -- "${gpg_home}"; die "OpenSearch release key fingerprint mismatch."; }
  GNUPGHOME="${gpg_home}" "${gpg_command}" --batch --verify "${signature}" "${archive}" >/dev/null 2>&1 || { rm -rf -- "${gpg_home}"; die "OpenSearch artifact signature verification failed."; }
  rm -rf -- "${gpg_home}"
}
resolve_release() {
  local archive signature key
  if [[ "${install_mode}" == offline ]]; then
    archive="${offline_package_path}/artifacts/${host_architecture}/${release_archive}"
    signature="${archive}.sig"; key="${offline_package_path}/keys/opensearch-release.pgp"
  else
    install -d -m 0750 -- "${downloads_dir}"
    archive="${downloads_dir}/${release_archive}"; signature="${archive}.sig"; key="${downloads_dir}/opensearch-release.pgp"
    if [[ ! -f "${archive}" ]] || ! verify_sha512 "${archive}"; then
      rm -f -- "${archive}" "${archive}.part"
      curl --proto '=https' --tlsv1.2 --fail --location --retry 3 --connect-timeout 20 --output "${archive}.part" "${release_url}" || { rm -f -- "${archive}.part"; die "Failed to download ${release_url}."; }
      verify_sha512 "${archive}.part" || { rm -f -- "${archive}.part"; die "OpenSearch SHA-512 verification failed."; }
      mv -f -- "${archive}.part" "${archive}"
    fi
    curl --proto '=https' --tlsv1.2 --fail --location --retry 3 --connect-timeout 20 --output "${signature}.part" "${release_signature_url}" || { rm -f -- "${signature}.part"; die "Failed to download OpenSearch signature."; }
    mv -f -- "${signature}.part" "${signature}"
    curl --proto '=https' --tlsv1.2 --fail --location --retry 3 --connect-timeout 20 --output "${key}.part" "${release_key_url}" || { rm -f -- "${key}.part"; die "Failed to download OpenSearch release key."; }
    mv -f -- "${key}.part" "${key}"
  fi
  verify_sha512 "${archive}" || die "OpenSearch SHA-512 verification failed."
  verify_release_signature "${archive}" "${signature}" "${key}"
  printf '%s\n' "${archive}"
}
ensure_account() {
  getent group "${run_group}" >/dev/null || groupadd --system "${run_group}"
  id "${run_user}" >/dev/null 2>&1 || useradd --system --gid "${run_group}" --home-dir "${data_dir}" --shell /usr/sbin/nologin "${run_user}"
}
service_is_active() { systemctl is-active --quiet "${service_name}.service" 2>/dev/null; }
service_start() { systemctl enable --now "${service_name}.service"; }
service_stop() {
  systemctl stop "${service_name}.service" 2>/dev/null || true
  systemctl reset-failed "${service_name}.service" 2>/dev/null || true
}
service_restart() { systemctl restart "${service_name}.service"; }
wait_for_service() { local n; for n in $(seq 1 90); do service_is_active && return 0; sleep 2; done; return 1; }
actual_version() {
  [[ -x "${install_dir}/bin/opensearch" ]] || return 0
  "${install_dir}/bin/opensearch" --version 2>&1 | grep -Eo '[0-9]+\.[0-9]+\.[0-9]+' | head -n1 || true
}
write_unit() {
  install -d -m 0755 -- /etc/systemd/system
  local candidate; candidate="$(mktemp /etc/systemd/system/.opensearch.service.XXXXXX)"
  cat >"${candidate}" <<EOF
[Unit]
Description=OpenSearch managed by Oneinstack
Wants=network-online.target
After=network-online.target

[Service]
Type=simple
User=${run_user}
Group=${run_group}
WorkingDirectory=${install_dir}
Environment=OPENSEARCH_HOME=${install_dir}
Environment=OPENSEARCH_PATH_CONF=${install_dir}/config
Environment=OPENSEARCH_JAVA_HOME=${install_dir}/jdk
ExecStart=${install_dir}/bin/opensearch
SuccessExitStatus=143 SIGTERM
Restart=on-failure
RestartSec=5
LimitNOFILE=65535
LimitNPROC=4096
LimitMEMLOCK=infinity
TimeoutStartSec=300
TimeoutStopSec=180

[Install]
WantedBy=multi-user.target
EOF
  chmod 0644 "${candidate}"; mv -f -- "${candidate}" "${unit_file}"; systemctl daemon-reload
}
write_managed_config() {
  local target="$1" base="${2:-${config_file}}"
  [[ -f "${base}" ]] || die "OpenSearch base configuration is missing: ${base}."
  awk '
    BEGIN { managed=0 }
    /^# BEGIN ONEINSTACK MANAGED$/ { managed=1; next }
    /^# END ONEINSTACK MANAGED$/ { managed=0; next }
    managed == 0 && $0 !~ /^[[:space:]]*(cluster\.name|node\.name|path\.data|path\.logs|network\.host|http\.port|discovery\.type|bootstrap\.memory_lock):/ { print }
  ' "${base}" >"${target}"
  cat >>"${target}" <<EOF

# BEGIN ONEINSTACK MANAGED
cluster.name: ${cluster_name}
node.name: ${node_name}
path.data: ${data_dir}
path.logs: ${log_dir}
network.host: ${bind_address}
http.port: ${opensearch_port}
discovery.type: single-node
bootstrap.memory_lock: ${memory_lock}
# END ONEINSTACK MANAGED
EOF
  chmod 0640 "${target}"
}
write_jvm_options() {
  local target="$1"
  install -d -m 0750 -- "$(dirname -- "${target}")"
  printf '%s\n%s\n' "-Xms${heap_size_mb}m" "-Xmx${heap_size_mb}m" >"${target}"
  chmod 0640 "${target}"
}
configure_demo_security() {
  validate_password
  local installer="${install_dir}/plugins/opensearch-security/tools/install_demo_configuration.sh"
  [[ -f "${installer}" ]] || die "OpenSearch Security configuration helper is missing."
  env OPENSEARCH_INITIAL_ADMIN_PASSWORD="${admin_password}" bash "${installer}" -y >/dev/null
}
initialize_security_index() {
  local tool="${install_dir}/plugins/opensearch-security/tools/securityadmin.sh"
  [[ -f "${tool}" && -x "${install_dir}/jdk/bin/java" ]] || die "OpenSearch Security administration tools are missing."
  env OPENSEARCH_JAVA_HOME="${install_dir}/jdk" bash "${tool}" -cd "${install_dir}/config/opensearch-security" -icl \
    -key "${install_dir}/config/kirk-key.pem" -cert "${install_dir}/config/kirk.pem" \
    -cacert "${install_dir}/config/root-ca.pem" -nhnv -arc >/dev/null
}
curl_with_admin_password() {
  local url="$1" credential response
  credential="$(mktemp "${state_dir}/.curl-credentials.XXXXXX")"; chmod 0600 "${credential}"
  printf 'user = "admin:%s"\n' "${admin_password}" >"${credential}"
  response="$(curl --config "${credential}" --noproxy '*' --fail --silent --show-error --insecure --connect-timeout 3 --max-time 40 "${url}")" || { rm -f -- "${credential}"; return 1; }
  rm -f -- "${credential}"; printf '%s' "${response}"
}
probe_url_host() {
  case "${bind_address}" in
    0.0.0.0) printf '127.0.0.1' ;;
    ::) printf '[::1]' ;;
    *:*) printf '[%s]' "${bind_address}" ;;
    *) printf '%s' "${bind_address}" ;;
  esac
}
https_ready() {
  local code
  code="$(curl --noproxy '*' --silent --insecure --output /dev/null --write-out '%{http_code}' --connect-timeout 2 --max-time 5 "https://$(probe_url_host):${opensearch_port}/" || true)"
  [[ "${code}" == 200 || "${code}" == 401 || "${code}" == 403 ]]
}
https_listener_reachable() {
  local code
  code="$(curl --noproxy '*' --silent --insecure --output /dev/null --write-out '%{http_code}' --connect-timeout 2 --max-time 5 "https://$(probe_url_host):${opensearch_port}/" || true)"
  [[ "${code}" =~ ^[1-5][0-9][0-9]$ ]]
}
wait_for_https_listener() {
  local n
  for ((n=1; n<=90; n++)); do
    service_is_active || return 1
    https_listener_reachable && return 0
    sleep 2
  done
  return 1
}
wait_for_https() {
  local n
  for ((n=1; n<=90; n++)); do
    service_is_active || return 1
    https_ready && return 0
    sleep 2
  done
  return 1
}
emit_startup_diagnostics() {
  printf '%s\n' '--- OpenSearch systemd status ---' >&2
  systemctl status "${service_name}.service" --no-pager --full >&2 || true
  printf '%s\n' '--- OpenSearch journal (last 80 lines) ---' >&2
  journalctl -u "${service_name}.service" --no-pager -n 80 >&2 || true
  if [[ -f "${log_dir}/${cluster_name}.log" ]]; then
    printf '%s\n' "--- OpenSearch log (last 120 lines): ${log_dir}/${cluster_name}.log ---" >&2
    tail -n 120 -- "${log_dir}/${cluster_name}.log" >&2 || true
  fi
}
verify_authenticated_health() {
  validate_password
  local response; response="$(curl_with_admin_password "https://$(probe_url_host):${opensearch_port}/_cluster/health?wait_for_status=yellow&timeout=30s")" || return 1
  [[ "${response}" =~ \"status\"[[:space:]]*:[[:space:]]*\"(yellow|green)\" ]]
}
verify_certificate_health() {
  local response
  response="$(curl --noproxy '*' --fail --silent --show-error --connect-timeout 3 --max-time 40 \
    --cacert "${install_dir}/config/root-ca.pem" \
    --cert "${install_dir}/config/kirk.pem" \
    --key "${install_dir}/config/kirk-key.pem" --insecure \
    "https://$(probe_url_host):${opensearch_port}/_cluster/health?wait_for_status=yellow&timeout=30s")" || return 1
  [[ "${response}" =~ \"status\"[[:space:]]*:[[:space:]]*\"(yellow|green)\" ]]
}
emit_allocation_diagnostics() {
  [[ -n "${admin_password}" ]] || return 0
  curl_with_admin_password "https://$(probe_url_host):${opensearch_port}/_cluster/allocation/explain?pretty" >&2 || true
}
managed_installation_present() { [[ -f "${state_dir}/installed" && -f "${state_dir}/ownership" ]] && grep -Fxq managed "${state_dir}/ownership"; }
snapshot_runtime() {
  rm -rf -- "${rollback_dir}"; install -d -m 0700 -- "${rollback_dir}"
  : >"${rollback_dir}/snapshot-active"
  if service_is_active; then : >"${rollback_dir}/was-active"; service_stop; fi
  [[ ! -f "${unit_file}" ]] || cp -a -- "${unit_file}" "${rollback_dir}/unit"
  [[ ! -d "${install_dir}" ]] || mv -- "${install_dir}" "${rollback_dir}/old-install"
}
restore_snapshot() {
  [[ -f "${rollback_dir}/snapshot-active" ]] || return 0
  service_stop
  [[ ! -d "${install_dir}" ]] || rm -rf -- "${install_dir}"
  [[ ! -d "${rollback_dir}/old-install" ]] || mv -- "${rollback_dir}/old-install" "${install_dir}"
  if [[ -f "${rollback_dir}/unit" ]]; then cp -a -- "${rollback_dir}/unit" "${unit_file}"; else rm -f -- "${unit_file}"; fi
  systemctl daemon-reload 2>/dev/null || true
  [[ ! -f "${rollback_dir}/was-active" ]] || systemctl start "${service_name}.service" 2>/dev/null || true
  rm -rf -- "${rollback_dir}"
}
