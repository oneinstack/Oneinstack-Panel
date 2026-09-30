#!/usr/bin/env bash
set -Eeuo pipefail
umask 027

component_id="php"
component_name="PHP"
software_version="${SOFTWARE_VERSION:-}"
install_dir="${INSTALL_DIR:-/usr/local/php}"
log_dir="${LOG_DIR:-/var/log/php-fpm}"
socket_path="${SOCKET_PATH:-/dev/shm/php-cgi.sock}"
run_user="${RUN_USER:-www}"
run_group="${RUN_GROUP:-www}"
memory_limit="${PHP_MEMORY_LIMIT:-256M}"
state_root="${ONEINSTACK_COMPONENT_STATE:-/var/lib/oneinstack/components}"
migrate_external_php="${MIGRATE_EXTERNAL_PHP:-false}"
migrate_external_confirm="${MIGRATE_EXTERNAL_CONFIRM:-false}"
data_policy="${DATA_POLICY:-preserve}"
delete_data_confirm="${DELETE_DATA_CONFIRM:-false}"
lifecycle_action="${ONEINSTACK_ACTION:-install}"
install_mode="${ONEINSTACK_INSTALL_MODE:-center}"
offline_package_path="${ONEINSTACK_OFFLINE_PACKAGE_PATH:-}"

state_dir="${state_root}/${component_id}"
rollback_dir="${state_dir}/rollback"
runtime_line="${software_version%.*}"
service_name="php-fpm"
if [[ "${software_version}" == 5.3.29 || "${software_version}" == 5.4.45 || "${software_version}" == 7.0.33 ]]; then
  service_name="php-fpm-${runtime_line}"
  if [[ "${SOCKET_PATH:-/dev/shm/php-cgi.sock}" == "/dev/shm/php-cgi.sock" &&
    "${ONEINSTACK_PARAMETER_SOCKET_PATH_EXPLICIT:-false}" != "true" ]]; then
    socket_path="/dev/shm/php-${runtime_line}.sock"
  fi
fi
unit_file="/etc/systemd/system/${service_name}.service"
pid_file="${state_dir}/php-fpm.pid"
external_migration_dir="${rollback_dir}/external"
php_ini_file="${install_dir}/lib/php.ini"
managed_ini_file="${install_dir}/etc/php.d/99-oneinstack.ini"
fpm_config_file="${install_dir}/etc/php-fpm.conf"
pool_config_file="${install_dir}/etc/php-fpm.d/99-oneinstack.conf"
log_file="${log_dir}/php-fpm.log"

die_code() {
  local code="$1"
  shift
  printf 'ERROR_CODE=%s\n' "${code}" >&2
  printf 'ERROR: %s\n' "$*" >&2
  exit 1
}

architecture_name() {
  case "$(uname -m)" in
    x86_64) printf '%s\n' amd64 ;;
    aarch64|arm64) printf '%s\n' arm64 ;;
    *) die_code HOST_UNSUPPORTED "Unsupported CPU architecture" ;;
  esac
}

patch_version=""
build_id=""
source_url=""
source_sha256=""
source_archive=""
source_urls=()
php_release_metadata_url="https://www.php.net/releases/index.php?json=1"
php_release_index_url="https://www.php.net/releases/index.php"
libzip_source_version="1.2.0"
libzip_source_url="https://libzip.org/download/libzip-1.2.0.tar.gz"
libzip_source_sha256="6cf9840e427db96ebf3936665430bab204c9ebbd0120c326459077ed9c907d9f"
libzip_build_prefix=""
legacy_openssl_source_version="1.0.2u"
legacy_openssl_source_archive="openssl-${legacy_openssl_source_version}.tar.gz"
legacy_openssl_source_sha256="ecd0c6ffb493dd06707d38b14bb4d8c2288bb7033735606569d8f90f89669d16"
legacy_openssl_source_urls=(
  "https://www.openssl.org/source/old/1.0.2/${legacy_openssl_source_archive}"
  "https://mirror.openssl-library.org/source/old/1.0.2/${legacy_openssl_source_archive}"
)
legacy_openssl_build_prefix=""
openssl_legacy_compatibility=false
openssl_major_version=""
patch_version="${software_version}"

die() { die_code SCRIPT_FAILED "$@"; }

require_command() {
  command -v "$1" >/dev/null 2>&1 || die_code DEPENDENCY_MISSING "Required command is unavailable"
}

emit_progress() {
  local percent="$1" code="$2" message="$3" fd="${ONEINSTACK_PROGRESS_FD:-}"
  [[ "${fd}" =~ ^[0-9]+$ ]] || return 0
  message="${message//\\/\\\\}"
  message="${message//\"/\\\"}"
  message="${message//$'\n'/ }"
  printf '{"type":"progress","percent":%s,"code":"%s","message":"%s"}\n' \
    "${percent}" "${code}" "${message}" >"${fd}" 2>/dev/null || true
}

require_root() {
  [[ "$(id -u)" -eq 0 ]] || die_code PERMISSION_DENIED "This action requires root privileges"
}

validate_identifier() {
  [[ "$1" =~ ^[a-z_][a-z0-9_-]{0,30}$ ]] || die_code INVALID_PARAMETER "Runtime account value is invalid"
}

validate_path() {
  local value="$1" label="$2"
  [[ "${value}" == /* && "$(realpath -m -- "${value}")" == "${value}" ]] ||
    die_code INVALID_PARAMETER "${label} must be a normalized absolute path"
  case "${value}" in
    /|/usr|/usr/local|/etc|/var|/data|/home|/root)
      die_code INVALID_PARAMETER "${label} is too broad"
      ;;
  esac
}

validate_boolean() {
  [[ "$1" == "true" || "$1" == "false" ]] || die_code INVALID_PARAMETER "Boolean parameter is invalid"
}

select_source() {
	case "${patch_version}" in
	  5.3.29)
	    source_url="https://www.php.net/distributions/php-5.3.29.tar.xz"
	    source_sha256="8438c2f14ab8f3d6cd2495aa37de7b559e33b610f9ab264f0c61b531bf0c262d"
	    ;;
	  5.4.45)
	    source_url="https://www.php.net/distributions/php-5.4.45.tar.gz"
	    source_sha256="25bc4723955f4e352935258002af14a14a9810b491a19400d76fcdfa9d04b28f"
	    ;;
	  7.0.33)
	    source_url="https://www.php.net/distributions/php-7.0.33.tar.xz"
	    source_sha256="ab8c5be6e32b1f8d032909dedaaaa4bbb1a209e519abb01a52ce3914f9a13d96"
	    ;;
	  8.1.34)
	    source_url="https://www.php.net/distributions/php-8.1.34.tar.xz"
	    source_sha256="ffa9e0982e82eeaea848f57687b425ed173aa278fe563001310ae2638db5c251"
	    ;;
	  8.2.30)
	    source_url="https://www.php.net/distributions/php-8.2.30.tar.xz"
	    source_sha256="bc90523e17af4db46157e75d0c9ef0b9d0030b0514e62c26ba7b513b8c4eb015"
	    ;;
	  8.3.30)
	    source_url="https://www.php.net/distributions/php-8.3.30.tar.xz"
	    source_sha256="67f084d36852daab6809561a7c8023d130ca07fc6af8fb040684dd1414934d48"
	    ;;
	  *) die_code VERSION_UNSUPPORTED "PHP version is not a Center-published exact release" ;;
	esac
	source_archive="${source_url##*/}"
	source_urls=("${source_url}")
  if is_legacy_php; then
    build_id="php-${patch_version}-$(architecture_name)-legacy-oneinstack-1.0.15"
  else
    build_id="php-${patch_version}-$(architecture_name)-glibc-oneinstack-1.0.15"
  fi
}

is_legacy_php() {
  case "${patch_version}" in
    5.3.29|5.4.45|7.0.33) return 0 ;;
    *) return 1 ;;
  esac
}

