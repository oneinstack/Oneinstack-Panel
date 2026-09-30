#!/usr/bin/env bash
set -Eeuo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
component_dir="$(cd -- "${script_dir}/.." && pwd)"
output_dir="${1:-}"
os_id="${2:-}"
os_version="${3:-}"
architecture="${4:-}"
artifact_dir="${5:-}"
package_dir="${6:-}"

usage() {
  printf 'Usage: %s OUTPUT OS_ID OS_VERSION ARCH ARTIFACT_DIR PACKAGE_DIR\n' "${BASH_SOURCE[0]}" >&2
  printf 'ARTIFACT_DIR must contain the matching official Caddy 2.10.2 Linux archive.\n' >&2
  printf 'PACKAGE_DIR must contain the matching ACL package; CentOS 7 also requires libcap.\n' >&2
  exit 64
}

[[ -n "${output_dir}" && -n "${os_id}" && -n "${os_version}" && -n "${architecture}" && -n "${artifact_dir}" && -n "${package_dir}" ]] || usage
[[ "${architecture}" == amd64 || "${architecture}" == arm64 ]] || usage
[[ "${os_id}" =~ ^[a-z0-9][a-z0-9._-]*$ && "${os_version}" =~ ^[A-Za-z0-9._-]+$ ]] || usage
case "${os_id}:${os_version}" in
  ubuntu:22.04|ubuntu:24.04|ubuntu:26.04|debian:11|debian:12|debian:13|rhel:8|rhel:9|rhel:10|rocky:8|rocky:9|rocky:10|almalinux:8|almalinux:9|almalinux:10|ol:8|ol:9|ol:10|centos:7|centos-stream:8|centos-stream:9|centos-stream:10|fedora:*|amzn:2023|sles:15|sles:16|opensuse-leap:*|opensuse-tumbleweed:*|opensuse:*) ;;
  *) printf 'unsupported Caddy Bundle host matrix entry: %s %s\n' "${os_id}" "${os_version}" >&2; exit 65 ;;
esac
[[ "${output_dir}" == /* ]] || usage
output_parent="$(dirname -- "${output_dir}")"
output_name="$(basename -- "${output_dir}")"
[[ "${output_name}" != "." && "${output_name}" != ".." ]] || usage
mkdir -p -- "${output_parent}"
output_dir="$(cd -- "${output_parent}" && pwd -P)/${output_name}"
[[ ! -L "${output_dir}" ]] || { printf 'refusing to replace a symlink output: %s\n' "${output_dir}" >&2; exit 64; }
case "${output_dir}" in
  /|/tmp|/private/tmp|/usr|/usr/local|/var|/var/tmp|/data|/home|/root|"${component_dir}"|"${component_dir}"/*)
    printf 'refusing to overwrite a broad or component source directory: %s\n' "${output_dir}" >&2
    exit 64
    ;;
esac
[[ -f "${component_dir}/manifest.yaml" && -d "${artifact_dir}" ]] || exit 66
artifact_dir="$(cd -- "${artifact_dir}" && pwd -P)"
case "${artifact_dir}" in "${output_dir}"|"${output_dir}"/*) usage ;; esac
[[ -d "${package_dir}" ]] || { printf 'PACKAGE_DIR does not exist: %s\n' "${package_dir}" >&2; exit 66; }
package_dir="$(cd -- "${package_dir}" && pwd -P)"
case "${package_dir}" in "${output_dir}"|"${output_dir}"/*) usage ;; esac

artifact="caddy_2.10.2_linux_${architecture}.tar.gz"
case "${architecture}" in
  amd64) artifact_sha256="5c218bc34c9197369263da7e9317a83acdbd80ef45d94dca5eff76e727c67cdd" ;;
  arm64) artifact_sha256="501e955fa634c5aab63247458c3ac655cfdd6cbf1e0436528f41248451c190ac" ;;
esac
input="${artifact_dir}/${artifact}"
[[ -f "${input}" && ! -L "${input}" ]] || { printf 'missing official artifact: %s\n' "${artifact}" >&2; exit 66; }
actual_sha256="$(sha256sum "${input}" | awk '{print $1}')"
[[ "${actual_sha256}" == "${artifact_sha256}" ]] || { printf 'official artifact checksum mismatch: %s\n' "${artifact}" >&2; exit 65; }

case "${os_id}" in
  ubuntu|debian)
    find "${package_dir}" -maxdepth 1 -type f -name 'acl_*.deb' -print -quit | grep -q . || { printf 'Bundle is missing the matching ACL DEB package.\n' >&2; exit 66; }
    ;;
  *)
    find "${package_dir}" -maxdepth 1 -type f -name 'acl-[0-9]*.rpm' -print -quit | grep -q . || { printf 'Bundle is missing the matching ACL RPM package.\n' >&2; exit 66; }
    ;;
esac
if [[ "${os_id}" == "centos" && "${os_version}" == "7" ]]; then
  find "${package_dir}" -maxdepth 1 -type f -name 'libcap*.rpm' -print -quit | grep -q . || { printf 'CentOS 7 Bundle is missing libcap RPM.\n' >&2; exit 66; }
fi

rm -rf -- "${output_dir}"
bundle_artifacts="${output_dir}/artifacts/${architecture}"
bundle_scripts="${output_dir}/scripts"
install -d -m 0755 -- "${bundle_artifacts}" "${bundle_scripts}"
install -m 0644 -- "${component_dir}/manifest.yaml" "${output_dir}/manifest.yaml"
printf 'component=caddy\npackageVersion=1.0.18\nsoftwareVersion=2.10.2\nosId=%s\nosVersion=%s\narchitecture=%s\n' \
  "${os_id}" "${os_version}" "${architecture}" >"${output_dir}/bundle-info"
chmod 0644 "${output_dir}/bundle-info"
find "${component_dir}/scripts" -maxdepth 1 -type f -name '*.sh' -exec install -m 0755 -- '{}' "${bundle_scripts}/" \;
install -m 0644 -- "${input}" "${bundle_artifacts}/${artifact}"
bundle_packages="${output_dir}/packages/${os_id}/${os_version}/${architecture}"
install -d -m 0755 -- "${bundle_packages}"
find "${package_dir}" -maxdepth 1 -type f \( -name '*.deb' -o -name '*.rpm' \) -exec install -m 0644 -- '{}' "${bundle_packages}/" \;
checksum_file="${output_dir}/files.sha256"
while IFS= read -r relative; do
  (cd -- "${output_dir}" && sha256sum "${relative}")
done < <(cd -- "${output_dir}" && find . -type f ! -name files.sha256 -print | sed 's#^./##' | sort) >"${checksum_file}"
chmod 0644 "${checksum_file}"
printf 'Offline Caddy Bundle created: %s (%s %s %s)\n' "${output_dir}" "${os_id}" "${os_version}" "${architecture}"
