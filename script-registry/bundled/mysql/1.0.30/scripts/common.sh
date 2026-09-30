#!/usr/bin/env bash
set -Eeuo pipefail
umask 027

component_id="mysql"
software_version="${SOFTWARE_VERSION:-8.0.45}"
patch_version=""
install_dir="${INSTALL_DIR:-/usr/local/mysql}"
data_dir="${DATA_DIR:-/data/mysql}"
log_dir="${LOG_DIR:-/data/mysql}"
mysql_port="${MYSQL_PORT:-3306}"
bind_address="${MYSQL_BIND_ADDRESS:-127.0.0.1}"
mysql_password="${MYSQL_PASSWORD:-}"
mysql_username="${MYSQL_USERNAME:-root}"
run_user="${RUN_USER:-mysql}"
run_group="${RUN_GROUP:-mysql}"
migrate_external="${MIGRATE_EXTERNAL_MYSQL:-false}"
migrate_external_confirm="${MIGRATE_EXTERNAL_CONFIRM:-false}"
reset_existing_root_password="${RESET_EXISTING_ROOT_PASSWORD:-true}"
reset_existing_root_password_confirm="${RESET_EXISTING_ROOT_PASSWORD_CONFIRM:-true}"
state_root="${COMPONENT_STATE_DIR:-${ONEINSTACK_COMPONENT_STATE:-/var/lib/oneinstack/components}}"
state_dir="${state_root}/${component_id}"
rollback_dir="${state_dir}/rollback"
unit_file="/etc/systemd/system/mysql.service"
config_file="/etc/my.cnf"
external_migration_dir="${rollback_dir}/external"
install_parameters_file="${state_dir}/install-parameters"
managed_path_acl_file="${state_dir}/managed-path-acl"
transaction_path_acl_file="${rollback_dir}/path-acl-added"
source_url=""
source_signature_url=""
source_sha256=""
source_fingerprint=""
source_archive=""
host_arch=""
system_id=""
system_version=""
package_manager=""
install_mode="${ONEINSTACK_INSTALL_MODE:-center}"
offline_package_path="${ONEINSTACK_OFFLINE_PACKAGE_PATH:-}"
release_key_url="https://keyserver.ubuntu.com/pks/lookup?op=get&search=0xBCA43417C3B485DD128EC6D4B7B3B788A8D3785C&exact=on"