validate_inputs() {
  [[ -n "${software_version}" && "${patch_version}" == "${software_version}" ]] ||
    die_code VERSION_UNSUPPORTED "Requested PHP version is not an exact supported release"
  select_source
  [[ "${install_mode}" == "center" || "${install_mode}" == "offline" ]] ||
    die_code INVALID_PARAMETER "ONEINSTACK_INSTALL_MODE must be center or offline"
  if [[ "${install_mode}" == "offline" ]]; then
    [[ -n "${offline_package_path}" ]] || die_code PACKAGE_UNAVAILABLE "Offline PHP installation requires a Bundle path"
    offline_package_path="$(realpath -m -- "${offline_package_path}")"
    [[ "${offline_package_path}" == /* ]] || die_code INVALID_PARAMETER "Offline Bundle path must be absolute"
  elif [[ -n "${offline_package_path}" ]]; then
    die_code INVALID_PARAMETER "Offline Bundle path is only valid in offline mode"
  fi
  [[ "${memory_limit}" =~ ^[0-9]+[MG]$ ]] || die_code INVALID_PARAMETER "PHP memory limit is invalid"
  validate_identifier "${run_user}"
  validate_identifier "${run_group}"
  validate_path "${install_dir}" INSTALL_DIR
  validate_path "${log_dir}" LOG_DIR
  validate_path "${socket_path}" SOCKET_PATH
  validate_path "${state_root}" ONEINSTACK_COMPONENT_STATE
  validate_boolean "${migrate_external_php}"
  validate_boolean "${migrate_external_confirm}"
  validate_boolean "${delete_data_confirm}"
  [[ "${data_policy}" == "preserve" || "${data_policy}" == "delete" ]] ||
    die_code INVALID_PARAMETER "DATA_POLICY must be preserve or delete"
  if [[ "${lifecycle_action}" == "uninstall" && "${data_policy}" == "delete" &&
    "${delete_data_confirm}" != "true" ]]; then
    die_code DATA_DELETE_CONFIRM_REQUIRED "Explicit data deletion confirmation is required"
  fi
}

systemd_available() {
  command -v systemctl >/dev/null 2>&1 && [[ -d /run/systemd/system ]]
}

component_architecture() {
  architecture_name
}

libc_flavor() {
  if command -v ldd >/dev/null 2>&1 && ldd --version 2>&1 | grep -qi musl; then
    printf '%s\n' musl
  else
    printf '%s\n' glibc
  fi
}

prepare_openssl_compatibility() {
  local openssl_version=""
  openssl_version="$(openssl version 2>/dev/null | awk 'NR == 1 {print $2}' || true)"
  if [[ "${openssl_version}" =~ ^([0-9]+)\. ]]; then
    openssl_major_version="${BASH_REMATCH[1]}"
  fi
  if is_legacy_php; then
    echo "PHP ${patch_version} will use the pinned OpenSSL ${legacy_openssl_source_version} compatibility toolchain."
    return 0
  fi
  if [[ "${openssl_version}" =~ ^1\.0\. ]]; then
    openssl_legacy_compatibility=true
    echo "Applying OpenSSL ${openssl_version} compatibility flags for PHP ${patch_version}."
  elif [[ "${patch_version}" == "7.0.33" && "${openssl_major_version}" == "3" ]]; then
    echo "Applying OpenSSL 3 compatibility flags for PHP ${patch_version}."
  fi
}

patch_openssl_legacy_sources() {
  if [[ "${openssl_legacy_compatibility}" == "true" ]] && ! is_legacy_php; then
    [[ -f "${source_dir}/ext/openssl/xp_ssl.c" ]] ||
      die_code BUILD_FAILED "PHP OpenSSL compatibility source is unavailable"
    # PHP 8.3's xp_ssl.c calls this OpenSSL 1.1 API directly. Limit the
    # compatibility change to that source file because openssl.c defines its
    # own helper with the same name on older OpenSSL versions.
    sed -i 's/ASN1_STRING_get0_data/ASN1_STRING_data/g' "${source_dir}/ext/openssl/xp_ssl.c"
    echo "Patched PHP OpenSSL source for OpenSSL 1.0.x compatibility."
  fi

  [[ "${patch_version}" == "7.0.33" && "${openssl_major_version}" == "3" && -z "${legacy_openssl_build_prefix}" ]] || return 0
  local openssl_source="${source_dir}/ext/openssl/openssl.c"
  [[ -f "${openssl_source}" ]] || die_code BUILD_FAILED "PHP 7.0 OpenSSL source is unavailable"
  if grep -Eq '^[[:space:]]*#define[[:space:]]+RSA_SSLV23_PADDING' "${openssl_source}"; then
    return 0
  fi
  grep -Fq 'RSA_SSLV23_PADDING' "${openssl_source}" || return 0
  local patched_source
  patched_source="$(mktemp "${openssl_source}.XXXXXX")"
  if ! awk '
    { print }
    ! inserted && $0 == "#include <openssl/rsa.h>" {
      print ""
      print "#ifndef RSA_SSLV23_PADDING"
      print "#define RSA_SSLV23_PADDING RSA_PKCS1_PADDING"
      print "#endif"
      inserted = 1
    }
    END { exit inserted ? 0 : 1 }
  ' "${openssl_source}" >"${patched_source}"; then
    rm -f -- "${patched_source}"
    die_code BUILD_FAILED "PHP 7.0 OpenSSL compatibility patch could not be applied"
  fi
  mv -f -- "${patched_source}" "${openssl_source}"
  echo "Patched PHP 7.0 source for OpenSSL 3 compatibility."
}

check_host() {
  [[ -r /etc/os-release ]] || die_code HOST_UNSUPPORTED "Linux release information is unavailable"
  # shellcheck disable=SC1091
  source /etc/os-release
  os_id="${ID:-}"; os_version="${VERSION_ID:-}"
  case "${os_id}" in
    ubuntu)
      case "${os_version}" in 22.04|24.04|26.04) ;; *) die_code HOST_UNSUPPORTED "Unsupported Ubuntu release" ;; esac
      ;;
    debian)
      case "${os_version}" in 11|12|13) ;; *) die_code HOST_UNSUPPORTED "Unsupported Debian release" ;; esac
      ;;
    rhel|rocky|almalinux|ol)
      case "${os_version%%.*}" in 8|9|10) ;; *) die_code HOST_UNSUPPORTED "Unsupported Enterprise Linux release" ;; esac
      ;;
    centos)
      case "${os_version%%.*}" in 7|8|9|10) ;; *) die_code HOST_UNSUPPORTED "Unsupported CentOS release" ;; esac
      ;;
    fedora)
      case "${os_version}" in 40|41|42|43|44) ;; *) die_code HOST_UNSUPPORTED "Unsupported Fedora release" ;; esac
      ;;
    opensuse-leap|opensuse)
      case "${os_version}" in 15.6|16.0) ;; *) die_code HOST_UNSUPPORTED "Unsupported openSUSE release" ;; esac
      ;;
    sles)
      case "${os_version%%.*}" in 15|16) ;; *) die_code HOST_UNSUPPORTED "Unsupported SLES release" ;; esac
      ;;
    amzn)
      [[ "${os_version}" == "2023" ]] || die_code HOST_UNSUPPORTED "Unsupported Amazon Linux release"
      ;;
    *) die_code HOST_UNSUPPORTED "Unsupported Linux distribution" ;;
  esac
  host_architecture="$(component_architecture)"
  if command -v apt-get >/dev/null 2>&1; then
    package_manager="apt"
  elif command -v dnf >/dev/null 2>&1; then
    package_manager="dnf"
  elif command -v yum >/dev/null 2>&1; then
    package_manager="yum"
  elif command -v zypper >/dev/null 2>&1; then
    package_manager="zypper"
  else
    die_code DEPENDENCY_MISSING "An APT, DNF, YUM, or Zypper package manager is required"
  fi
  if is_legacy_php && [[ "${host_architecture}" != "amd64" && "${host_architecture}" != "arm64" ]]; then
    die_code HOST_UNSUPPORTED "Legacy PHP-FPM releases are currently validated on amd64 and arm64 only"
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
  local source_dir source_parts source_list source_file target_file
  source_dir="$(mktemp -d)"
  trap 'rm -rf -- "${source_dir}"' EXIT
  source_list="${source_dir}/sources.list"
  source_parts="${source_dir}/sources.list.d"
  install -d -m 0755 -- "${source_parts}"
  if [[ -f /etc/apt/sources.list ]]; then
    copy_apt_source_without_docker /etc/apt/sources.list "${source_list}"
  else
    : >"${source_list}"
  fi
  for source_file in /etc/apt/sources.list.d/*.list /etc/apt/sources.list.d/*.sources; do
    [[ -f "${source_file}" ]] || continue
    target_file="${source_parts}/$(basename -- "${source_file}")"
    copy_apt_source_without_docker "${source_file}" "${target_file}"
  done
  apt-get update \
    -o "Dir::Etc::sourcelist=${source_list}" \
    -o "Dir::Etc::sourceparts=${source_parts}/"
)

apt_update_for_php() {
  local docker_sources=()
  mapfile -t docker_sources < <(docker_apt_sources)
  if ((${#docker_sources[@]} > 0)); then
    emit_progress 6 apt.repository.filtered "Detected an unrelated Docker APT source; using an isolated source list"
    apt_update_without_docker_source
  else
    apt-get update
  fi
}

rpm_build_repository_id() {
  local package_manager="$1" os_id="$2" os_major="$3"
  local repository_id=""
  [[ "${package_manager}" == "dnf" ]] || return 0
  case "${os_id}:${os_major}" in
    rocky:9|rocky:10|almalinux:9|almalinux:10|ol:9|ol:10|centos:9|centos:10)
      repository_id="crb"
      ;;
    rocky:8|almalinux:8|ol:8|centos:8)
      repository_id="powertools"
      ;;
  esac
  [[ -n "${repository_id}" ]] || return 0
  if "${package_manager}" repolist all 2>/dev/null |
    awk -v repository_id="${repository_id}" '$1 == repository_id { found = 1 } END { exit !found }'; then
    printf '%s\n' "${repository_id}"
  fi
}

install_optional_libzip_dependency() {
  local package_manager="$1" os_id="$2" os_major="$3"
  local package_name="libzip-devel" repository_id="" install_args=(-y)
  if [[ "${package_manager}" == "apt-get" ]]; then
    package_name="libzip-dev"
    install_args+=(--no-install-recommends)
  elif command -v rpm >/dev/null 2>&1 && rpm -q libzip-devel >/dev/null 2>&1; then
    return 0
  fi

  repository_id="$(rpm_build_repository_id "${package_manager}" "${os_id}" "${os_major}" || true)"
  [[ -z "${repository_id}" ]] || install_args+=("--enablerepo=${repository_id}")
  if "${package_manager}" install "${install_args[@]}" "${package_name}" >/dev/null 2>&1; then
    emit_progress 8 dependency.libzip.package "已安装系统 ${package_name} 依赖"
    return 0
  fi

  emit_progress 8 dependency.libzip.fallback "系统仓库未提供 ${package_name}，将自动使用源码构建兼容依赖"
}

install_dependencies() {
  if [[ "${install_mode}" == "offline" ]]; then
    install_dependencies_offline
    return 0
  fi
  if command -v apt-get >/dev/null 2>&1; then
    export DEBIAN_FRONTEND=noninteractive
    apt_update_for_php
    if is_legacy_php; then
      if ! apt-get install -y --no-install-recommends build-essential ca-certificates curl pkg-config perl xz-utils zlib1g-dev \
        libxml2-dev libssl-dev libcurl4-openssl-dev libjpeg-dev libpng-dev libfreetype6-dev \
        libsqlite3-dev libreadline-dev libxslt1-dev; then
        die_code DEPENDENCY_MISSING "PHP legacy 编译依赖安装失败，请检查系统软件源和网络"
      fi
    elif ! apt-get install -y --no-install-recommends build-essential ca-certificates curl pkg-config xz-utils zlib1g-dev \
      libxml2-dev libssl-dev libcurl4-openssl-dev libjpeg-dev libpng-dev libwebp-dev libfreetype6-dev \
      libonig-dev libsqlite3-dev libreadline-dev libsodium-dev libxslt1-dev libicu-dev libargon2-dev; then
      die_code DEPENDENCY_MISSING "PHP 编译依赖安装失败，请检查系统软件源和网络"
    fi
    is_legacy_php || install_optional_libzip_dependency apt-get "" ""
    return 0
  fi
  local package_manager os_id os_major
  if command -v dnf >/dev/null 2>&1; then
    package_manager=dnf
  elif command -v yum >/dev/null 2>&1; then
    package_manager=yum
  elif command -v zypper >/dev/null 2>&1; then
    package_manager=zypper
  else
    die_code DEPENDENCY_MISSING "An APT, DNF, YUM, or Zypper package manager is required"
  fi
  case "${package_manager}" in
    dnf|yum)
      os_id=""
      os_major=""
      if [[ -r /etc/os-release ]]; then
        # shellcheck disable=SC1091
        source /etc/os-release
        os_id="${ID:-}"
        os_major="${VERSION_ID%%.*}"
      fi
      local pkgconfig_package="pkgconf-pkg-config"
      local repository_id="" package_install_args=(-y)
      [[ "${package_manager}" == "yum" || "${os_id}" == "centos" && "${os_major}" == "7" ]] &&
        pkgconfig_package="pkgconfig"
      repository_id="$(rpm_build_repository_id "${package_manager}" "${os_id}" "${os_major}" || true)"
      [[ -z "${repository_id}" ]] || package_install_args+=("--enablerepo=${repository_id}")
      if is_legacy_php; then
        if ! "${package_manager}" install "${package_install_args[@]}" \
          gcc gcc-c++ make autoconf automake libtool ca-certificates curl "${pkgconfig_package}" perl xz tar gzip \
          libxml2-devel openssl-devel libcurl-devel libjpeg-turbo-devel libpng-devel \
          freetype-devel zlib-devel sqlite-devel readline-devel libxslt-devel; then
          die_code DEPENDENCY_MISSING "PHP legacy 编译依赖安装失败，请检查系统软件源和网络"
        fi
      elif ! "${package_manager}" install "${package_install_args[@]}" \
        gcc gcc-c++ make autoconf automake libtool ca-certificates curl "${pkgconfig_package}" xz tar gzip \
        libxml2-devel openssl-devel libcurl-devel libjpeg-turbo-devel libpng-devel libwebp-devel \
        freetype-devel oniguruma-devel zlib-devel sqlite-devel readline-devel libsodium-devel \
        libxslt-devel libicu-devel libargon2-devel; then
        die_code DEPENDENCY_MISSING "PHP 编译依赖安装失败，请检查系统软件源和网络"
      fi
      is_legacy_php || install_optional_libzip_dependency "${package_manager}" "${os_id}" "${os_major}"
      ;;
    zypper)
      if is_legacy_php; then
        if ! zypper --non-interactive install -y --no-recommends \
          gcc gcc-c++ make autoconf automake libtool ca-certificates curl pkg-config perl xz tar gzip \
          libxml2-devel libopenssl-devel libcurl-devel libjpeg8-devel libpng16-devel \
          freetype2-devel zlib-devel sqlite3-devel readline-devel libxslt-devel; then
          die_code DEPENDENCY_MISSING "PHP legacy 编译依赖安装失败，请检查系统软件源和网络"
        fi
      elif ! zypper --non-interactive install -y --no-recommends \
        gcc gcc-c++ make autoconf automake libtool ca-certificates curl pkg-config xz tar gzip \
        libxml2-devel libopenssl-devel libcurl-devel libjpeg8-devel libpng16-devel libwebp-devel \
        freetype2-devel libonig-devel zlib-devel sqlite3-devel readline-devel libsodium-devel \
        libxslt-devel libicu-devel libargon2-devel; then
        die_code DEPENDENCY_MISSING "PHP 编译依赖安装失败，请检查系统软件源和网络"
      fi
      is_legacy_php || install_optional_libzip_dependency "${package_manager}" "" ""
      ;;
  esac
}

offline_package_dir() {
  [[ -n "${offline_package_path}" && -n "${os_id}" && -n "${os_version}" && -n "${host_architecture}" ]] ||
    die_code PACKAGE_UNAVAILABLE "Offline PHP installation context is incomplete"
  printf '%s/packages/%s/%s/%s\n' "${offline_package_path}" "${os_id}" "${os_version}" "${host_architecture}"
}

validate_offline_bundle() {
  local package_dir="$(offline_package_dir)"
  [[ -d "${offline_package_path}" ]] || die_code PACKAGE_UNAVAILABLE "Offline PHP Bundle is unavailable"
  [[ -f "${offline_package_path}/manifest.yaml" ]] || die_code PACKAGE_INVALID "Offline PHP Bundle manifest is missing"
  [[ -f "${offline_package_path}/files.sha256" ]] || die_code PACKAGE_INVALID "Offline PHP Bundle checksum file is missing"
  [[ -d "${package_dir}" ]] || die_code PACKAGE_UNAVAILABLE "Offline PHP dependencies are missing for this host"
  [[ -f "${offline_package_path}/artifacts/${host_architecture}/${source_archive}" ]] ||
    die_code PACKAGE_UNAVAILABLE "Offline PHP ${software_version} source artifact is missing"
  if is_legacy_php; then
    [[ -f "${offline_package_path}/artifacts/common/${legacy_openssl_source_archive}" ]] ||
      die_code PACKAGE_UNAVAILABLE "Offline PHP legacy OpenSSL source artifact is missing"
  fi
  (cd "${offline_package_path}" && sha256sum -c files.sha256 --status) ||
    die_code PACKAGE_VERIFY_FAILED "Offline PHP Bundle checksum verification failed"
  grep -Eq '^[[:space:]]+id:[[:space:]]+php[[:space:]]*$' "${offline_package_path}/manifest.yaml" ||
    die_code PACKAGE_INVALID "Offline Bundle component is not PHP"
  grep -Eq '^[[:space:]]+version:[[:space:]]+1\\.0\\.15[[:space:]]*$' "${offline_package_path}/manifest.yaml" ||
    die_code PACKAGE_INVALID "Offline Bundle package version does not match PHP 1.0.15"
}

install_dependencies_offline() {
  local package_dir
  local -a packages=()
  package_dir="$(offline_package_dir)"
  mapfile -t packages < <(find "${package_dir}" -maxdepth 1 -type f \( -name '*.deb' -o -name '*.rpm' \) -print | sort)
  ((${#packages[@]} > 0)) || die_code PACKAGE_UNAVAILABLE "Offline PHP dependency packages are missing"
  case "${package_manager}" in
    apt)
      DEBIAN_FRONTEND=noninteractive dpkg -i "${packages[@]}" ||
        apt-get -y --no-download -f install
      ;;
    dnf|yum)
      "${package_manager}" --disablerepo='*' --cacheonly install -y "${packages[@]}"
      ;;
    zypper)
      zypper --non-interactive --no-refresh --no-gpg-checks install --allow-unsigned-rpm "${packages[@]}"
      ;;
    *) die_code DEPENDENCY_MISSING "Offline PHP package manager is unsupported" ;;
  esac
}

libzip_version_supported() {
  local version="$1"
  [[ -n "${version}" ]] || return 1
  case "${version}" in
    1.3.1*|1.7.0*) return 1 ;;
  esac
  awk -F. '{ major = $1 + 0; minor = $2 + 0; exit !((major >= 1) || (major == 0 && minor >= 11)) }' <<<"${version}"
}

download_libzip_source() {
  local cache_dir="/var/cache/oneinstack/downloads"
  local cache_file="${cache_dir}/libzip-${libzip_source_version}.tar.gz"
  local temporary_cache curl_args=()
  install -d -m 0750 -- "${cache_dir}"
  if [[ -f "${cache_file}" ]] &&
    printf '%s  %s\n' "${libzip_source_sha256}" "${cache_file}" | sha256sum --check --status; then
    cp -- "${cache_file}" "${work_dir}/libzip.tar.gz"
    return 0
  fi
  if [[ "${install_mode}" == "offline" ]]; then
    local offline_libzip="${offline_package_path}/artifacts/common/libzip-${libzip_source_version}.tar.gz"
    [[ -f "${offline_libzip}" ]] ||
      die_code PACKAGE_UNAVAILABLE "Offline PHP libzip source artifact is missing"
    printf '%s  %s\n' "${libzip_source_sha256}" "${offline_libzip}" | sha256sum --check --status ||
      die_code PACKAGE_VERIFY_FAILED "Offline PHP libzip source checksum did not match"
    cp -- "${offline_libzip}" "${work_dir}/libzip.tar.gz"
    return 0
  fi
  rm -f -- "${cache_file}"
  temporary_cache="$(mktemp "${cache_file}.tmp.XXXXXX")"
  curl_args=(--proto '=https' --tlsv1.2 --fail --location --retry 5 --retry-delay 5 --retry-max-time 300
    --connect-timeout 20 --max-time 900)
  if curl --help all 2>/dev/null | grep -q -- '--retry-all-errors'; then
    curl_args+=(--retry-all-errors)
  fi
  if ! curl "${curl_args[@]}" --output "${temporary_cache}" "${libzip_source_url}"; then
    rm -f -- "${temporary_cache}"
    die_code PACKAGE_UNAVAILABLE "The compatible libzip source could not be downloaded"
  fi
  printf '%s  %s\n' "${libzip_source_sha256}" "${temporary_cache}" | sha256sum --check --status || {
    rm -f -- "${temporary_cache}"
    die_code PACKAGE_VERIFY_FAILED "The compatible libzip source checksum did not match"
  }
  chmod 0640 "${temporary_cache}"
  mv -- "${temporary_cache}" "${cache_file}"
  cp -- "${cache_file}" "${work_dir}/libzip.tar.gz"
}

prepare_libzip_dependency() {
  local system_version
  system_version="$(pkg-config --modversion libzip 2>/dev/null || true)"
  libzip_version_supported "${system_version}" && return 0

  require_command make
  require_command tar
  download_libzip_source
  libzip_build_prefix="${work_dir}/libzip-prefix"
  tar -xzf "${work_dir}/libzip.tar.gz" -C "${work_dir}"
  [[ -d "${work_dir}/libzip-${libzip_source_version}" ]] ||
    die_code PACKAGE_INVALID "The compatible libzip source archive layout is invalid"
  if ! (
    cd "${work_dir}/libzip-${libzip_source_version}"
    ./configure --prefix="${libzip_build_prefix}" --disable-static --enable-shared
    make -j"$(nproc)"
    make install
  ); then
    die_code DEPENDENCY_MISSING "PHP ZIP 依赖不可用：未找到 libzip-devel，且源码 fallback 构建失败"
  fi
}

download_legacy_openssl_verified() {
  local destination="$1"
  local cache_dir="/var/cache/oneinstack/downloads"
  local cache_file="${cache_dir}/${legacy_openssl_source_archive%.tar.gz}-${host_architecture}-${legacy_openssl_source_sha256:0:12}.tar.gz"
  local offline_source temporary_cache download_url verify_failed=false
  local -a curl_args=()
  install -d -m 0750 -- "${cache_dir}"
  if [[ -f "${cache_file}" ]] &&
    printf '%s  %s\n' "${legacy_openssl_source_sha256}" "${cache_file}" | sha256sum --check --status; then
    cp -- "${cache_file}" "${destination}"
    return 0
  fi
  if [[ "${install_mode}" == "offline" ]]; then
    offline_source="${offline_package_path}/artifacts/common/${legacy_openssl_source_archive}"
    [[ -f "${offline_source}" ]] || die_code PACKAGE_UNAVAILABLE "Offline PHP legacy OpenSSL source artifact is missing"
    printf '%s  %s\n' "${legacy_openssl_source_sha256}" "${offline_source}" | sha256sum --check --status ||
      die_code PACKAGE_VERIFY_FAILED "Offline PHP legacy OpenSSL source checksum did not match"
    cp -- "${offline_source}" "${destination}"
    return 0
  fi
  for download_url in "${legacy_openssl_source_urls[@]}"; do
    temporary_cache="$(mktemp "${cache_file}.tmp.XXXXXX")"
    curl_args=(--proto '=https' --tlsv1.2 --fail --location --retry 5 --retry-delay 5 --retry-max-time 300
      --connect-timeout 20 --max-time 1800)
    if curl --help all 2>/dev/null | grep -q -- '--retry-all-errors'; then
      curl_args+=(--retry-all-errors)
    fi
    if curl "${curl_args[@]}" --output "${temporary_cache}" "${download_url}"; then
      if printf '%s  %s\n' "${legacy_openssl_source_sha256}" "${temporary_cache}" | sha256sum --check --status; then
        chmod 0640 "${temporary_cache}"
        mv -- "${temporary_cache}" "${cache_file}"
        cp -- "${cache_file}" "${destination}"
        return 0
      fi
      verify_failed=true
    fi
    rm -f -- "${temporary_cache}"
  done
  if [[ "${verify_failed}" == "true" ]]; then
    die_code PACKAGE_VERIFY_FAILED "The pinned PHP legacy OpenSSL source checksum did not match"
  fi
  die_code PACKAGE_UNAVAILABLE "The pinned PHP legacy OpenSSL source could not be downloaded"
}

prepare_legacy_openssl() {
  is_legacy_php || return 0
  local archive="${work_dir}/${legacy_openssl_source_archive}"
  local source_dir="${work_dir}/openssl-${legacy_openssl_source_version}"
  local openssl_cflags="${CFLAGS:-} -fPIC -fcommon"
  legacy_openssl_build_prefix="${work_dir}/openssl-prefix"
  download_legacy_openssl_verified "${archive}"
  tar -xzf "${archive}" -C "${work_dir}"
  [[ -d "${source_dir}" ]] || die_code PACKAGE_INVALID "Pinned PHP legacy OpenSSL archive layout is invalid"
  emit_progress 17 openssl.configure "正在编译 PHP legacy OpenSSL ${legacy_openssl_source_version}"
  if ! (
    cd "${source_dir}"
    CFLAGS="${openssl_cflags}" ./config --prefix="${legacy_openssl_build_prefix}" \
      --openssldir="${legacy_openssl_build_prefix}/ssl" no-shared no-dso no-ssl3 -fPIC
    make -j"$(nproc)" build_libs
    install -d -m 0755 -- "${legacy_openssl_build_prefix}/include/openssl" "${legacy_openssl_build_prefix}/lib"
    cp -aL -- include/openssl/. "${legacy_openssl_build_prefix}/include/openssl/"
    install -m 0644 -- libcrypto.a libssl.a "${legacy_openssl_build_prefix}/lib/"
  ); then
    die_code DEPENDENCY_MISSING "PHP legacy OpenSSL ${legacy_openssl_source_version} 构建失败"
  fi
  [[ -f "${legacy_openssl_build_prefix}/lib/libssl.a" &&
    -f "${legacy_openssl_build_prefix}/lib/libcrypto.a" ]] ||
    die_code DEPENDENCY_MISSING "PHP legacy OpenSSL static libraries are missing"
  export CPPFLAGS="-I${legacy_openssl_build_prefix}/include ${CPPFLAGS:-}"
  export LDFLAGS="-L${legacy_openssl_build_prefix}/lib ${LDFLAGS:-}"
  emit_progress 18 openssl.ready "PHP legacy OpenSSL compatibility toolchain is ready"
}

configure_php_build() {
  if is_legacy_php; then
    local openssl_option="--with-openssl"
    [[ -z "${legacy_openssl_build_prefix}" ]] || openssl_option="--with-openssl=${legacy_openssl_build_prefix}"
    ./configure --prefix="${install_dir}" --with-config-file-path="${install_dir}/lib" \
      --with-config-file-scan-dir="${install_dir}/etc/php.d" \
      --enable-fpm --with-fpm-user="${run_user}" --with-fpm-group="${run_group}" \
      "${openssl_option}" --with-zlib --with-curl \
      --with-mysqli=mysqlnd --with-pdo-mysql=mysqlnd --with-pdo-sqlite --with-sqlite3 \
      --enable-bcmath --enable-calendar --enable-exif --enable-ftp --enable-gd \
      --with-freetype-dir=/usr --with-jpeg-dir=/usr --with-png-dir=/usr \
      --enable-mbstring --enable-opcache --enable-pcntl --enable-soap --enable-sockets \
      --with-gettext --with-iconv --with-xsl --enable-zip
    return 0
  fi
  ./configure --prefix="${install_dir}" --with-config-file-path="${install_dir}/lib" \
    --with-config-file-scan-dir="${install_dir}/etc/php.d" \
    --enable-fpm --with-fpm-user="${run_user}" --with-fpm-group="${run_group}" \
    --with-openssl --with-zlib --with-curl --with-zip --with-sodium \
    --with-mysqli=mysqlnd --with-pdo-mysql=mysqlnd --with-pdo-sqlite --with-sqlite3 \
    --enable-bcmath --enable-calendar --enable-exif --enable-ftp --enable-gd \
    --with-freetype --with-jpeg --with-webp --enable-intl --enable-mbstring \
    --enable-opcache --enable-pcntl --enable-soap --enable-sockets \
    --with-gettext --with-iconv --with-xsl --with-password-argon2
}

ensure_account() {
  getent group "${run_group}" >/dev/null || groupadd --system "${run_group}"
  id "${run_user}" >/dev/null 2>&1 || useradd --system --gid "${run_group}" --home-dir /nonexistent --shell /usr/sbin/nologin "${run_user}"
}

managed_unit_matches() {
  [[ -f "${unit_file}" ]] && grep -Fq "ExecStart=${install_dir}/sbin/php-fpm" "${unit_file}"
}

managed_control_unit() {
  local candidate
  for candidate in "${unit_file}" /etc/systemd/system/php-fpm.service /etc/systemd/system/php-fpm-*.service; do
    [[ -f "${candidate}" ]] && grep -Fq "ExecStart=${install_dir}/sbin/php-fpm" "${candidate}" || continue
    basename -- "${candidate}"
    return 0
  done
  return 1
}

pid_is_managed() {
  local pid="$1" executable
  [[ "${pid}" =~ ^[0-9]+$ && -e "/proc/${pid}/exe" ]] || return 1
  executable="$(readlink -f -- "/proc/${pid}/exe" 2>/dev/null || true)"
  [[ "${executable}" == "$(readlink -f -- "${install_dir}/sbin/php-fpm")" ]]
}

managed_pid_running() {
  [[ -r "${pid_file}" ]] || return 1
  local pid
  pid="$(cat -- "${pid_file}")"
  pid_is_managed "${pid}"
}

managed_installation_present() {
  [[ -f "${state_dir}/managed" || -f "${state_dir}/version" || -f "${state_dir}/pending-version" ]] && return 0
  managed_unit_matches && return 0
  managed_pid_running && return 0
  return 1
}

external_service_candidates() {
  systemd_available || return 0
  systemctl list-units --type=service --state=active --no-legend \
    'php*-fpm.service' 'php-fpm-*.service' 2>/dev/null |
    awk '{print $1}' | sort -u |
    while IFS= read -r service; do
      [[ -z "${service}" || "${service}" == "${service_name}.service" ]] || printf '%s\n' "${service}"
    done
}

external_process_candidates() {
  command -v pgrep >/dev/null 2>&1 || return 0
  pgrep -x php-fpm 2>/dev/null || true
}

external_php_detected() {
  managed_installation_present && return 1
  if external_service_candidates | grep -q .; then
    return 0
  fi
  if external_process_candidates | grep -Eq '^[0-9]+$'; then
    return 0
  fi
  if systemd_available && [[ -f "${unit_file}" ]] && ! managed_unit_matches; then
    return 0
  fi
  [[ -d /etc/php ]] && return 0
  [[ -f /etc/php-fpm.conf || -d /etc/php.d ]] && return 0
  command -v php-fpm >/dev/null 2>&1 && return 0
  if command -v dpkg-query >/dev/null 2>&1 &&
    dpkg-query -W -f='${binary:Package}\n' 'php*-fpm' 2>/dev/null | grep -q .; then
    return 0
  fi
  command -v rpm >/dev/null 2>&1 && rpm -qa 'php*-fpm' 2>/dev/null | grep -q .
}

external_php_identity_present() {
  external_service_candidates | grep -q . && return 0
  external_process_candidates | grep -Eq '^[0-9]+$' && return 0
  if systemd_available && [[ -f "${unit_file}" ]] && ! managed_unit_matches; then
    return 0
  fi
  if command -v php-fpm >/dev/null 2>&1 && [[ -x "$(command -v php-fpm)" ]]; then
    return 0
  fi
  return 1
}

validate_external_policy() {
  [[ "${lifecycle_action}" == "install" || "${lifecycle_action}" == "upgrade" ]] || return 0
  external_php_detected || return 0
  external_php_identity_present ||
    die_code EXTERNAL_SERVICE_CONFLICT "PHP-FPM ownership could not be confirmed from service, process, or package metadata"
  if [[ "${migrate_external_php}" != "true" || "${migrate_external_confirm}" != "true" ]]; then
    die_code EXTERNAL_MIGRATION_REQUIRED "An external PHP-FPM installation requires explicit migration confirmation"
  fi
}

normalize_runtime_permissions() {
  ensure_account
  chmod 0755 "${install_dir}" "${install_dir}/bin" "${install_dir}/sbin"
  find "${install_dir}/etc" -type d -exec chmod 0755 {} +
  find "${install_dir}/etc" -type f -exec chmod 0640 {} +
  chown -R root:"${run_group}" "${install_dir}/etc"
  install -d -m 0750 -o "${run_user}" -g "${run_group}" -- "${log_dir}"
  emit_progress 58 permissions.runtime.applied "PHP-FPM runtime permissions applied"
}

verify_runtime_permissions() {
  require_command runuser
  require_command stat
  runuser -u "${run_user}" -- test -x "${install_dir}/bin/php" ||
    die_code RUNTIME_PERMISSION_FAILED "The PHP-FPM worker account cannot execute the managed PHP binary"
  [[ -S "${socket_path}" ]] || die_code SERVICE_NOT_READY "PHP-FPM Unix socket is unavailable"
  [[ "$(stat -c '%U:%G:%a' "${socket_path}")" == "${run_user}:${run_group}:660" ]] ||
    die_code RUNTIME_PERMISSION_FAILED "PHP-FPM socket ownership or mode is invalid"
  runuser -u "${run_user}" -- test -w "${log_dir}" ||
    die_code RUNTIME_PERMISSION_FAILED "The PHP-FPM worker account cannot write the log directory"
  emit_progress 78 permissions.runtime.verified "PHP-FPM runtime permissions verified"
}

download_verified() {
  local destination="$1"
  local architecture libc cache_dir cache_file download_url temporary_cache mirror_index=0 verify_failed=false
  local curl_args=()
  architecture="$(component_architecture)"
  libc="$(libc_flavor)"
  cache_dir="/var/cache/oneinstack/downloads"
  cache_file="${cache_dir}/php-${patch_version}-${architecture}-${libc}-${build_id}-${source_sha256:0:12}.${source_archive##*.}"
  install -d -m 0750 -- "${cache_dir}"
  if [[ -f "${cache_file}" ]] &&
    printf '%s  %s\n' "${source_sha256}" "${cache_file}" | sha256sum --check --status; then
    cp -- "${cache_file}" "${destination}"
    return 0
  fi
  if [[ "${install_mode}" == "offline" ]]; then
    local offline_source="${offline_package_path}/artifacts/${architecture}/${source_archive}"
    [[ -f "${offline_source}" ]] || die_code PACKAGE_UNAVAILABLE "Offline PHP source artifact is missing"
    printf '%s  %s\n' "${source_sha256}" "${offline_source}" | sha256sum --check --status ||
      die_code PACKAGE_VERIFY_FAILED "Offline PHP source checksum did not match"
    cp -- "${offline_source}" "${destination}"
    return 0
  fi
  for download_url in "${source_urls[@]}"; do
    ((mirror_index += 1))
    ((mirror_index == 1)) || emit_progress 22 download.retry "Retrying the same exact PHP release from a configured mirror"
    temporary_cache="$(mktemp "${cache_file}.tmp.XXXXXX")"
    curl_args=(--proto '=https' --tlsv1.2 --fail --location --retry 5 --retry-delay 5 --retry-max-time 300
      --connect-timeout 20 --max-time 1800)
    if curl --help all 2>/dev/null | grep -q -- '--retry-all-errors'; then
      curl_args+=(--retry-all-errors)
    fi
    if curl "${curl_args[@]}" --output "${temporary_cache}" "${download_url}"; then
      if printf '%s  %s\n' "${source_sha256}" "${temporary_cache}" | sha256sum --check --status; then
        chmod 0640 "${temporary_cache}"
        mv -- "${temporary_cache}" "${cache_file}"
        cp -- "${cache_file}" "${destination}"
        return 0
      fi
      verify_failed=true
    fi
    rm -f -- "${temporary_cache}"
  done
  if [[ "${verify_failed}" == "true" ]]; then
    die_code PACKAGE_VERIFY_FAILED "The exact PHP release checksum did not match"
  fi
  die_code PACKAGE_UNAVAILABLE "The exact PHP release could not be downloaded"
}

snapshot_external_php() {
  external_php_detected || return 0
  [[ "${migrate_external_php}" == "true" && "${migrate_external_confirm}" == "true" ]] ||
    die_code EXTERNAL_MIGRATION_REQUIRED "External PHP-FPM migration was not confirmed"
  install -d -m 0700 -- "${external_migration_dir}"
  if [[ -d /etc/php ]]; then
    cp -a -- /etc/php "${external_migration_dir}/config"
    find "${external_migration_dir}/config" -type f \( -name '*.ini' -o -name '*.conf' \) -print \
      >"${external_migration_dir}/config-files" 2>/dev/null || true
    grep -hE '^[[:space:]]*(error_log|slowlog|listen|session.save_path|upload_tmp_dir|sys_temp_dir)[[:space:]]*=' \
      "${external_migration_dir}/config"/*/*/*.conf \
      "${external_migration_dir}/config"/*/*/*.ini \
      "${external_migration_dir}/config"/*/*.ini 2>/dev/null \
      >"${external_migration_dir}/runtime-paths" || true
  fi
  if command -v dpkg-query >/dev/null 2>&1; then
    dpkg-query -W -f='${binary:Package}\t${Version}\t${Status}\n' 'php*-fpm' 'php*-cli' 'php*-common' 2>/dev/null |
      awk '$3 == "install" && $4 == "ok" && $5 == "installed" {print $1 "\t" $2}' \
      >"${external_migration_dir}/package-versions" || true
  elif command -v rpm >/dev/null 2>&1; then
    rpm -qa --qf='%{NAME}\t%{VERSION}-%{RELEASE}\n' 'php*' 2>/dev/null |
      awk '$1 ~ /^php.*-(fpm|cli|common)$/ {print}' >"${external_migration_dir}/package-versions" || true
  else
    : >"${external_migration_dir}/package-versions"
  fi
  external_service_candidates >"${external_migration_dir}/active-services" || true
  external_process_candidates >"${external_migration_dir}/pids" || true
  cp -a -- "${external_migration_dir}/active-services" "${external_migration_dir}/identified-services"
  if systemd_available && [[ -f "${unit_file}" ]] && ! managed_unit_matches; then
    grep -Fxq "${service_name}.service" "${external_migration_dir}/identified-services" 2>/dev/null ||
      printf '%s\n' "${service_name}.service" >>"${external_migration_dir}/identified-services"
  fi
  while IFS= read -r service; do
    [[ -z "${service}" ]] && continue
    systemctl is-enabled "${service}" >"${external_migration_dir}/enabled-${service}" 2>/dev/null || true
    systemctl cat "${service}" >"${external_migration_dir}/${service}" 2>/dev/null || true
    systemctl show "${service}" \
      --property=MainPID --property=ExecStart --property=FragmentPath \
      --property=ActiveState --property=SubState --property=UnitFileState \
      >"${external_migration_dir}/inventory-${service}" 2>/dev/null || true
    main_pid="$(systemctl show "${service}" --property=MainPID --value 2>/dev/null || true)"
    if [[ "${main_pid}" =~ ^[0-9]+$ && "${main_pid}" != "0" && -e "/proc/${main_pid}/exe" ]]; then
      executable="$(readlink -f -- "/proc/${main_pid}/exe" 2>/dev/null || true)"
      printf '%s\n' "${executable}" >"${external_migration_dir}/exe-${service}"
      tr '\0' ' ' <"/proc/${main_pid}/cmdline" >"${external_migration_dir}/cmdline-${service}" 2>/dev/null || true
      if [[ -x "${executable}" ]]; then
        "${executable}" -v 2>/dev/null | sed -n '1p' | cut -c1-256 >"${external_migration_dir}/runtime-version-${service}" || true
      fi
    fi
  done <"${external_migration_dir}/identified-services"
  while IFS= read -r pid; do
    [[ "${pid}" =~ ^[0-9]+$ && -e "/proc/${pid}/exe" ]] || continue
    executable="$(readlink -f -- "/proc/${pid}/exe" 2>/dev/null || true)"
    printf '%s\n' "${executable}" >"${external_migration_dir}/exe-pid-${pid}"
    tr '\0' ' ' <"/proc/${pid}/cmdline" >"${external_migration_dir}/cmdline-pid-${pid}" 2>/dev/null || true
    if [[ -x "${executable}" ]]; then
      "${executable}" -v 2>/dev/null | sed -n '1p' | cut -c1-256 >"${external_migration_dir}/runtime-version-pid-${pid}" || true
    fi
  done <"${external_migration_dir}/pids"
  for socket in /run/php/php*-fpm.sock /var/run/php/php*-fpm.sock; do
    [[ -S "${socket}" ]] || continue
    stat -c '%n %U:%G %a' -- "${socket}" >>"${external_migration_dir}/socket-owners" 2>/dev/null || true
  done
  : >"${external_migration_dir}/detected"
  emit_progress 25 migration.snapshot.created "External PHP-FPM inventory captured"
}

stop_external_php() {
  [[ -f "${external_migration_dir}/detected" ]] || return 0
  if systemd_available; then
    while IFS= read -r service; do
      [[ -z "${service}" ]] || systemctl stop "${service}"
    done <"${external_migration_dir}/identified-services"
    return 0
  fi
  while IFS= read -r pid; do
    [[ "${pid}" =~ ^[0-9]+$ ]] || continue
    executable="$(readlink -f -- "/proc/${pid}/exe" 2>/dev/null || true)"
    snapshot_executable="$(cat -- "${external_migration_dir}/exe-pid-${pid}" 2>/dev/null || true)"
    [[ -n "${executable}" && "${executable}" == "${snapshot_executable}" ]] || continue
    [[ "${executable}" != "$(readlink -f -- "${install_dir}/sbin/php-fpm")" ]] || continue
    kill -TERM "${pid}" 2>/dev/null || true
  done <"${external_migration_dir}/pids"
}

migrate_external_php_config() {
  [[ -d "${external_migration_dir}/config" ]] || return 0
  local migrated="${install_dir}/etc/php.d/90-migrated.ini"
  find "${external_migration_dir}/config" -type f -name '*.ini' -print0 |
    xargs -0 -r awk '
      /^[[:space:]]*[;#]/ {next}
      /^[[:space:]]*(extension|zend_extension|error_log|session.save_path|upload_tmp_dir)[[:space:]]*=/ {next}
      /^[[:space:]]*[A-Za-z0-9_.-]+[[:space:]]*=/ {print}
    ' >"${migrated}"
  chmod 0640 "${migrated}"
  chown root:"${run_group}" "${migrated}"
  emit_progress 55 migration.config.copied "Compatible external PHP settings migrated"
}

commit_external_php() {
  [[ -f "${external_migration_dir}/detected" ]] || return 0
  systemd_available || return 0
  while IFS= read -r service; do
    [[ -z "${service}" ]] || systemctl disable "${service}" 2>/dev/null || true
  done <"${external_migration_dir}/active-services"
  emit_progress 88 migration.commit.completed "External PHP-FPM ownership transition committed"
}

safe_remove_component_path() {
  local target="$1"
  [[ -n "${target}" && "${target}" == "${state_dir}"/* && "${target}" != "${state_dir}" ]] ||
    die_code ROLLBACK_FAILED "Refusing to remove a path outside the component state directory"
  rm -rf -- "${target}"
}

stop_managed_service() {
  if systemd_available; then
    local control_unit
    control_unit="$(managed_control_unit || true)"
    if [[ -n "${control_unit}" ]]; then
      systemctl stop "${control_unit}" 2>/dev/null || true
      systemctl disable "${control_unit}" 2>/dev/null || true
      return 0
    fi
  fi
  if managed_pid_running; then
    local pid waited=0
    pid="$(cat -- "${pid_file}")"
    kill -TERM "${pid}" 2>/dev/null || true
    while pid_is_managed "${pid}" && ((waited < 50)); do
      sleep 0.1
      ((waited += 1))
    done
    if pid_is_managed "${pid}"; then
      kill -KILL "${pid}" 2>/dev/null || true
    fi
    rm -f -- "${pid_file}"
  fi
}

start_managed_service() {
  install -d -m 0750 -o "${run_user}" -g "${run_group}" -- "${log_dir}"
  if systemd_available && managed_unit_matches; then
    systemctl daemon-reload
    systemctl enable --now "${service_name}.service"
    return 0
  fi
  [[ -x "${install_dir}/sbin/php-fpm" ]] || die_code SERVICE_START_FAILED "Managed PHP-FPM binary is unavailable"
  nohup "${install_dir}/sbin/php-fpm" --nodaemonize --fpm-config "${fpm_config_file}" >>"${log_file}" 2>&1 &
  printf '%s\n' "$!" >"${pid_file}"
}

service_is_active() {
  if systemd_available; then
    local control_unit
    control_unit="$(managed_control_unit || true)"
    [[ -n "${control_unit}" ]] && systemctl is-active --quiet "${control_unit}"
    return
  else
    managed_pid_running && [[ -S "${socket_path}" ]]
  fi
}

service_is_enabled() {
  systemd_available || return 1
  local control_unit
  control_unit="$(managed_control_unit || true)"
  [[ -n "${control_unit}" ]] && systemctl is-enabled --quiet "${control_unit}"
}

wait_for_socket() {
  local attempt=0
  while ((attempt < 50)); do
    [[ -S "${socket_path}" ]] && return 0
    sleep 0.1
    ((attempt += 1))
  done
  return 1
}

write_runtime_parameters() {
  local target="$1"
  install -d -m 0750 -- "${state_dir}"
  {
    printf 'install-dir=%s\n' "${install_dir}"
    printf 'log-dir=%s\n' "${log_dir}"
    printf 'socket-path=%s\n' "${socket_path}"
    printf 'run-user=%s\n' "${run_user}"
    printf 'run-group=%s\n' "${run_group}"
    printf 'service-name=%s\n' "${service_name}"
    printf 'component-state-dir=%s\n' "${state_root}"
  } >"${target}"
  chmod 0600 "${target}"
}

prepare_rollback() {
  install -d -m 0750 -- "${state_dir}"
  if [[ -e "${install_dir}" ]] && ! managed_installation_present; then
    die_code INSTALL_PATH_CONFLICT "The requested PHP installation directory is not owned by Oneinstack"
  fi
  safe_remove_component_path "${rollback_dir}"
  install -d -m 0750 -- "${rollback_dir}"
  if [[ -f "${state_dir}/runtime-params" ]]; then
    cp -a -- "${state_dir}/runtime-params" "${rollback_dir}/runtime-params"
  else
    write_runtime_parameters "${rollback_dir}/runtime-params"
  fi
  if service_is_active; then
    : >"${rollback_dir}/was-active"
    stop_managed_service
  fi
  if service_is_enabled; then : >"${rollback_dir}/was-enabled"; fi
  [[ ! -e "${install_dir}" ]] || mv -- "${install_dir}" "${rollback_dir}/install"
  current_unit="$(managed_control_unit || true)"
  if [[ -n "${current_unit}" ]]; then
    printf '%s\n' "${current_unit}" >"${rollback_dir}/unit-name"
    cp -a -- "/etc/systemd/system/${current_unit}" "${rollback_dir}/unit"
    rm -f -- "/etc/systemd/system/${current_unit}"
    systemd_available && systemctl daemon-reload || true
  fi
  snapshot_external_php
  stop_external_php
}

restore_rollback() {
  [[ -d "${rollback_dir}" && -d "${rollback_dir}/install" ]] ||
    die_code ROLLBACK_FAILED "A valid PHP rollback snapshot is unavailable"
  stop_managed_service
  if [[ -e "${install_dir}" ]]; then
    managed_installation_present || die_code ROLLBACK_FAILED "The current PHP installation is not owned by Oneinstack"
    rm -rf -- "${install_dir}"
  fi
  mv -- "${rollback_dir}/install" "${install_dir}"
  current_unit="$(managed_control_unit || true)"
  if [[ -n "${current_unit}" ]]; then
    rm -f -- "/etc/systemd/system/${current_unit}"
  fi
  if [[ -f "${rollback_dir}/unit-name" && -f "${rollback_dir}/unit" ]]; then
    previous_unit="$(cat -- "${rollback_dir}/unit-name")"
    [[ "${previous_unit}" =~ ^php-fpm(-[0-9]+\.[0-9]+)?\.service$ ]] ||
      die_code ROLLBACK_FAILED "The previous PHP-FPM unit identity is invalid"
    install -D -m 0644 -- "${rollback_dir}/unit" "/etc/systemd/system/${previous_unit}"
    service_name="${previous_unit%.service}"
    unit_file="/etc/systemd/system/${previous_unit}"
    if [[ -f "${rollback_dir}/runtime-params" ]]; then
      previous_socket="$(sed -nE 's/^socket-path=(.*)$/\1/p' "${rollback_dir}/runtime-params" | head -n1)"
      [[ -z "${previous_socket}" ]] || socket_path="${previous_socket}"
    fi
  fi
  [[ ! -f "${rollback_dir}/runtime-params" ]] || cp -a -- "${rollback_dir}/runtime-params" "${state_dir}/runtime-params"
  if systemd_available; then systemctl daemon-reload; fi
  if [[ -f "${rollback_dir}/was-enabled" ]] && systemd_available; then
    systemctl enable "${service_name}.service" 2>/dev/null || true
  fi
  if [[ -f "${rollback_dir}/was-active" ]]; then
    start_managed_service
    wait_for_socket || die_code ROLLBACK_FAILED "The previous PHP-FPM service did not become ready after rollback"
  fi
  if [[ -f "${external_migration_dir}/active-services" ]] && systemd_available; then
    while IFS= read -r service; do
      [[ -z "${service}" ]] && continue
      if grep -Fxq enabled "${external_migration_dir}/enabled-${service}" 2>/dev/null; then
        systemctl enable "${service}" 2>/dev/null || true
      else
        systemctl disable "${service}" 2>/dev/null || true
      fi
      grep -Fxq "${service}" "${external_migration_dir}/active-services" 2>/dev/null || continue
      systemctl start "${service}" 2>/dev/null || true
    done <"${external_migration_dir}/identified-services"
  fi
}
