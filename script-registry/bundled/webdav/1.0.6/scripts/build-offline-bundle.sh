#!/usr/bin/env bash
set -Eeuo pipefail
script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
component_dir="$(cd -- "$script_dir/.." && pwd)"
output="${1:-}"; os_id="${2:-}"; os_version="${3:-}"; arch="${4:-}"
source_dir="${5:-}"; package_dir="${6:-}"
[[ "$#" -eq 6 && "$output" == /* && "$output" != / && "$output" != /tmp &&
   "$output" != /private/tmp && "$output" != "$component_dir" &&
   "$output" != "$component_dir/"* && "$component_dir" != "$output/"* ]] ||
  { printf 'Usage: %s OUTPUT OS_ID OS_VERSION ARCH SOURCE_DIR PACKAGE_DIR\n' "$0" >&2; exit 64; }
[[ "$(realpath -m -- "$output")" == "$output" && -d "$source_dir" && -d "$package_dir" ]] ||
  { printf 'Invalid output or input directory.\n' >&2; exit 64; }
[[ ! -e "$output" && ! -L "$output" && ! -e "${output}.tar.gz" ]] ||
  { printf 'Bundle output already exists; choose a new path.\n' >&2; exit 64; }
case "$os_id:$os_version" in
  ubuntu:20.04|ubuntu:22.04|ubuntu:24.04|debian:11|debian:12|rocky:8|rocky:8.*|rocky:9|rocky:9.*|almalinux:8|almalinux:8.*|almalinux:9|almalinux:9.*|centos:7|centos:7.*) ;;
  *) printf 'Target is outside the current WebDAV Manifest matrix: %s %s\n' "$os_id" "$os_version" >&2; exit 64 ;;
esac
case "$os_id:$os_version" in
  ubuntu:20.04|ubuntu:22.04|ubuntu:24.04|ubuntu:26.04|debian:11|debian:12|debian:13)
    extension=deb; password_package=apache2-utils ;;
  rhel:8*|rhel:9*|rhel:10*|rocky:8*|rocky:9*|rocky:10*|almalinux:8*|almalinux:9*|almalinux:10*|ol:8*|ol:9*|ol:10*|centos-stream:8*|centos-stream:9*|centos-stream:10*|centos:7*|amzn:2023|fedora:[0-9]*)
    extension=rpm; password_package=httpd-tools ;;
  sles:15*|opensuse-leap:15*|opensuse-leap:16*|opensuse-tumbleweed:[0-9]*)
    extension=rpm; password_package=apache2-utils ;;
  *) printf 'Unsupported WebDAV Bundle target: %s %s\n' "$os_id" "$os_version" >&2; exit 64 ;;
esac
[[ "$arch" == amd64 || "$arch" == arm64 ]] || { printf 'Unsupported architecture.\n' >&2; exit 64; }
[[ "$os_id" != centos || "$arch" == amd64 ]] ||
  { printf 'CentOS 7 WebDAV is limited to amd64.\n' >&2; exit 64; }
case "$arch" in
  amd64) source_hash=f632f359335a78f2d99b491250da54150281c27386cd4352d667ed6c30efb58b ;;
  arm64) source_hash=05a84c466001a179c9abb457374122480c5367b8d2b3bf0892d98bccc5b32ad2 ;;
esac
source_name="linux-$arch-webdav.tar.gz"
[[ -f "$source_dir/$source_name" ]] || { printf 'Missing upstream archive: %s\n' "$source_name" >&2; exit 66; }
printf '%s  %s\n' "$source_hash" "$source_dir/$source_name" | sha256sum --check --status ||
  { printf 'Upstream archive checksum mismatch.\n' >&2; exit 65; }
shopt -s nullglob
packages=("$package_dir"/*."$extension")
(("${#packages[@]}" > 0)) || { printf 'Offline dependency directory is empty.\n' >&2; exit 66; }
if [[ "$extension" == deb ]]; then
  command -v dpkg-deb >/dev/null 2>&1 || { printf 'dpkg-deb is required to inspect offline DEBs.\n' >&2; exit 69; }
  required=(acl apache2-utils ca-certificates curl iproute2 openssl tar)
  expected_arch="$arch"
else
  command -v rpm >/dev/null 2>&1 || { printf 'rpm is required to inspect offline RPMs.\n' >&2; exit 69; }
  required=(acl "$password_package" ca-certificates curl openssl tar)
  if [[ "$password_package" == httpd-tools ]]; then required+=(iproute); else required+=(iproute2); fi
  [[ "$arch" == amd64 ]] && expected_arch=x86_64 || expected_arch=aarch64
fi
declare -A included=()
for package in "${packages[@]}"; do
  case "$extension" in
    deb)
      package_name="$(dpkg-deb -f "$package" Package 2>/dev/null)" ||
        { printf 'Unreadable DEB metadata: %s\n' "$package" >&2; exit 66; }
      package_arch="$(dpkg-deb -f "$package" Architecture 2>/dev/null)" ||
        { printf 'Unreadable DEB architecture: %s\n' "$package" >&2; exit 66; }
      [[ "$package_arch" == "$expected_arch" || "$package_arch" == all ]] ||
        { printf 'DEB package architecture does not match: %s\n' "$package" >&2; exit 66; } ;;
    rpm)
      package_name="$(rpm -qp --qf '%{NAME}' "$package" 2>/dev/null)" ||
        { printf 'Unreadable RPM metadata: %s\n' "$package" >&2; exit 66; }
      package_arch="$(rpm -qp --qf '%{ARCH}' "$package" 2>/dev/null)" ||
        { printf 'Unreadable RPM architecture: %s\n' "$package" >&2; exit 66; }
      [[ "$package_arch" == "$expected_arch" || "$package_arch" == noarch ]] ||
        { printf 'RPM package architecture does not match: %s\n' "$package" >&2; exit 66; } ;;
  esac
  included["$package_name"]=1
done
for required_name in "${required[@]}"; do
  [[ -n "${included[$required_name]:-}" ]] ||
    { printf 'Offline dependency set lacks %s.\n' "$required_name" >&2; exit 66; }
done
source_real="$(realpath -m -- "$source_dir")"
package_real="$(realpath -m -- "$package_dir")"
for input in "$source_real" "$package_real"; do
  case "$input" in
    "$output"|"$output"/*) printf 'Output contains an input directory.\n' >&2; exit 64 ;;
  esac
  case "$output" in
    "$input"|"$input"/*) printf 'Output is inside an input directory.\n' >&2; exit 64 ;;
  esac
done
install -d -m 0755 -- "$output/artifacts/$arch" "$output/packages/$os_id/$os_version/$arch" "$output/scripts"
cp -p -- "$component_dir/manifest.yaml" "$output/manifest.yaml"
cp -p -- "$component_dir"/scripts/*.sh "$output/scripts/"
cp -p -- "$source_dir/$source_name" "$output/artifacts/$arch/"
cp -p -- "${packages[@]}" "$output/packages/$os_id/$os_version/$arch/"
printf 'component=webdav\npackageVersion=1.0.6\nsoftwareVersion=5.16.0\nosId=%s\nosVersion=%s\narchitecture=%s\n' \
  "$os_id" "$os_version" "$arch" >"$output/bundle-info"
(
  cd -- "$output"
  inventory="$(find . -type f ! -name files.sha256 -print | sed 's#^./##' | sort)"
  : >files.sha256
  while IFS= read -r file; do sha256sum "$file" >>files.sha256; done <<<"$inventory"
  sha256sum --check --strict files.sha256 >/dev/null
)
archive="${output}.tar.gz"
tar -czf "${archive}.part" -C "$output" manifest.yaml files.sha256 bundle-info scripts artifacts packages
mv -- "${archive}.part" "$archive"
printf 'WebDAV offline Bundle created: %s\n' "$archive"