die() { echo "ERROR: $*" >&2; exit 1; }
require_command() { command -v "$1" >/dev/null 2>&1 || die "Required command not found: $1"; }
emit_progress() {
  local percent="$1" code="$2" message="$3" fd="${ONEINSTACK_PROGRESS_FD:-}"
  [[ "${fd}" =~ ^[0-9]+$ ]] || return 0
  message="${message//\\/\\\\}"; message="${message//\"/\\\"}"; message="${message//$'\n'/ }"
  # The Panel runner reserves the progress descriptor as a write-only FD.
  # shellcheck disable=SC2261
  printf '{"type":"progress","percent":%s,"code":"%s","message":"%s"}\n' \
    "${percent}" "${code}" "${message}" >&"${fd}" 2>/dev/null || true
}
require_root() { [[ "$(id -u)" -eq 0 ]] || die "This action must run as root."; }
validate_path() {
  local value="$1" label="$2"
  [[ "${value}" == /* && "$(realpath -m -- "${value}")" == "${value}" ]] || die "${label} must be a normalized absolute path."
  case "${value}" in /|/usr|/usr/local|/etc|/var|/data|/home|/root) die "${label} is too broad: ${value}" ;; esac
}
architecture_name() {
  case "$(uname -m)" in
    x86_64) printf 'amd64' ;;
    aarch64|arm64) printf 'arm64' ;;
    *) die "Unsupported CPU architecture: $(uname -m)" ;;
  esac
}
glibc_is_older_than_228() {
  local version major minor
  require_command ldd
  version="$(LC_ALL=C ldd --version 2>&1 | awk 'NR == 1 { for (i = 1; i <= NF; i++) if ($i ~ /^[0-9]+\.[0-9]+$/) { print $i; exit } }')"
  [[ "${version}" =~ ^[0-9]+\.[0-9]+$ ]] || die "Cannot determine the host glibc version."
  IFS='.' read -r major minor <<<"${version}"
  (( major < 2 || (major == 2 && minor < 28) ))
}
select_source() {
  local architecture
  architecture="$(architecture_name)"
  # The artifact name is derived only after the component has validated the
  # requested 8.0.x version. Every download is verified by a pinned SHA-256
  # when available, otherwise by MySQL's detached signature and pinned key.
  case "${architecture}" in
    amd64)
      if glibc_is_older_than_228; then
        # CentOS 7 ships glibc 2.17 and cannot load the glibc2.28 build.
        source_url="https://cdn.mysql.com/archives/mysql-8.0/mysql-${software_version}-linux-glibc2.17-x86_64.tar.xz"
      else
        source_url="https://cdn.mysql.com/archives/mysql-8.0/mysql-${software_version}-linux-glibc2.28-x86_64.tar.xz"
      fi
      ;;
    arm64)
      if glibc_is_older_than_228; then
        die "MySQL ${software_version} does not provide a verified ARM64 binary for glibc older than 2.28."
      fi
      source_url="https://cdn.mysql.com/archives/mysql-8.0/mysql-${software_version}-linux-glibc2.28-aarch64.tar.xz"
      ;;
  esac
  source_archive="${source_url##*/}"
  source_signature_url="${source_url}.asc"
  source_fingerprint="BCA43417C3B485DD128EC6D4B7B3B788A8D3785C"
  source_sha256=""
  if [[ "${software_version}" == "8.0.45" ]]; then
    # The existing 8.0.45 artifacts are kept on their known SHA-256 path;
    # MySQL does not publish a detached signature at the corresponding URL.
    source_signature_url=""
    source_fingerprint=""
    case "${architecture}" in
      amd64)
        if [[ "${source_url}" == *glibc2.17* ]]; then
          source_sha256="3a9a7163ce42dbf4277281cdf4fbb52fb486246f6e29001e666e47a437c8b91e"
        else
          source_sha256="c09137539ab42590c8682d498716e6b97a45d83d2188339fa2e980dd54b6e0af"
        fi
        ;;
      arm64) source_sha256="2bbbdd107bc02bd6a135d5140b6a790de5ed4d4ff5389803600c26c79e208d2a" ;;
    esac
  fi
}
load_persisted_parameters() {
  local persisted="${state_dir}/install-parameters" key value marker
  [[ -r "${persisted}" ]] || return 0
  while IFS='=' read -r key value; do
    marker="ONEINSTACK_PARAMETER_${key}_EXPLICIT"
    case "${key}" in
      MYSQL_PORT) [[ "${!marker:-false}" == true ]] || mysql_port="${value}" ;;
      MYSQL_BIND_ADDRESS) [[ "${!marker:-false}" == true ]] || bind_address="${value}" ;;
      MYSQL_USERNAME) [[ "${!marker:-false}" == true ]] || mysql_username="${value}" ;;
      INSTALL_DIR) [[ "${!marker:-false}" == true ]] || install_dir="${value}" ;;
      DATA_DIR) [[ "${!marker:-false}" == true ]] || data_dir="${value}" ;;
      LOG_DIR) [[ "${!marker:-false}" == true ]] || log_dir="${value}" ;;
      RUN_USER) [[ "${!marker:-false}" == true ]] || run_user="${value}" ;;
      RUN_GROUP) [[ "${!marker:-false}" == true ]] || run_group="${value}" ;;
    esac
  done <"${persisted}"
}
reject_immutable_runtime_changes() {
  local persisted="${state_dir}/install-parameters" key value marker current
  [[ -f "${state_dir}/version" && -r "${persisted}" ]] || return 0
  while IFS='=' read -r key value; do
    case "${key}" in
      INSTALL_DIR) current="${install_dir}" ;;
      DATA_DIR) current="${data_dir}" ;;
      LOG_DIR) current="${log_dir}" ;;
      RUN_USER) current="${run_user}" ;;
      RUN_GROUP) current="${run_group}" ;;
      *) continue ;;
    esac
    marker="ONEINSTACK_PARAMETER_${key}_EXPLICIT"
    if [[ "${!marker:-false}" == true && "${current}" != "${value}" ]]; then
      die "${key} cannot change during an upgrade; reinstall the component to change its runtime identity."
    fi
  done <"${persisted}"
}
load_persisted_parameters
has_systemd() { command -v systemctl >/dev/null 2>&1 && [[ -d /run/systemd/system ]]; }
service_is_active() {
  if has_systemd; then systemctl is-active --quiet mysql.service 2>/dev/null; return; fi
  [[ -r "${data_dir}/mysqld.pid" ]] && kill -0 "$(cat "${data_dir}/mysqld.pid")" 2>/dev/null
}
service_start() {
  if has_systemd; then
    systemctl daemon-reload
    systemctl enable --now mysql.service
  else
    install -d -m 0755 -o "${run_user}" -g "${run_group}" /run/mysqld
    runuser -u "${run_user}" -- "${install_dir}/bin/mysqld" --defaults-file="${config_file}" >/dev/null 2>&1 &
    printf '%s\n' "$!" >"${data_dir}/mysqld.pid"
  fi
}
service_stop() {
  if has_systemd; then
    systemctl stop mysql.service 2>/dev/null || true
  elif [[ -r "${data_dir}/mysqld.pid" ]]; then
    kill "$(cat "${data_dir}/mysqld.pid")" 2>/dev/null || true
  fi
}
service_restart() { service_stop; service_start; }
service_status_value() {
  local property="$1"
  if has_systemd; then
    local unit value
    for unit in mysql.service mysqld.service; do
      value="$(systemctl show "${unit}" --property="${property}" --value 2>/dev/null || true)"
      if [[ -n "${value}" && "${value}" != "not-found" ]]; then
        printf '%s' "${value}"
        return 0
      fi
    done
    return 0
  elif [[ "${property}" == "ActiveState" ]] && service_is_active; then
    printf 'active'
  else
    printf 'inactive'
  fi
}
verify_existing_database_password() {
  [[ -d "${data_dir}/mysql" && -n "${mysql_password}" ]] || return 0
  local mysql_client client_file login_path_file output
  mysql_client="${install_dir}/bin/mysql"
  if [[ ! -x "${mysql_client}" ]]; then
    mysql_client="$(command -v mysql || true)"
  fi
  [[ -x "${mysql_client}" ]] || return 0
  client_file="$(mktemp "${TMPDIR:-/tmp}/oneinstack-mysql-precheck.XXXXXX")"
  login_path_file="$(mktemp "${TMPDIR:-/tmp}/oneinstack-mysql-login.XXXXXX")"
  chmod 0600 "${client_file}"
  chmod 0600 "${login_path_file}"
  cat >"${client_file}" <<EOF
[client]
user=root
password='${mysql_password}'
host=127.0.0.1
port=${mysql_port}
protocol=tcp
get-server-public-key
EOF
  if output="$(MYSQL_TEST_LOGIN_FILE="${login_path_file}" "${mysql_client}" \
    --defaults-file="${client_file}" --user=root --host=127.0.0.1 \
    --port="${mysql_port}" --protocol=tcp \
    --batch --skip-column-names --execute="SELECT 1" 2>&1)"; then
    rm -f -- "${client_file}"
    rm -f -- "${login_path_file}"
    grep -Fxq "1" <<<"${output}" ||
      die "Existing MySQL data directory could not be verified with the supplied root password."
    return 0
  fi
  rm -f -- "${client_file}"
  rm -f -- "${login_path_file}"
  if grep -Eqi "access denied|authentication plugin|using password" <<<"${output}"; then
    if existing_root_password_reset_authorized; then
      emit_progress 92 password_reset.authorized "已有 MySQL root 密码验证失败，将按显式确认执行受控重置"
      return 0
    fi
    die "Existing MySQL data directory was found, but the supplied root password was rejected. Use the current root password to repair this instance."
  fi
}
existing_root_password_reset_authorized() {
  [[ "${reset_existing_root_password}" == true &&
    "${reset_existing_root_password_confirm}" == true ]]
}
recover_existing_root_password() (
  existing_root_password_reset_authorized ||
    die "Resetting an existing MySQL root password requires both explicit confirmation parameters."
  [[ -d "${data_dir}/mysql" ]] || die "Existing MySQL data directory is unavailable for password recovery."
  [[ -n "${mysql_password}" ]] || die "MYSQL_PASSWORD is required for MySQL root password recovery."
  [[ -x "${install_dir}/bin/mysqld" && -x "${install_dir}/bin/mysql" ]] ||
    die "MySQL recovery binaries are unavailable."

  # Keep recovery state outside mysql.service's RuntimeDirectory. CentOS 7's
  # systemd 219 fails with status=233/RUNTIME_DIRECTORY when /run/mysqld was
  # recreated by the recovery process before the managed service restarts.
  local recovery_dir="/run/oneinstack-mysql-recovery"
  local recovery_socket="${recovery_dir}/mysql.sock"
  local recovery_pid_file="${recovery_dir}/mysqld.pid"
  local recovery_log="${log_dir}/mysql-recovery.log"
  local recovery_client_file=""
  local recovery_pid="" ready=false output

  # shellcheck disable=SC2329 # Invoked indirectly by the EXIT trap below.
  cleanup_recovery_instance() {
    local exit_status="$?"
    trap - EXIT
    if [[ -n "${recovery_pid}" ]] && kill -0 "${recovery_pid}" 2>/dev/null; then
      kill "${recovery_pid}" 2>/dev/null || true
      wait "${recovery_pid}" 2>/dev/null || true
    fi
    [[ -z "${recovery_client_file}" ]] || rm -f -- "${recovery_client_file}"
    rm -rf -- "${recovery_dir}"
    exit "${exit_status}"
  }
  trap cleanup_recovery_instance EXIT

  emit_progress 94 recover_root_password "正在通过隔离的本地 MySQL 实例重置 root 密码"
  service_stop
  for _ in $(seq 1 30); do
    service_is_active || break
    sleep 1
  done
  service_is_active && die "MySQL service did not stop before root password recovery."

  rm -rf -- "${recovery_dir}"
  install -d -o "${run_user}" -g "${run_group}" -m 0700 -- "${recovery_dir}"
  install -d -o "${run_user}" -g "${run_group}" -m 0750 -- "${log_dir}"
  "${install_dir}/bin/mysqld" \
    --defaults-file="${config_file}" \
    --user="${run_user}" \
    --skip-grant-tables \
    --skip-networking \
    --socket="${recovery_socket}" \
    --pid-file="${recovery_pid_file}" \
    --log-error="${recovery_log}" \
    >/dev/null 2>&1 &
  recovery_pid="$!"
  for _ in $(seq 1 60); do
    if [[ -S "${recovery_socket}" ]]; then
      ready=true
      break
    fi
    kill -0 "${recovery_pid}" 2>/dev/null || break
    sleep 1
  done
  if [[ "${ready}" != true ]]; then
    die "MySQL recovery socket was not ready; the existing data was not modified."
  fi

  if ! output="$("${install_dir}/bin/mysql" \
    --no-defaults \
    --protocol=socket \
    --socket="${recovery_socket}" \
    -uroot <<EOF
FLUSH PRIVILEGES;
CREATE USER IF NOT EXISTS 'root'@'localhost' IDENTIFIED BY '${mysql_password}';
ALTER USER 'root'@'localhost' IDENTIFIED BY '${mysql_password}';
CREATE USER IF NOT EXISTS 'root'@'127.0.0.1' IDENTIFIED BY '${mysql_password}';
ALTER USER 'root'@'127.0.0.1' IDENTIFIED BY '${mysql_password}';
GRANT ALL PRIVILEGES ON *.* TO 'root'@'localhost' WITH GRANT OPTION;
GRANT ALL PRIVILEGES ON *.* TO 'root'@'127.0.0.1' WITH GRANT OPTION;
FLUSH PRIVILEGES;
EOF
  )"; then
    [[ -z "${output}" ]] || printf '%s\n' "${output}" >&2
    die "MySQL root password recovery failed."
  fi

  recovery_client_file="$(mktemp "${state_dir}/recovery-client.XXXXXX")"
  chmod 0600 "${recovery_client_file}"
  cat >"${recovery_client_file}" <<EOF
