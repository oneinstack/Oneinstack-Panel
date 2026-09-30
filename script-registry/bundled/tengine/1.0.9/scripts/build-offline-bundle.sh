#!/usr/bin/env bash
set -Eeuo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
component_dir="$(cd -- "${script_dir}/.." && pwd)"
output_dir="${1:-}"
os_id="${2:-}"
os_version="${3:-}"
architecture="${4:-}"
source_dir="${5:-}"
package_dir="${6:-}"

usage() {
  printf 'Usage: %s OUTPUT OS_ID OS_VERSION ARCH SOURCE_DIR PACKAGE_DIR\n' "${BASH_SOURCE[0]}" >&2
  printf 'SOURCE_DIR must contain tengine-3.1.0.tar.gz.\n' >&2
  printf 'PACKAGE_DIR must contain the target offline .deb or .rpm dependency set.\n' >&2
  exit 64
}

[[ -n "${output_dir}" && -n "${os_id}" && -n "${os_version}" && -n "${architecture}" && -n "${source_dir}" && -n "${package_dir}" ]] || usage
[[ "${architecture}" == amd64 || "${architecture}" == arm64 ]] || usage
[[ "${os_id}" =~ ^[a-z0-9][a-z0-9._-]*$ && "${os_version}" =~ ^[A-Za-z0-9._-]+$ ]] || usage
case "${os_id}:${os_version}" in
  ubuntu:22.04|ubuntu:24.04|ubuntu:26.04|debian:11|debian:12|debian:13|rhel:8|rhel:8.*|rhel:9|rhel:9.*|rhel:10|rhel:10.*|rocky:8|rocky:8.*|rocky:9|rocky:9.*|rocky:10|rocky:10.*|almalinux:8|almalinux:8.*|almalinux:9|almalinux:9.*|almalinux:10|almalinux:10.*|ol:8|ol:8.*|ol:9|ol:9.*|ol:10|ol:10.*|centos:7|centos:7.*|centos-stream:8|centos-stream:8.*|centos-stream:9|centos-stream:9.*|centos-stream:10|centos-stream:10.*|fedora:*|amzn:2023|sles:15|sles:15.*|sles:16|sles:16.*|opensuse-leap:*|opensuse-tumbleweed:*|opensuse:*) ;;
  *) printf 'unsupported Tengine Bundle host matrix entry: %s %s\n' "${os_id}" "${os_version}" >&2; exit 65 ;;
esac
[[ "${output_dir}" == /* && "$(realpath -m -- "${output_dir}")" == "${output_dir}" ]] || usage
case "${output_dir}" in
  /|/tmp|/private/tmp|/usr|/usr/local|/var|/var/tmp|/data|/home|/root|"${component_dir}"|"${component_dir}"/*)
    printf 'refusing to overwrite a broad or component source directory: %s\n' "${output_dir}" >&2
    exit 64
    ;;
esac
[[ -f "${component_dir}/manifest.yaml" && -d "${source_dir}" && -d "${package_dir}" ]] || exit 66
source_real="$(realpath -m -- "${source_dir}")"
package_real="$(realpath -m -- "${package_dir}")"
case "${source_real}" in "${output_dir}"|"${output_dir}"/*) exit 64 ;; esac
case "${package_real}" in "${output_dir}"|"${output_dir}"/*) exit 64 ;; esac

source_archive="tengine-3.1.0.tar.gz"
source_sha256="64ed7155c0c904ce0fe7199c21b8eb6c2abfc267278fa8af832c0cb781e864dc"
input="${source_dir}/${source_archive}"
[[ -f "${input}" ]] || { printf 'missing source archive: %s\n' "${source_archive}" >&2; exit 66; }
printf '%s  %s\n' "${source_sha256}" "${input}" | sha256sum --check --status || exit 65

bundle_artifacts="${output_dir}/artifacts/${architecture}"
bundle_packages="${output_dir}/packages/${os_id}/${os_version}/${architecture}"
rm -rf -- "${output_dir}"
install -d -m 0755 -- "${bundle_artifacts}" "${bundle_packages}"
bundle_scripts="${output_dir}/scripts"
install -d -m 0755 -- "${bundle_scripts}"
install -m 0644 -- "${component_dir}/manifest.yaml" "${output_dir}/manifest.yaml"
printf 'component=tengine\npackageVersion=1.0.9\nsoftwareVersion=3.1.0\nosId=%s\nosVersion=%s\narchitecture=%s\n' "${os_id}" "${os_version}" "${architecture}" >"${output_dir}/bundle-info"
chmod 0644 "${output_dir}/bundle-info"
find "${component_dir}/scripts" -maxdepth 1 -type f -name '*.sh' -exec install -m 0755 -- '{}' "${bundle_scripts}/" \;
install -m 0644 -- "${input}" "${bundle_artifacts}/${source_archive}"
find "${package_dir}" -maxdepth 1 -type f \( -name '*.deb' -o -name '*.rpm' \) -exec install -m 0644 -- '{}' "${bundle_packages}/" \;
find "${bundle_packages}" -maxdepth 1 -type f \( -name '*.deb' -o -name '*.rpm' \) -print -quit | grep -q . || {
  printf 'no offline .deb or .rpm packages were supplied.\n' >&2
  exit 66
}
checksum_file="${output_dir}/files.sha256"
while IFS= read -r relative; do
  (cd -- "${output_dir}" && sha256sum "${relative}")
done < <(cd -- "${output_dir}" && find . -type f ! -name files.sha256 -print | sed 's#^./##' | sort) >"${checksum_file}"
chmod 0644 "${checksum_file}"
printf 'Offline Tengine Bundle created: %s\n' "${output_dir}"
