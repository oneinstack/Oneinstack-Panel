#!/usr/bin/env bash
set -Eeuo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
component_dir="$(cd -- "${script_dir}/.." && pwd)"
output_dir="${1:-}"
software_version="${2:-}"
os_id="${3:-}"
os_version="${4:-}"
architecture="${5:-}"
source_dir="${6:-}"
package_dir="${7:-}"
publisher_fingerprint="25451EB088460026195BD62CB550E09EA0E98066"

usage() {
  printf 'Usage: %s OUTPUT SOFTWARE_VERSION OS_ID OS_VERSION ARCH SOURCE_DIR PACKAGE_DIR\n' "${BASH_SOURCE[0]}" >&2
  printf 'SOURCE_DIR must contain the selected source and its .asc signature.\n' >&2
  exit 64
}

[[ -n "${output_dir}" && -n "${software_version}" && -n "${os_id}" && -n "${os_version}" && -n "${architecture}" && -n "${source_dir}" && -n "${package_dir}" ]] || usage
case "${software_version}" in
  1.27.1.2) source_sha256="74f076f7e364b2a99a6c5f9bb531c27610c78985abe956b442b192a2295f7548" ;;
  1.31.1.1) source_sha256="65b78baadd3f0984055de89bf13f4a1932e5bfe9c31932037a134ea2b1a0ce42" ;;
  *) usage ;;
esac
[[ "${architecture}" == amd64 || "${architecture}" == arm64 ]] || usage
[[ "${os_id}" =~ ^[a-z0-9][a-z0-9._-]*$ && "${os_version}" =~ ^[A-Za-z0-9._-]+$ ]] || usage
case "${os_id}:${os_version}" in
  ubuntu:22.04|ubuntu:24.04|ubuntu:26.04|debian:11|debian:12|debian:13|rhel:8|rhel:8.*|rhel:9|rhel:9.*|rhel:10|rhel:10.*|rocky:8|rocky:8.*|rocky:9|rocky:9.*|rocky:10|rocky:10.*|almalinux:8|almalinux:8.*|almalinux:9|almalinux:9.*|almalinux:10|almalinux:10.*|ol:8|ol:8.*|ol:9|ol:9.*|ol:10|ol:10.*|centos:7|centos:7.*|centos:8|centos:8.*|centos:9|centos:9.*|centos:10|centos:10.*|fedora:*|amzn:2023|sles:15|sles:15.*|sles:16|sles:16.*|opensuse-leap:*|opensuse-tumbleweed:*|opensuse:*) ;;
  *) printf 'unsupported OpenResty Bundle host matrix entry: %s %s\n' "${os_id}" "${os_version}" >&2; exit 65 ;;
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

source_archive="openresty-${software_version}.tar.gz"
input="${source_dir}/${source_archive}"
signature="${input}.asc"
key_file="${component_dir}/keys/openresty/release.key"
[[ -f "${input}" && -f "${signature}" && -f "${key_file}" ]] || { printf 'missing OpenResty source, signature, or bundled release key.\n' >&2; exit 66; }
printf '%s  %s\n' "${source_sha256}" "${input}" | sha256sum --check --status || exit 65
gpg_command="gpg"
command -v "${gpg_command}" >/dev/null 2>&1 || gpg_command="gpg2"
command -v "${gpg_command}" >/dev/null 2>&1 || { printf 'gpg or gpg2 is required.\n' >&2; exit 69; }
gpg_home="$(mktemp -d)"
trap 'rm -rf -- "${gpg_home}"' EXIT
chmod 0700 "${gpg_home}"
"${gpg_command}" --batch --homedir "${gpg_home}" --import "${key_file}" >/dev/null 2>&1 || exit 65
fingerprints="$("${gpg_command}" --batch --homedir "${gpg_home}" --with-colons --fingerprint 2>/dev/null | awk -F: '$1 == "fpr" {print $10}')"
grep -Fxq "${publisher_fingerprint}" <<<"${fingerprints}" || { printf 'OpenResty release key fingerprint mismatch.\n' >&2; exit 65; }
verification_status="$("${gpg_command}" --batch --homedir "${gpg_home}" --status-fd=1 --verify "${signature}" "${input}" 2>/dev/null)" || exit 65
grep -Eq "^\[GNUPG:\] VALIDSIG ([A-F0-9]+ )?.*${publisher_fingerprint}" <<<"${verification_status}" || {
  grep -Fq "[GNUPG:] VALIDSIG ${publisher_fingerprint}" <<<"${verification_status}" || { printf 'OpenResty source signature fingerprint mismatch.\n' >&2; exit 65; }
}

bundle_artifacts="${output_dir}/artifacts/${architecture}"
bundle_packages="${output_dir}/packages/${os_id}/${os_version}/${architecture}"
bundle_scripts="${output_dir}/scripts"
bundle_keys="${output_dir}/keys/openresty"
rm -rf -- "${output_dir}"
install -d -m 0755 -- "${bundle_artifacts}" "${bundle_packages}" "${bundle_scripts}" "${bundle_keys}"
install -m 0644 -- "${component_dir}/manifest.yaml" "${output_dir}/manifest.yaml"
printf 'component=openresty\npackageVersion=1.0.13\nsoftwareVersion=%s\nosId=%s\nosVersion=%s\narchitecture=%s\n' "${software_version}" "${os_id}" "${os_version}" "${architecture}" >"${output_dir}/bundle-info"
chmod 0644 "${output_dir}/bundle-info"
find "${component_dir}/scripts" -maxdepth 1 -type f -name '*.sh' -exec install -m 0755 -- '{}' "${bundle_scripts}/" \;
install -m 0644 -- "${input}" "${bundle_artifacts}/${source_archive}"
install -m 0644 -- "${signature}" "${bundle_artifacts}/${source_archive}.asc"
install -m 0644 -- "${key_file}" "${bundle_keys}/release.key"
find "${package_dir}" -maxdepth 1 -type f \( -name '*.deb' -o -name '*.rpm' \) -exec install -m 0644 -- '{}' "${bundle_packages}/" \;
find "${bundle_packages}" -maxdepth 1 -type f \( -name '*.deb' -o -name '*.rpm' \) -print -quit | grep -q . || { printf 'no offline .deb or .rpm packages were supplied.\n' >&2; exit 66; }
checksum_file="${output_dir}/files.sha256"
while IFS= read -r relative; do
  (cd -- "${output_dir}" && sha256sum "${relative}")
done < <(cd -- "${output_dir}" && find . -type f ! -name files.sha256 -print | sed 's#^./##' | sort) >"${checksum_file}"
chmod 0644 "${checksum_file}"
printf 'Offline OpenResty Bundle created: %s\n' "${output_dir}"