[client]
user=root
password='${mysql_password}'
socket=${recovery_socket}
protocol=socket
EOF
  if ! "${install_dir}/bin/mysqladmin" --defaults-file="${recovery_client_file}" shutdown >/dev/null 2>&1; then
    kill "${recovery_pid}" 2>/dev/null || true
  fi
  wait "${recovery_pid}" 2>/dev/null || true
  recovery_pid=""
  rm -f -- "${recovery_client_file}"
  recovery_client_file=""
  rm -rf -- "${recovery_dir}"

  service_start
  ready=false
  for _ in $(seq 1 120); do
    if [[ -S /run/mysqld/mysqld.sock ]] && service_is_active; then
      ready=true
      break
    fi
    service_is_active || break
    sleep 1
  done
  [[ "${ready}" == true ]] || die "MySQL did not restart after root password recovery."
  emit_progress 96 recover_root_password.completed "MySQL root 密码已完成受控重置"
)
persist_install_parameters() {
	install -d -m 0750 -- "${state_dir}"
	local temporary
	temporary="$(mktemp "${state_dir}/.install-parameters.XXXXXX")"
	{
		printf 'SOFTWARE_VERSION=%s\n' "${software_version}"
		printf 'MYSQL_PORT=%s\n' "${mysql_port}"
		printf 'MYSQL_BIND_ADDRESS=%s\n' "${bind_address}"
		printf 'MYSQL_USERNAME=%s\n' "${mysql_username}"
		printf 'INSTALL_DIR=%s\n' "${install_dir}"
		printf 'DATA_DIR=%s\n' "${data_dir}"
		printf 'LOG_DIR=%s\n' "${log_dir}"
		printf 'RUN_USER=%s\n' "${run_user}"
		printf 'RUN_GROUP=%s\n' "${run_group}"
	} >"${temporary}"
	chmod 0600 "${temporary}"
	mv -f -- "${temporary}" "${install_parameters_file}"
}
validate_inputs() {
	[[ "${software_version}" == "8.0.45" ]] || die "Unsupported MySQL version: ${software_version}; only the Center-published exact release 8.0.45 is supported."
	patch_version="${software_version}"
	[[ "${install_mode}" == "center" || "${install_mode}" == "offline" ]] || die "ONEINSTACK_INSTALL_MODE must be center or offline."
	if [[ "${install_mode}" == "offline" ]]; then
		[[ -n "${offline_package_path}" && "${offline_package_path}" == /* &&
			"$(realpath -m -- "${offline_package_path}")" == "${offline_package_path}" ]] ||
			die "Offline installation requires a normalized absolute Bundle path."
	elif [[ -n "${offline_package_path}" ]]; then
		die "Offline Bundle path cannot be used in Center mode."
	fi
	[[ "${mysql_port}" =~ ^[0-9]+$ && "${mysql_port}" -ge 1 && "${mysql_port}" -le 65535 ]] || die "Invalid MYSQL_PORT."
	[[ "${bind_address}" =~ ^[0-9a-fA-F:.]+$ ]] || die "MYSQL_BIND_ADDRESS must be an IP address."
	[[ -z "${mysql_password}" || "${mysql_password}" =~ ^[A-Za-z0-9_@%+=:,.!#?-]{12,128}$ ]] || die "MYSQL_PASSWORD must be 12-128 safe characters."
	[[ "${mysql_username}" =~ ^[a-z_][a-z0-9_-]{0,31}$ ]] || die "MYSQL_USERNAME is invalid."
	[[ "${migrate_external}" == true || "${migrate_external}" == false ]] || die "MIGRATE_EXTERNAL_MYSQL must be true or false."
	[[ "${migrate_external_confirm}" == true || "${migrate_external_confirm}" == false ]] || die "MIGRATE_EXTERNAL_CONFIRM must be true or false."
	[[ "${reset_existing_root_password}" == true || "${reset_existing_root_password}" == false ]] || die "RESET_EXISTING_ROOT_PASSWORD must be true or false."
	[[ "${reset_existing_root_password_confirm}" == true || "${reset_existing_root_password_confirm}" == false ]] || die "RESET_EXISTING_ROOT_PASSWORD_CONFIRM must be true or false."
	[[ "${run_user}" =~ ^[a-z_][a-z0-9_-]{0,31}$ ]] || die "RUN_USER is invalid."
	[[ "${run_group}" =~ ^[a-z_][a-z0-9_-]{0,31}$ ]] || die "RUN_GROUP is invalid."
	validate_path "${install_dir}" INSTALL_DIR; validate_path "${data_dir}" DATA_DIR; validate_path "${log_dir}" LOG_DIR
	validate_path "${state_root}" ONEINSTACK_COMPONENT_STATE
	select_source
}
check_host() {
	[[ -r /etc/os-release ]] || die "Cannot identify the Linux distribution."
	local os_id os_version
	# shellcheck disable=SC1091
	source /etc/os-release
	system_id="${ID:-}"
	system_version="${VERSION_ID:-}"
	os_id="${system_id}"
	os_version="${system_version}"
	case "${os_id}" in
		ubuntu) case "${os_version}" in 22.04|24.04|26.04) ;; *) die "Unsupported Ubuntu release: ${os_version:-unknown}" ;; esac ;;
		debian) case "${os_version}" in 11|12|13) ;; *) die "Unsupported Debian release: ${os_version:-unknown}" ;; esac ;;
		rhel|rocky|almalinux|ol) case "${os_version%%.*}" in 8|9|10) ;; *) die "Unsupported Enterprise Linux release: ${os_version:-unknown}" ;; esac ;;
		centos) case "${os_version%%.*}" in 7|8|9|10) ;; *) die "Unsupported CentOS release: ${os_version:-unknown}" ;; esac ;;
		fedora|opensuse-leap|opensuse-tumbleweed|opensuse) ;;
		sles) case "${os_version%%.*}" in 15|16) ;; *) die "Unsupported SLES release: ${os_version:-unknown}" ;; esac ;;
		amzn) [[ "${os_version}" == "2023" ]] || die "Unsupported Amazon Linux release: ${os_version:-unknown}" ;;
		*) die "Unsupported Linux distribution: ${os_id:-unknown}" ;;
	esac
	host_arch="$(architecture_name)"
	if command -v apt-get >/dev/null 2>&1; then package_manager="apt"
	elif command -v dnf >/dev/null 2>&1; then package_manager="dnf"
	elif command -v yum >/dev/null 2>&1; then package_manager="yum"
	elif command -v zypper >/dev/null 2>&1; then package_manager="zypper"
	else die "No supported package manager (apt, dnf, yum, or zypper) was found."; fi
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
    *.list)
      sed -E '/^[[:space:]]*deb(-src)?([[:space:]]|\[).*download\.docker\.com\/linux\//d' \
        "${source_file}" >"${target_file}"
      ;;
    *.sources)
      awk 'BEGIN { RS=""; ORS="\n\n" } $0 !~ /download\.docker\.com\/linux\// { print }' \
        "${source_file}" >"${target_file}"
      ;;
    *)
      cp -a -- "${source_file}" "${target_file}"
      ;;
  esac
}
apt_update_without_docker_source() (
  set -Eeuo pipefail
  local source_dir source_list source_file target_file
  source_dir="$(mktemp -d)"
  trap 'rm -rf -- "${source_dir}"' EXIT
  source_list="${source_dir}/sources.list"
  if [[ -f /etc/apt/sources.list ]]; then
    copy_apt_source_without_docker /etc/apt/sources.list "${source_list}"
  else
    : >"${source_list}"
  fi
  for source_file in /etc/apt/sources.list.d/*.list /etc/apt/sources.list.d/*.sources; do
    [[ -f "${source_file}" ]] || continue
    target_file="${source_dir}/$(basename -- "${source_file}")"
    copy_apt_source_without_docker "${source_file}" "${target_file}"
  done
  apt-get update \
    -o "Dir::Etc::sourcelist=${source_list}" \
    -o "Dir::Etc::sourceparts=${source_dir}/"
)
apt_update_for_mysql() {
  local docker_sources=()
  mapfile -t docker_sources < <(docker_apt_sources)
  if ((${#docker_sources[@]} > 0)); then
    emit_progress 6 apt.repository.filtered "检测到 Docker APT 源，已隔离其源配置以更新 MySQL 依赖"
    apt_update_without_docker_source
  else
    apt-get update
  fi
}
install_dependencies() {
	if [[ "${install_mode}" == "offline" ]]; then
		install_dependencies_offline
		return
	fi
	local manager ncurses_package
	if command -v apt-get >/dev/null 2>&1; then
		export DEBIAN_FRONTEND=noninteractive
		apt_update_for_mysql
		local aio_package="libaio1"
		if ! apt-cache policy libaio1 2>/dev/null | awk '/Candidate:/ {found=1; if ($2 != "(none)") available=1} END {exit !(found && available)}'; then
			aio_package="libaio1t64"
		fi
		apt-get install -y --no-install-recommends acl ca-certificates curl gnupg "${aio_package}" iproute2 libncurses6 libnuma1 libtinfo6 xz-utils
		return
	fi
	if command -v dnf >/dev/null 2>&1; then manager=dnf
	elif command -v yum >/dev/null 2>&1; then manager=yum
	elif command -v zypper >/dev/null 2>&1; then manager=zypper
	else die "No supported package manager (apt, dnf, yum, or zypper) was found."; fi
	case "${manager}" in
		dnf|yum)
		ncurses_package="ncurses-compat-libs"
		if [[ "${system_id}" == centos && "${system_version%%.*}" == 7 ]]; then
			ncurses_package="ncurses-libs"
		fi
		"${manager}" install -y acl ca-certificates curl gnupg2 iproute libaio "${ncurses_package}" numactl-libs tar xz
		;;
		zypper)
		zypper --non-interactive install -y acl ca-certificates curl gpg2 iproute2 libaio1 libnuma1 ncurses6 tar xz
		;;
	esac
}
offline_package_dir() {
	[[ -n "${offline_package_path}" ]] || die "Offline installation requires ONEINSTACK_OFFLINE_PACKAGE_PATH."
	printf '%s/packages/%s/%s/%s\n' "${offline_package_path}" "${system_id}" "${system_version}" "${host_arch}"
}
validate_offline_bundle() {
	local package_dir
	[[ -d "${offline_package_path}" ]] || die "Offline MySQL Bundle is unavailable."
	[[ -f "${offline_package_path}/manifest.yaml" ]] || die "Offline Bundle manifest is missing."
	[[ -f "${offline_package_path}/files.sha256" ]] || die "Offline Bundle checksum file is missing."
	package_dir="$(offline_package_dir)"
	[[ -d "${package_dir}" ]] || die "Offline MySQL dependencies are missing for this host."
	[[ -f "${offline_package_path}/artifacts/${host_arch}/${source_archive}" ]] ||
		die "Offline MySQL 8.0.45 artifact is missing for ${host_arch}."
	(cd "${offline_package_path}" && sha256sum -c files.sha256 --status) || die "Offline Bundle checksum verification failed."
	grep -Eq '^[[:space:]]+id:[[:space:]]+mysql[[:space:]]*$' "${offline_package_path}/manifest.yaml" || die "Offline Bundle component is not MySQL."
	grep -Eq "^[[:space:]]+version:[[:space:]]+1\\.0\\.30[[:space:]]*$" "${offline_package_path}/manifest.yaml" || die "Offline Bundle package version does not match MySQL 1.0.30."
}
install_dependencies_offline() {
	local package_dir
	local -a packages=()
	package_dir="$(offline_package_dir)"
	mapfile -t packages < <(find "${package_dir}" -maxdepth 1 -type f \( -name '*.deb' -o -name '*.rpm' \) -print | sort)
	((${#packages[@]} > 0)) || die "Offline MySQL dependency packages are missing."
	case "${package_manager}" in
		apt) DEBIAN_FRONTEND=noninteractive dpkg -i "${packages[@]}" || apt-get -y --no-download -f install ;;
		dnf|yum) "${package_manager}" --disablerepo='*' --cacheonly install -y "${packages[@]}" ;;
		zypper) zypper --non-interactive --no-refresh --no-gpg-checks install --allow-unsigned-rpm "${packages[@]}" ;;
	esac
}
ensure_account() {
	getent group "${run_group}" >/dev/null || groupadd --system "${run_group}"
	id "${run_user}" >/dev/null 2>&1 || useradd --system --gid "${run_group}" --home-dir "${data_dir}" --shell /usr/sbin/nologin "${run_user}"
	id -nG "${run_user}" | tr ' ' '\n' | grep -Fxq "${run_group}" || usermod -a -G "${run_group}" "${run_user}"
}
record_managed_path_acl() {
  local acl_path="$1" record
  record="${run_user}"$'\t'"${acl_path}"
  install -d -m 0750 -- "${state_dir}"
  if [[ ! -f "${managed_path_acl_file}" ]] || ! grep -Fqx -- "${record}" "${managed_path_acl_file}"; then
    printf '%s\n' "${record}" >>"${managed_path_acl_file}"
    chmod 0600 "${managed_path_acl_file}"
  fi
  if [[ -f "${rollback_dir}/transaction-started" ]] &&
    { [[ ! -f "${transaction_path_acl_file}" ]] || ! grep -Fqx -- "${record}" "${transaction_path_acl_file}"; }; then
    printf '%s\n' "${record}" >>"${transaction_path_acl_file}"
    chmod 0600 "${transaction_path_acl_file}"
  fi
}
ensure_default_data_root_access() {
  local current_acl access_mask existing_user_permissions
  if [[ "${data_dir}" != /data/* && "${log_dir}" != /data/* ]]; then
    return 0
  fi
  runuser -u "${run_user}" -- test -x /data && return 0
  require_command getfacl
  require_command setfacl
  [[ "$(stat -c '%u' /data)" == 0 ]] ||
    die "MySQL cannot safely grant access to DATA_DIR because /data is not root-owned."
  if ! current_acl="$(getfacl -cp -- /data)"; then
    die "MySQL could not inspect the existing /data ACL policy."
  fi
  existing_user_permissions="$(awk -F: -v user="${run_user}" '$1 == "user" && $2 == user {print $3; exit}' <<<"${current_acl}")"
  [[ -z "${existing_user_permissions}" ]] ||
    die "MySQL cannot safely replace the existing ACL entry for its runtime user on /data."
  access_mask="$(awk -F: '$1 == "mask" && $2 == "" {print $3; exit}' <<<"${current_acl}")"
  if [[ -n "${access_mask}" ]]; then
    [[ "${access_mask}" == *x* ]] ||
      die "MySQL cannot safely grant access to DATA_DIR because the existing /data ACL mask denies traversal."
    setfacl --no-mask -m "u:${run_user}:--x" -- /data ||
      die "MySQL could not grant its runtime user traverse access to /data."
  else
    setfacl -m "u:${run_user}:--x" -- /data ||
      die "MySQL could not grant its runtime user traverse access to /data."
  fi
  record_managed_path_acl /data
  runuser -u "${run_user}" -- test -x /data ||
    die "MySQL could not grant its runtime user traverse access to /data."
  emit_progress 10 permissions.data_root.applied "已为 MySQL 运行账户授予数据根目录最小穿越权限"
}
remove_managed_path_acl_entries() {
  local records_file="$1" acl_user acl_path current_permissions
  [[ -f "${records_file}" ]] || return 0
  if ! command -v getfacl >/dev/null 2>&1 || ! command -v setfacl >/dev/null 2>&1; then
    printf 'WARNING: managed MySQL path ACL could not be restored because ACL tools are unavailable.\n' >&2
    return 0
  fi
  while IFS=$'\t' read -r acl_user acl_path; do
    [[ "${acl_user}" =~ ^[a-z_][a-z0-9_-]{0,31}$ && "${acl_path}" == /data ]] || continue
    [[ -d "${acl_path}" ]] || continue
    current_permissions="$(getfacl -cp -- "${acl_path}" 2>/dev/null | awk -F: -v user="${acl_user}" '$1 == "user" && $2 == user {print $3; exit}' || true)"
    if [[ "${current_permissions}" == "--x" ]]; then
      setfacl --no-mask -x "u:${acl_user}" -- "${acl_path}" ||
        printf 'WARNING: managed MySQL path ACL could not be restored for %s.\n' "${acl_path}" >&2
    elif [[ -n "${current_permissions}" ]]; then
      printf 'WARNING: managed MySQL path ACL for %s was changed externally and was preserved.\n' "${acl_path}" >&2
    fi
  done <"${records_file}"
}
restore_transaction_path_acl() {
  local temporary record
  [[ -f "${transaction_path_acl_file}" ]] || return 0
  remove_managed_path_acl_entries "${transaction_path_acl_file}"
  if [[ -f "${managed_path_acl_file}" ]]; then
    temporary="$(mktemp "${state_dir}/.managed-path-acl.XXXXXX")"
    while IFS= read -r record; do
      grep -Fqx -- "${record}" "${transaction_path_acl_file}" || printf '%s\n' "${record}" >>"${temporary}"
    done <"${managed_path_acl_file}"
    if [[ -s "${temporary}" ]]; then
      chmod 0600 "${temporary}"
      mv -f -- "${temporary}" "${managed_path_acl_file}"
    else
      rm -f -- "${temporary}" "${managed_path_acl_file}"
    fi
  fi
}
normalize_runtime_permissions() {
  ensure_account
	install -d -o "${run_user}" -g "${run_group}" -m 0750 -- "${data_dir}" "${log_dir}"
	chown -R "${run_user}:${run_group}" "${data_dir}" "${log_dir}"
  chmod 0750 "${data_dir}"
  chmod 0755 "${install_dir}" "${install_dir}/bin"
  emit_progress 58 permissions.runtime.applied "MySQL runtime ownership and directory permissions applied"
}
verify_runtime_permissions() {
  require_command runuser
	runuser -u "${run_user}" -- test -x "${install_dir}/bin/mysqld" ||
    die "MySQL runtime user cannot execute the managed server binary."
	runuser -u "${run_user}" -- test -x "$(dirname -- "${data_dir}")" ||
    die "MySQL runtime user cannot traverse the DATA_DIR parent path."
	runuser -u "${run_user}" -- test -r "${data_dir}" ||
    die "MySQL runtime user cannot read the managed data directory."
	runuser -u "${run_user}" -- test -w "${data_dir}" ||
    die "MySQL runtime user cannot write the managed data directory."
	runuser -u "${run_user}" -- test -x "$(dirname -- "${log_dir}")" ||
    die "MySQL runtime user cannot traverse the LOG_DIR parent path."
	runuser -u "${run_user}" -- test -w "${log_dir}" ||
    die "MySQL runtime user cannot write the managed log directory."
  emit_progress 10 permissions.runtime.verified "MySQL runtime access verified as the mysql user"
}
managed_installation_present() {
  [[ -f "${state_dir}/version" &&
    -x "${install_dir}/bin/mysqld" &&
    -d "${data_dir}/mysql" ]]
}
external_mysql_detected() {
  managed_installation_present && return 1
  [[ ! -f "${state_dir}/version" ]] &&
    { command -v mysqld >/dev/null 2>&1 || command -v mariadbd >/dev/null 2>&1 ||
      [[ -d /var/lib/mysql ]] ||
      dpkg-query -W -f='${Status}' mysql-server 2>/dev/null | grep -Fq 'install ok installed' ||
      rpm_server_package_detected; }
}
rpm_server_package_detected() {
  local package
  while IFS= read -r package; do
    case "${package}" in
      mysql-server-*|mysql-community-server-*|mariadb-server-*|MariaDB-server-*|percona-server-server-*|Percona-Server-server-*)
        return 0
        ;;
    esac
  done < <(rpm -qa 2>/dev/null || true)
  return 1
}
rpm_mariadb_or_percona_server_detected() {
  local package
  while IFS= read -r package; do
    case "${package}" in
      mariadb-server-*|MariaDB-server-*|percona-server-server-*|Percona-Server-server-*)
        return 0
        ;;
    esac
  done < <(rpm -qa 2>/dev/null || true)
  return 1
}
snapshot_external_mysql() {
  external_mysql_detected || return 0
  [[ "${migrate_external}" == true && "${migrate_external_confirm}" == true ]] ||
    die "An external MySQL-compatible instance was detected; set MIGRATE_EXTERNAL_MYSQL=true and MIGRATE_EXTERNAL_CONFIRM=true to authorize controlled migration."
  if command -v mariadbd >/dev/null 2>&1 ||
    dpkg-query -W -f='${Status}' mariadb-server 2>/dev/null | grep -Fq 'install ok installed' ||
    rpm_mariadb_or_percona_server_detected; then
    die "MariaDB or Percona data cannot be migrated automatically to MySQL 8.0."
  fi
  external_binary="$(command -v mysqld || true)"
  [[ -n "${external_binary}" ]] || die "External MySQL server binary cannot be identified."
  "${external_binary}" --version 2>/dev/null | grep -Fq 'Ver 8.0' ||
    die "Only an external MySQL 8.0 installation can be migrated automatically."
  install -d -m 0700 -- "${external_migration_dir}"
  local external_data="/var/lib/mysql" package manager
  external_data="$(${external_binary} --verbose --help 2>/dev/null | awk '$1 == "datadir" {print $2; exit}')"
  [[ -n "${external_data}" ]] || external_data="/var/lib/mysql"
  external_data="$(realpath -m -- "${external_data}")"
  validate_path "${external_data}" EXTERNAL_DATA_DIR
  printf '%s\n' "${external_data}" >"${external_migration_dir}/data-path"
  : >"${external_migration_dir}/package-versions"
  if command -v dpkg-query >/dev/null 2>&1; then
    manager=apt
    dpkg-query -W -f='${binary:Package}\t${Version}\n' 2>/dev/null |
      awk '$1 ~ /^(mysql-server|mysql-client|mysql-community)/ {print}' >"${external_migration_dir}/package-versions" || true
  elif command -v rpm >/dev/null 2>&1; then
    manager=rpm
    rpm -qa --qf '%{NAME}\t%{VERSION}-%{RELEASE}\n' 'mysql*' >"${external_migration_dir}/package-versions" 2>/dev/null || true
  else
    manager=unknown
  fi
  printf '%s\n' "${manager}" >"${external_migration_dir}/package-manager"
  [[ -f /etc/mysql/my.cnf ]] && cp -a -- /etc/mysql "${external_migration_dir}/config"
  if has_systemd; then
    systemctl is-active --quiet mysql.service 2>/dev/null && : >"${external_migration_dir}/was-active" || true
    systemctl is-enabled --quiet mysql.service 2>/dev/null && : >"${external_migration_dir}/was-enabled" || true
    systemctl stop mysql.service mysqld.service 2>/dev/null || true
  fi
  emit_progress 25 migration.snapshot.created "External MySQL 8.0 package, configuration, and data-path snapshot created"
}
migrate_external_mysql_data() {
  [[ -r "${external_migration_dir}/data-path" ]] || return 0
  local external_data
  read -r external_data <"${external_migration_dir}/data-path"
  [[ "${external_data}" != "${data_dir}" ]] || return 0
  [[ -d "${external_data}/mysql" ]] || die "External MySQL data dictionary is missing."
  [[ ! -e "${data_dir}/mysql" ]] || die "Target MySQL data directory already contains a database."
  emit_progress 35 migration.data.copying "Copying external MySQL data into the managed data directory"
  install -d -o "${run_user}" -g "${run_group}" -m 0750 -- "${data_dir}"
  cp -a -- "${external_data}/." "${data_dir}/"
  chown -R "${run_user}:${run_group}" "${data_dir}"
}
commit_external_mysql() {
  [[ -r "${external_migration_dir}/data-path" ]] || return 0
  local packages=() package version external_data manager migration_dir
  if [[ -s "${external_migration_dir}/package-versions" ]]; then
    while IFS=$'\t' read -r package version; do packages+=("${package}"); done <"${external_migration_dir}/package-versions"
    read -r manager <"${external_migration_dir}/package-manager" || manager=unknown
    case "${manager}" in
      apt) ((${#packages[@]} == 0)) || DEBIAN_FRONTEND=noninteractive apt-get remove -y "${packages[@]}" ;;
      dnf|yum|zypper) ((${#packages[@]} == 0)) || "${manager}" remove -y "${packages[@]}" ;;
      *) die "Cannot safely remove the recorded external MySQL packages." ;;
    esac
  fi
  read -r external_data <"${external_migration_dir}/data-path"
  if [[ "${external_data}" != "${data_dir}" && -d "${external_data}" ]]; then
    migration_dir="${state_dir}/migrations/$(date -u +%Y%m%dT%H%M%SZ)"
    install -d -m 0700 -- "${state_dir}/migrations"
    mv -- "${external_data}" "${migration_dir}-external-data"
  fi
  emit_progress 88 migration.commit.completed "External MySQL package and data replacement committed"
}
verify_source_signature() {
  local archive="$1" signature="$2" gpg_home status_line marker status signing_fingerprint
  local created timestamp expire version reserved pubkey hash class primary_fingerprint rest
  local key_file key_verified signature_verified
  local gpg_command="gpg"
  command -v "${gpg_command}" >/dev/null 2>&1 || gpg_command="gpg2"
  require_command "${gpg_command}"
  gpg_home="$(mktemp -d "$(dirname -- "${archive}")/.gnupg.XXXXXX")"
  chmod 0700 "${gpg_home}"
  key_file="${gpg_home}/mysql-release.key"
  curl --proto '=https' --tlsv1.2 --fail --location --retry 3 --connect-timeout 20 \
    --output "${key_file}" "${release_key_url}"
  "${gpg_command}" --batch --homedir "${gpg_home}" --import "${key_file}" >/dev/null 2>&1
  key_verified=false
  while IFS= read -r status_line; do
    [[ "${status_line}" == "${source_fingerprint}" ]] && key_verified=true
  done < <("${gpg_command}" --batch --homedir "${gpg_home}" --with-colons --fingerprint 2>/dev/null | awk -F: '$1 == "fpr" {print $10}')
  [[ "${key_verified}" == true ]] || { rm -rf -- "${gpg_home}"; die "MySQL release signing key fingerprint verification failed."; }
  status_line="$(${gpg_command} --batch --homedir "${gpg_home}" --status-fd=1 --verify "${signature}" "${archive}" 2>/dev/null)" || {
    rm -rf -- "${gpg_home}"
    die "MySQL source signature verification failed."
  }
  signature_verified=false
  while IFS=' ' read -r marker status signing_fingerprint _ _ _ _ _ _ _ _ primary_fingerprint _; do
    [[ "${marker}" == "[GNUPG:]" && "${status}" == "VALIDSIG" ]] || continue
    [[ "${signing_fingerprint}" == "${source_fingerprint}" || "${primary_fingerprint}" == "${source_fingerprint}" ]] && signature_verified=true
  done <<<"${status_line}"
  rm -rf -- "${gpg_home}"
  [[ "${signature_verified}" == true ]] || die "MySQL source signature fingerprint verification failed."
}
verify_source_checksum() {
  local archive="$1"
  [[ -z "${source_sha256}" ]] ||
    printf '%s  %s\n' "${source_sha256}" "${archive}" | sha256sum --check --status
}
download_verified() {
  local destination="$1"
  local cache_dir="/var/cache/oneinstack/downloads"
  local architecture cache_file signature_cache temporary_cache temporary_signature
  select_source
  { [[ -n "${source_sha256}" ]] ||
    [[ -n "${source_signature_url}" && -n "${source_fingerprint}" ]]; } ||
    die "MySQL source signature verification is not configured."
	architecture="$(architecture_name)"
	if [[ "${install_mode}" == "offline" ]]; then
		local offline_source="${offline_package_path}/artifacts/${architecture}/${source_archive}"
		[[ -f "${offline_source}" ]] || die "Offline MySQL source artifact is missing."
		verify_source_checksum "${offline_source}" || die "Offline MySQL archive checksum verification failed."
		cp -- "${offline_source}" "${destination}"
		return
	fi
	cache_file="${cache_dir}/mysql-${patch_version}-linux-${architecture}.tar.xz"
  signature_cache="${cache_file}.asc"
  install -d -m 0750 -- "${cache_dir}"
  if [[ -f "${cache_file}" ]] &&
    verify_source_checksum "${cache_file}" &&
    { [[ -z "${source_signature_url}" ]] || [[ -f "${signature_cache}" ]]; }; then
    if [[ -n "${source_signature_url}" ]]; then
      verify_source_signature "${cache_file}" "${signature_cache}"
    fi
    cp -- "${cache_file}" "${destination}"
    return
  fi
  temporary_cache="$(mktemp "${cache_file}.tmp.XXXXXX")"
  temporary_signature=""
  if [[ -n "${source_signature_url}" ]]; then
    temporary_signature="$(mktemp "${signature_cache}.tmp.XXXXXX")"
  fi
  trap 'rm -f -- "${temporary_cache}" "${temporary_signature}"' RETURN
  curl --proto '=https' --tlsv1.2 --fail --location --retry 3 \
    --connect-timeout 20 --output "${temporary_cache}" "${source_url}"
  if [[ -n "${source_signature_url}" ]]; then
    curl --proto '=https' --tlsv1.2 --fail --location --retry 3 \
      --connect-timeout 20 --output "${temporary_signature}" "${source_signature_url}"
  fi
  verify_source_checksum "${temporary_cache}" ||
    die "MySQL archive checksum verification failed."
  if [[ -n "${source_signature_url}" ]]; then
    verify_source_signature "${temporary_cache}" "${temporary_signature}"
    chmod 0640 "${temporary_cache}" "${temporary_signature}"
  else
    chmod 0640 "${temporary_cache}"
  fi
  mv -f -- "${temporary_cache}" "${cache_file}"
  if [[ -n "${source_signature_url}" ]]; then
    mv -f -- "${temporary_signature}" "${signature_cache}"
  fi
  trap - RETURN
  cp -- "${cache_file}" "${destination}"
}
prepare_rollback() {
  install -d -m 0750 -- "${state_dir}"
  rm -rf -- "${rollback_dir}"; install -d -m 0750 -- "${rollback_dir}"
  : >"${rollback_dir}/transaction-started"
  rm -f -- "${state_dir}/initialized-this-run"
  if service_is_active; then : >"${rollback_dir}/was-active"; service_stop; fi
  [[ ! -e "${install_dir}" ]] || mv -- "${install_dir}" "${rollback_dir}/install"
  [[ ! -e "${unit_file}" ]] || cp -a -- "${unit_file}" "${rollback_dir}/mysql.service"
  [[ ! -e "${config_file}" ]] || cp -a -- "${config_file}" "${rollback_dir}/my.cnf"
  snapshot_external_mysql
}
restore_rollback() {
  if [[ ! -f "${rollback_dir}/transaction-started" ]]; then
    echo "MySQL rollback skipped because installation did not create a rollback point."
    return 0
  fi
  service_stop
  [[ ! -e "${install_dir}" ]] || rm -rf -- "${install_dir}"
  [[ ! -e "${rollback_dir}/install" ]] || mv -- "${rollback_dir}/install" "${install_dir}"
  if [[ -e "${rollback_dir}/mysql.service" ]]; then cp -a -- "${rollback_dir}/mysql.service" "${unit_file}"; else rm -f -- "${unit_file}"; fi
  if [[ -e "${rollback_dir}/my.cnf" ]]; then cp -a -- "${rollback_dir}/my.cnf" "${config_file}"; else rm -f -- "${config_file}"; fi
  if [[ -e "${state_dir}/initialized-this-run" && -d "${data_dir}" ]]; then
    mv -- "${data_dir}" "${state_dir}/failed-data-$(date -u +%Y%m%dT%H%M%SZ)"
    rm -f -- "${state_dir}/initialized-this-run"
  fi
  if has_systemd; then systemctl daemon-reload; fi
  [[ ! -e "${rollback_dir}/was-active" ]] || service_start
  restore_transaction_path_acl
  rm -rf -- "${rollback_dir}"
}
