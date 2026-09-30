#!/usr/bin/env bash
set -Eeuo pipefail
umask 027

service_name=webdav
package_version=1.0.6
software_version="${SOFTWARE_VERSION:-5.16.0}"
port="${WEBDAV_PORT:-5005}"
bind_address="${WEBDAV_BIND_ADDRESS:-127.0.0.1}"
username="${WEBDAV_USERNAME:-webdav_admin}"
password="${WEBDAV_PASSWORD:-}"
install_dir="${INSTALL_DIR:-/opt/oneinstack/webdav}"
data_dir="${DATA_DIR:-/var/lib/webdav}"
state_root="${ONEINSTACK_COMPONENT_STATE:-/var/lib/oneinstack/components}"
# shellcheck disable=SC2034
state_dir="${state_root}/webdav"
# shellcheck disable=SC2034
rollback_dir="${state_dir}/rollback"
config_dir=/etc/oneinstack/webdav
config_file="${config_dir}/config.yaml"
unit_file=/etc/systemd/system/webdav.service
install_mode="${ONEINSTACK_INSTALL_MODE:-center}"
offline_path="${ONEINSTACK_OFFLINE_PACKAGE_PATH:-}"
offline_id="${ONEINSTACK_OFFLINE_BUNDLE_ID:-}"
offline_digest="${ONEINSTACK_OFFLINE_BUNDLE_DIGEST:-}"
prefix=/
permissions=CRUD
behind_proxy=false
tls=false
tls_cert=
tls_key=
host_id=
host_version=
host_arch=
package_manager=

die() { printf 'ERROR: %s\n' "$*" >&2; exit 1; }
emit_progress() {
  [[ "${ONEINSTACK_PROGRESS_FD:-}" =~ ^[0-9]+$ ]] || return 0
  printf '{"type":"progress","percent":%s,"code":"%s","message":"%s"}\n' "$1" "$2" "$3" >&"${ONEINSTACK_PROGRESS_FD}" || true
}
require_root() { [[ "$(id -u)" -eq 0 ]] || die 'ROOT_REQUIRED: root privileges are required.'; }
require_cmd() { command -v "$1" >/dev/null 2>&1 || die "DEPENDENCY_MISSING: $1 is required."; }
validate_path() {
  [[ "$1" == /* && "$(realpath -m -- "$1")" == "$1" && "$1" =~ ^/[A-Za-z0-9_./-]+$ ]] ||
    die "INVALID_PATH: $2 must be a normalized absolute path without spaces."
  case "$1" in /|/usr|/var|/etc|/data|/home|/root|/tmp|/opt|/var/lib|/etc/oneinstack) die "INVALID_PATH: $2 is too broad." ;; esac
}
path_overlaps() { [[ "$1" == "$2" || "$1" == "$2/"* || "$2" == "$1/"* ]]; }
validate_port() { [[ "$1" =~ ^[0-9]+$ && "$1" -ge 1 && "$1" -le 65535 ]] || die 'INVALID_PORT: WebDAV port must be 1-65535.'; }
validate_address() {
  [[ "$1" =~ ^[0-9a-fA-F:.]+$ && "$1" != *..* ]] || die 'INVALID_BIND_ADDRESS: use a numeric IPv4 or IPv6 address.'
}
validate_username() {
  [[ "$1" =~ ^[A-Za-z_][A-Za-z0-9._-]{0,63}$ ]] || die 'INVALID_USERNAME: WebDAV username is invalid.'
}
validate_password() {
  [[ "${#1}" -ge 12 && "${#1}" -le 72 && "$1" =~ ^[A-Za-z0-9._@%+=!#?-]+$ ]] ||
    die 'INVALID_PASSWORD: use 12-72 letters, digits, or ._@%+=!#?- characters.'
}
validate_config_values() {
  validate_port "$port"; validate_address "$bind_address"
  [[ "$prefix" == / || "$prefix" =~ ^/[A-Za-z0-9/_-]+$ ]] ||
    die 'INVALID_PREFIX: prefix must be an absolute URL path.'
  [[ "$permissions" =~ ^[CRUD]{1,4}$ ]] || die 'INVALID_PERMISSIONS: use C, R, U, D combinations.'
  [[ "$behind_proxy" == true || "$behind_proxy" == false ]] || die 'INVALID_PROXY_SETTING: expected true or false.'
  [[ "$tls" == true || "$tls" == false ]] || die 'INVALID_TLS_SETTING: expected true or false.'
  if [[ "$tls" == true ]]; then
    validate_path "$tls_cert" TLS_CERT; validate_path "$tls_key" TLS_KEY
    [[ -f "$tls_cert" && -f "$tls_key" && ! -L "$tls_cert" && ! -L "$tls_key" ]] ||
      die 'TLS_FILE_MISSING: certificate and private key must be regular files.'
    require_cmd openssl
    openssl x509 -in "$tls_cert" -noout >/dev/null 2>&1 || die 'TLS_CERT_INVALID: invalid certificate.'
    openssl pkey -in "$tls_key" -noout >/dev/null 2>&1 || die 'TLS_KEY_INVALID: invalid private key.'
  elif [[ "$bind_address" != 127.0.0.1 && "$bind_address" != ::1 ]]; then
    die 'TLS_REQUIRED: non-loopback binding requires TLS.'
  fi
}
validate_common() {
  [[ "$software_version" == 5.16.0 ]] || die 'VERSION_UNSUPPORTED: only WebDAV 5.16.0 is supported.'
  validate_path "$install_dir" INSTALL_DIR; validate_path "$data_dir" DATA_DIR; validate_path "$state_root" ONEINSTACK_COMPONENT_STATE
  if path_overlaps "$install_dir" "$data_dir" || path_overlaps "$install_dir" "$state_root" ||
     path_overlaps "$data_dir" "$state_root" || path_overlaps "$data_dir" "$config_dir" ||
     path_overlaps "$install_dir" "$config_dir" || path_overlaps "$state_root" "$config_dir"; then
    die 'PATH_CONFLICT: managed paths overlap.'
  fi
  validate_username "$username"
  validate_config_values
}
validate_install() {
  validate_common
  validate_password "$password"
  [[ "$install_mode" == center || "$install_mode" == offline ]] || die 'INSTALL_MODE_INVALID: expected center or offline.'
  if [[ "$install_mode" == offline ]]; then
    validate_path "$offline_path" ONEINSTACK_OFFLINE_PACKAGE_PATH
    [[ "$offline_digest" =~ ^[0-9a-f]{64}$ && "$offline_id" == "sha256:$offline_digest" ]] ||
      die 'OFFLINE_BUNDLE_IDENTITY_MISMATCH: Bundle identity is invalid.'
  elif [[ -n "$offline_path$offline_id$offline_digest" ]]; then
    die 'INSTALL_MODE_INVALID: offline metadata in center mode.'
  fi
}
detect_host() {
  [[ -r /etc/os-release ]] || die 'HOST_UNSUPPORTED: /etc/os-release is unavailable.'
  # shellcheck disable=SC1091
  source /etc/os-release
  host_id="${ID,,}"; host_version="${VERSION_ID:-}"
  case "$(uname -m)" in x86_64) host_arch=amd64 ;; aarch64|arm64) host_arch=arm64 ;; *) die 'HOST_UNSUPPORTED: architecture is not amd64 or arm64.' ;; esac
  if [[ "$host_id" == centos ]] && grep -Eiq 'CentOS[[:space:]]+Stream' /etc/os-release; then
    host_id=centos-stream
  fi
  case "$host_id:$host_version" in
    ubuntu:22.04|ubuntu:24.04|ubuntu:26.04|ubuntu:20.04|debian:11|debian:12|debian:13) package_manager=apt ;;
    rhel:8*|rhel:9*|rhel:10*|rocky:8*|rocky:9*|rocky:10*|almalinux:8*|almalinux:9*|almalinux:10*|ol:8*|ol:9*|ol:10*|centos-stream:8*|centos-stream:9*|centos-stream:10*|amzn:2023) package_manager=dnf ;;
    centos:7*) package_manager=yum ;;
    fedora:[0-9]*) package_manager=dnf ;;
    sles:15*|opensuse-leap:15*|opensuse-leap:16*|opensuse-tumbleweed:[0-9]*) package_manager=zypper ;;
    *) die "HOST_UNSUPPORTED: $host_id $host_version has no verified WebDAV package-manager branch." ;;
  esac
  case "$package_manager" in
    apt) require_cmd apt-get ;;
    dnf) require_cmd dnf ;;
    yum) require_cmd yum ;;
    zypper) require_cmd zypper; require_cmd rpm ;;
  esac
  require_cmd systemctl; require_cmd tar; require_cmd sha256sum; require_cmd realpath
}
source_sha() {
  case "$host_arch" in
    amd64) printf '%s' f632f359335a78f2d99b491250da54150281c27386cd4352d667ed6c30efb58b ;;
    arm64) printf '%s' 05a84c466001a179c9abb457374122480c5367b8d2b3bf0892d98bccc5b32ad2 ;;
  esac
}
archive_name() { printf 'linux-%s-webdav.tar.gz' "$host_arch"; }
verify_sha() {
  local actual
  actual="$(sha256sum "$1" | awk '{print $1}')"
  [[ "$actual" == "$2" ]] || die 'SOURCE_CHECKSUM_MISMATCH: WebDAV release digest differs from the pinned value.'
}
validate_bundle() {
  [[ "$install_mode" == offline ]] || return 0
  [[ -f "$offline_path/bundle-info" && -f "$offline_path/files.sha256" && ! -L "$offline_path" ]] ||
    die 'OFFLINE_BUNDLE_INVALID: metadata is missing.'
  ! find "$offline_path" \( -type l -o \( ! -type f ! -type d \) \) -print -quit | grep -q . ||
    die 'OFFLINE_BUNDLE_INVALID: links and special files are forbidden.'
  local listed actual
  listed="$(awk '{print $2}' "$offline_path/files.sha256" | sort)"
  actual="$(cd "$offline_path" && find . -type f ! -name files.sha256 -print | sed 's#^./##' | sort)"
  [[ "$listed" == "$actual" ]] || die 'OFFLINE_BUNDLE_INVALID: file inventory differs from checksum list.'
  while IFS= read -r entry; do
    [[ "$entry" =~ ^[A-Za-z0-9._/-]+$ && "$entry" != ../* && "$entry" != */../* && "$entry" != */.. ]] ||
      die 'OFFLINE_BUNDLE_INVALID: unsafe file path.'
  done <<<"$listed"
  (cd "$offline_path" && sha256sum --check --strict files.sha256) >/dev/null ||
    die 'OFFLINE_BUNDLE_CHECKSUM_MISMATCH: Bundle files did not verify.'
  local expected
  expected="$(printf 'component=webdav\npackageVersion=%s\nsoftwareVersion=%s\nosId=%s\nosVersion=%s\narchitecture=%s\n' \
    "$package_version" "$software_version" "$host_id" "$host_version" "$host_arch")"
  [[ "$(cat "$offline_path/bundle-info")" == "$expected" ]] ||
    die 'OFFLINE_BUNDLE_PLATFORM_MISMATCH: Bundle identity does not match this host.'
  [[ -f "$offline_path/artifacts/$host_arch/$(archive_name)" ]] ||
    die 'OFFLINE_SOURCE_MISSING: matching WebDAV binary archive is missing.'
  verify_sha "$offline_path/artifacts/$host_arch/$(archive_name)" "$(source_sha)"
  validate_offline_dependencies
}
validate_offline_dependencies() {
  local package_root="$offline_path/packages/$host_id/$host_version/$host_arch"
  local extension package expected_arch name arch
  local -a packages required
  [[ "$package_manager" == apt ]] && extension=deb || extension=rpm
  [[ -d "$package_root" ]] || die 'OFFLINE_DEPENDENCY_MISSING: matching package directory is missing.'
  shopt -s nullglob
  packages=("$package_root"/*."$extension")
  (("${#packages[@]}" > 0)) || die 'OFFLINE_DEPENDENCY_MISSING: dependency packages are absent.'
  if [[ "$package_manager" == apt ]]; then
    required=(acl apache2-utils ca-certificates curl iproute2 openssl tar)
    expected_arch="$host_arch"
  elif [[ "$package_manager" == zypper ]]; then
    required=(acl apache2-utils ca-certificates curl iproute2 openssl tar)
    [[ "$host_arch" == amd64 ]] && expected_arch=x86_64 || expected_arch=aarch64
  else
    required=(acl httpd-tools ca-certificates curl iproute openssl tar)
    [[ "$host_arch" == amd64 ]] && expected_arch=x86_64 || expected_arch=aarch64
  fi
  local -A present=()
  for package in "${packages[@]}"; do
    if [[ "$extension" == deb ]]; then
      name="$(dpkg-deb -f "$package" Package 2>/dev/null)" ||
        die 'OFFLINE_BUNDLE_INVALID: unreadable DEB metadata.'
      arch="$(dpkg-deb -f "$package" Architecture 2>/dev/null)" ||
        die 'OFFLINE_BUNDLE_INVALID: unreadable DEB architecture.'
      [[ "$arch" == "$expected_arch" || "$arch" == all ]] ||
        die 'OFFLINE_BUNDLE_PLATFORM_MISMATCH: DEB architecture differs from host.'
    else
      name="$(rpm -qp --qf '%{NAME}' "$package" 2>/dev/null)" ||
        die 'OFFLINE_BUNDLE_INVALID: unreadable RPM metadata.'
      arch="$(rpm -qp --qf '%{ARCH}' "$package" 2>/dev/null)" ||
        die 'OFFLINE_BUNDLE_INVALID: unreadable RPM architecture.'
      [[ "$arch" == "$expected_arch" || "$arch" == noarch ]] ||
        die 'OFFLINE_BUNDLE_PLATFORM_MISMATCH: RPM architecture differs from host.'
    fi
    present["$name"]=1
  done
  for name in "${required[@]}"; do
    [[ -n "${present[$name]:-}" ]] || die "OFFLINE_DEPENDENCY_MISSING: $name is absent from Bundle."
  done
}
install_dependencies() {
  if [[ "$install_mode" == offline ]]; then
    local package_root="$offline_path/packages/$host_id/$host_version/$host_arch"
    [[ -d "$package_root" ]] || die 'OFFLINE_DEPENDENCY_MISSING: platform package directory is missing.'
    if [[ "$package_manager" == apt ]]; then
      shopt -s nullglob
      local packages=("$package_root"/*.deb)
      (("${#packages[@]}" > 0)) || die 'OFFLINE_DEPENDENCY_MISSING: no DEB dependencies were supplied.'
      DEBIAN_FRONTEND=noninteractive apt-get -y --no-download --no-install-recommends install "${packages[@]}" ||
        die 'OFFLINE_DEPENDENCY_MISSING: local DEB dependency closure is incomplete.'
    else
      shopt -s nullglob
      local packages=("$package_root"/*.rpm)
      (("${#packages[@]}" > 0)) || die 'OFFLINE_DEPENDENCY_MISSING: no RPM dependencies were supplied.'
      local package_name missing=false
      for package in "${packages[@]}"; do
        package_name="$(rpm -qp --qf '%{NAME}' "$package")" ||
          die 'OFFLINE_BUNDLE_INVALID: unreadable RPM metadata.'
        if ! rpm -q --quiet "$package_name"; then missing=true; break; fi
      done
      if [[ "$missing" == true ]]; then
        case "$package_manager" in
          dnf) dnf -y --disablerepo='*' install "${packages[@]}" ;;
          yum) yum -y --disablerepo='*' install "${packages[@]}" ;;
          zypper) rpm -Uvh --replacepkgs "${packages[@]}" ;;
        esac || die 'OFFLINE_DEPENDENCY_MISSING: local RPM dependency closure is incomplete.'
      fi
    fi
  elif [[ "$package_manager" == apt ]]; then
    apt-get update
    DEBIAN_FRONTEND=noninteractive apt-get -y --no-install-recommends install acl apache2-utils ca-certificates curl iproute2 openssl tar
  elif [[ "$package_manager" == dnf ]]; then
    dnf -y install acl httpd-tools ca-certificates curl iproute openssl tar
  elif [[ "$package_manager" == yum ]]; then
    yum -y install acl httpd-tools ca-certificates curl iproute openssl tar
  else
    zypper --non-interactive install acl apache2-utils ca-certificates curl iproute2 openssl tar
  fi
  for command in curl htpasswd ss getfacl setfacl runuser; do require_cmd "$command"; done
}
release_archive() {
  if [[ "$install_mode" == offline ]]; then
    printf '%s' "$offline_path/artifacts/$host_arch/$(archive_name)"
    return
  fi
  local target="$state_root/downloads/webdav-5.16.0-$host_arch.tar.gz"
  install -d -m 0700 -- "$state_root/downloads"
  if [[ ! -f "$target" ]]; then
    local -a curl_options=(--proto '=https' --fail --location --retry 3 --connect-timeout 20)
    # CentOS 7's curl 7.29 can negotiate TLS 1.2, but lacks the --tlsv1.2 switch.
    if curl --help 2>&1 | grep -F -- '--tlsv1.2' >/dev/null; then
      curl_options+=(--tlsv1.2)
    fi
    curl "${curl_options[@]}" \
      "https://github.com/hacdias/webdav/releases/download/v5.16.0/$(archive_name)" -o "$target.part"
    mv -- "$target.part" "$target"
  fi
  verify_sha "$target" "$(source_sha)"
  printf '%s' "$target"
}
port_listening() {
  if command -v ss >/dev/null 2>&1; then
    ss -ltn | awk -v port="$1" 'NR > 1 {n=split($4,a,":"); if (a[n] == port) found=1} END {exit !found}'
  else
    local -a sockets=()
    [[ -r /proc/net/tcp ]] && sockets+=(/proc/net/tcp)
    [[ -r /proc/net/tcp6 ]] && sockets+=(/proc/net/tcp6)
    ((${#sockets[@]} > 0)) || die 'PORT_PROBE_UNAVAILABLE: cannot inspect listening TCP sockets.'
    awk -v port="$1" 'BEGIN {target=sprintf("%04X",port)} NR > 1 {n=split($2,a,":"); if (toupper(a[n]) == target && $4 == "0A") found=1} END {exit !found}' \
      "${sockets[@]}"
  fi
}
service_active() { systemctl is-active --quiet "$service_name.service" 2>/dev/null; }
systemd_property() {
  local property="$1" value
  value="$(systemctl show --property="$property" webdav.service 2>/dev/null || true)"
  [[ "$value" == "$property="* ]] || return 1
  printf '%s' "${value#*=}"
}
managed() { [[ -f "$state_dir/installed" && -f "$state_dir/ownership" ]] && grep -Fxq managed "$state_dir/ownership"; }
verify_managed_paths() {
  [[ ! -L "$install_dir" && ! -L "$data_dir" && ! -L "$config_dir" && ! -L "$unit_file" ]] ||
    die 'MANAGED_PATH_INVALID: symbolic links are not accepted for managed resources.'
  if [[ -f "$state_dir/install-dir" && -f "$state_dir/data-dir" ]]; then
    [[ "$(cat "$state_dir/install-dir")" == "$install_dir" &&
       "$(cat "$state_dir/data-dir")" == "$data_dir" ]] ||
      die 'MANAGED_PATH_MISMATCH: requested paths differ from the recorded installation.'
    if [[ ! -f "$unit_file" ]] || ! grep -Fqx 'Description=Oneinstack WebDAV' "$unit_file" ||
       ! grep -Fqx "ExecStart=$install_dir/webdav --config $config_file" "$unit_file" ||
       ! grep -Fqx "WorkingDirectory=$data_dir" "$unit_file"; then
      die 'UNIT_UNMANAGED: WebDAV unit ownership could not be verified.'
    fi
  else
    if [[ ! -f "$unit_file" ]] || ! grep -Fqx 'Description=WebDAV' "$unit_file" ||
       ! grep -Fq "ExecStart=$install_dir/webdav -c $data_dir/config.yaml" "$unit_file" ||
       ! grep -Fqx "WorkingDirectory=$data_dir" "$unit_file"; then
      die 'LEGACY_OWNERSHIP_UNVERIFIED: previous WebDAV unit paths do not match.'
    fi
  fi
}
ensure_requested_port_available() {
  local current_port='' config
  if managed && service_active; then
    for config in "$config_file" "$data_dir/config.yaml"; do
      [[ -f "$config" ]] || continue
      current_port="$(sed -nE 's/^[[:space:]]*port:[[:space:]]*([0-9]+).*$/\1/p' "$config" | head -n1)"
      [[ -n "$current_port" ]] && break
    done
    [[ "$current_port" == "$port" ]] && return 0
  fi
  port_listening "$port" && die "PORT_CONFLICT: WebDAV port $port is occupied."
  return 0
}
ensure_unowned_safe() {
  if managed; then verify_managed_paths; return 0; fi
  [[ ! -e "$install_dir" && ! -e "$config_dir" && ! -e "$unit_file" ]] ||
    die 'EXTERNAL_INSTALLATION_CONFLICT: WebDAV program, config, or unit already exists without managed ownership.'
  if [[ -e "$data_dir" || -L "$data_dir" ]]; then
    [[ -d "$data_dir" && ! -L "$data_dir" && -f "$state_dir/retained-data" &&
       -f "$state_dir/data-dir" && "$(cat "$state_dir/data-dir")" == "$data_dir" &&
       "$(cat "$state_dir/retained-data")" == "$data_dir" ]] ||
      die 'EXTERNAL_DATA_CONFLICT: data directory already exists without managed ownership.'
  fi
}
ensure_traversal_acl() {
  local target="$1" record="$2" traversal_need="${3:-write}" parent entry mask index
  local -a parents=()
  [[ -d "$target" ]] || die 'DATA_DIR_MISSING: WebDAV data directory is missing.'
  parent="$target"
  while [[ "$parent" != / ]]; do
    parent="$(dirname -- "$parent")"
    parents+=("$parent")
  done
  for ((index=${#parents[@]}-1; index>=0; index--)); do
    parent="${parents[index]}"
    runuser -u webdav -- test -x "$parent" && continue
    entry="$(getfacl -cp -- "$parent" 2>/dev/null | awk -F: '$1=="user" && $2=="webdav" {print $3; exit}')"
    [[ -z "$entry" ]] || die "ACL_CONFLICT: existing webdav ACL blocks traversal of $parent."
    mask="$(getfacl -cp -- "$parent" 2>/dev/null | awk -F: '$1=="mask" {print $3; exit}')"
    [[ -z "$mask" || "$mask" == *x* ]] || die "ACL_MASK_CONFLICT: ACL mask blocks traversal of $parent."
    setfacl -n -m u:webdav:--x -- "$parent"
    runuser -u webdav -- test -x "$parent" || die "ACL_TRAVERSAL_FAILED: cannot traverse $parent."
    printf '%s\n' "$parent" >>"$record"
  done
  runuser -u webdav -- test -x "$target" || die 'DIRECTORY_PERMISSION_DENIED: webdav cannot traverse target directory.'
  if [[ "$traversal_need" == write ]]; then
    runuser -u webdav -- test -w "$target" || die 'DATA_PERMISSION_DENIED: webdav cannot write data directory.'
  fi
}
remove_added_acl() {
  local record="${1:-$state_dir/added-acl}"
  [[ -f "$record" ]] || return 0
  local parent entry
  while IFS= read -r parent; do
    [[ -d "$parent" ]] || continue
    entry="$(getfacl -cp -- "$parent" 2>/dev/null | awk -F: '$1=="user" && $2=="webdav" {print $3; exit}')"
    [[ "$entry" == --x ]] && setfacl -n -x u:webdav -- "$parent" || true
  done <"$record"
  rm -f -- "$record"
}
hash_password() {
  local line
  line="$(printf '%s\n' "$password" | htpasswd -inBC 12 "$username")" ||
    die 'PASSWORD_HASH_FAILED: htpasswd could not generate a bcrypt hash.'
  [[ "$line" == "$username:"* ]] || die 'PASSWORD_HASH_FAILED: unexpected hash output.'
  password_hash="{bcrypt}${line#*:}"
  # shellcheck disable=SC2016
  [[ "$password_hash" == '{bcrypt}$2'* ]] || die 'PASSWORD_HASH_FAILED: expected bcrypt output.'
}
write_config() {
  local target="$1"
  cat >"$target" <<CONFIG
# Managed by Oneinstack WebDAV component
address: "$bind_address"
port: $port
prefix: "$prefix"
directory: "$data_dir"
permissions: "$permissions"
behindProxy: $behind_proxy
tls: $tls
cert: "$tls_cert"
key: "$tls_key"
users:
  - username: "$username"
    password: "$password_hash"
CONFIG
  chmod 0640 "$target"; chown root:webdav "$target"
}
write_unit() {
  local sandbox_directives
  if [[ "$host_id" == centos && "$host_version" == 7* ]]; then
    # systemd 219 supports these names and ProtectSystem=full, but not strict/ReadWritePaths.
    sandbox_directives="ProtectSystem=full
ReadWriteDirectories=$data_dir"
  else
    sandbox_directives="ProtectSystem=strict
ReadWritePaths=$data_dir"
  fi
  cat >"$unit_file" <<UNIT
[Unit]
Description=Oneinstack WebDAV
After=network.target
[Service]
Type=simple
User=webdav
Group=webdav
WorkingDirectory=$data_dir
ExecStart=$install_dir/webdav --config $config_file
Restart=on-failure
NoNewPrivileges=true
$sandbox_directives
PrivateTmp=true
[Install]
WantedBy=multi-user.target
UNIT
  chmod 0644 "$unit_file"
  systemctl daemon-reload
}
read_config() {
  [[ -f "$config_file" && ! -L "$config_file" ]] || die 'CONFIG_MISSING: managed configuration is unavailable.'
  grep -Fqx '# Managed by Oneinstack WebDAV component' "$config_file" ||
    die 'CONFIG_UNMANAGED: configuration ownership marker is missing.'
  local key value
  for key in address port prefix permissions behindProxy tls cert key; do
    value="$(sed -nE "s/^${key}: \"([^\"]*)\"$/\\1/p; s/^${key}: (true|false|[0-9]+)$/\\1/p" "$config_file" | head -n1)"
    case "$key" in
      address) bind_address="$value" ;; port) port="$value" ;; prefix) prefix="$value" ;;
      permissions) permissions="$value" ;; behindProxy) behind_proxy="$value" ;;
      tls) tls="$value" ;; cert) tls_cert="$value" ;; key) tls_key="$value" ;;
    esac
  done
  username="$(sed -nE 's/^  - username: "([^"]+)"$/\1/p' "$config_file" | head -n1)"
  password_hash="$(sed -nE 's/^    password: "([^"]+)"$/\1/p' "$config_file" | head -n1)"
  # shellcheck disable=SC2016
  [[ -n "$username" && "$password_hash" == '{bcrypt}$2'* ]] || die 'CONFIG_INVALID: authentication entry is missing.'
}
probe_url() {
  local host="$bind_address"
  [[ "$host" == 0.0.0.0 ]] && host=127.0.0.1
  [[ "$host" == :: ]] && host=::1
  [[ "$host" == *:* ]] && host="[$host]"
  local scheme=http
  [[ "$tls" == true ]] && scheme=https
  printf '%s://%s:%s%s' "$scheme" "$host" "$port" "$prefix"
}
verify_runtime() {
  local url unauthorized authorized attempt
  require_cmd curl
  url="$(probe_url)"
  for ((attempt=1; attempt<=30; attempt++)); do
    service_active && break
    sleep 1
  done
  service_active || die 'SERVICE_INACTIVE: WebDAV systemd service is not active.'
  [[ "$(systemd_property MainPID)" =~ ^[1-9][0-9]*$ ]] ||
    die 'PROCESS_MISSING: WebDAV main process is unavailable.'
  for ((attempt=1; attempt<=30; attempt++)); do
    unauthorized="$(curl --noproxy '*' --silent --insecure --max-time 2 --output /dev/null --write-out '%{http_code}' -X PROPFIND -H 'Depth: 0' "$url" 2>/dev/null || true)"
    [[ "$unauthorized" == 401 ]] && break
    sleep 1
  done
  [[ "$unauthorized" == 401 ]] || die 'AUTH_PROBE_FAILED: unauthenticated PROPFIND was not rejected.'
  if [[ -n "$password" ]]; then
    authorized="$(printf 'user = "%s:%s"\n' "$username" "$password" |
      curl --config - --noproxy '*' --silent --insecure --max-time 5 --output /dev/null \
        --write-out '%{http_code}' -X PROPFIND -H 'Depth: 0' "$url" 2>/dev/null)" ||
      die 'DAV_PROBE_FAILED: authenticated PROPFIND failed.'
    [[ "$authorized" == 207 ]] || die "DAV_PROBE_FAILED: authenticated PROPFIND returned HTTP $authorized."
  fi
}
