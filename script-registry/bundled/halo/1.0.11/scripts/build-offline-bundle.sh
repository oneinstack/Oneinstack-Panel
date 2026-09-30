#!/usr/bin/env bash
set -Eeuo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
component_dir="$(cd -- "${script_dir}/.." && pwd)"
output_dir="${1:-}"
software_version_arg="${2:-}"
os_id="${3:-}"
os_version="${4:-}"
architecture="${5:-}"
source_dir="${6:-}"
package_dir="${7:-}"
usage() {
  printf 'Usage: %s OUTPUT SOFTWARE_VERSION OS_ID OS_VERSION ARCH SOURCE_DIR PACKAGE_DIR\n' "${BASH_SOURCE[0]}" >&2
  printf 'SOURCE_DIR must contain both Halo JARs as needed, the exact Temurin archive and .sig, and adoptium-release-key.asc.\n' >&2
  exit 64
}
[[ -n "${output_dir}" && -n "${software_version_arg}" && -n "${os_id}" && -n "${os_version}" && -n "${architecture}" && -n "${source_dir}" && -n "${package_dir}" ]] || usage
[[ "${software_version_arg}" == 2.22.9 || "${software_version_arg}" == 2.25.4 ]] || usage
[[ "${architecture}" == amd64 || "${architecture}" == arm64 ]] || usage
[[ "${os_id}" =~ ^[a-z0-9][a-z0-9._-]*$ && "${os_version}" =~ ^[A-Za-z0-9._-]+$ ]] || usage
case "${os_id}:${os_version}" in
  ubuntu:20.04|ubuntu:22.04|ubuntu:24.04|debian:11|debian:12|rocky:8|rocky:9|almalinux:8|almalinux:9|centos:7) ;;
  *) printf 'unsupported Halo Bundle target: %s %s\n' "${os_id}" "${os_version}" >&2; exit 64 ;;
esac
[[ "${output_dir}" == /* && "$(realpath -m -- "${output_dir}")" == "${output_dir}" ]] || usage
case "${output_dir}" in /|/tmp|/private/tmp|/usr|/usr/local|/var|/var/tmp|/data|/home|/root|"${component_dir}"|"${component_dir}"/*) printf 'refusing unsafe Bundle output: %s\n' "${output_dir}" >&2; exit 64 ;; esac
[[ -f "${component_dir}/manifest.yaml" && -d "${source_dir}" && -d "${package_dir}" ]] || exit 66
source_real="$(realpath -m -- "${source_dir}")"; package_real="$(realpath -m -- "${package_dir}")"
case "${source_real}" in "${output_dir}"|"${output_dir}"/*) exit 64 ;; esac
case "${package_real}" in "${output_dir}"|"${output_dir}"/*) exit 64 ;; esac

# Load the exact lifecycle checksum and filename table without inspecting this build host.
SOFTWARE_VERSION="${software_version_arg}" ONEINSTACK_COMPONENT_STATE=/var/lib/oneinstack/halo-bundle-build-state source "${script_dir}/common.sh"
software_version="${software_version_arg}"; host_arch="${architecture}"
select_artifacts
jar_input="${source_dir}/halo-${software_version}.jar"
jre_input="${source_dir}/${jre_archive}"
signature_input="${jre_input}.sig"
key_input="${source_dir}/adoptium-release-key.asc"
for required in "${jar_input}" "${jre_input}" "${signature_input}" "${key_input}"; do
  [[ -f "${required}" ]] || { printf 'missing Bundle input: %s\n' "$(basename -- "${required}")" >&2; exit 66; }
done
verify_checksum "${jar_input}" "${jar_sha256}"
verify_checksum "${jre_input}" "${jre_sha256}"
verify_checksum "${signature_input}" "${jre_signature_sha256}"
verify_checksum "${key_input}" "${adoptium_key_sha256}"
verify_signature "${jre_input}" "${signature_input}" "${key_input}"

rm -rf -- "${output_dir}"
bundle_common="${output_dir}/artifacts/common"
bundle_arch="${output_dir}/artifacts/${architecture}"
bundle_packages="${output_dir}/packages/${os_id}/${os_version}/${architecture}"
install -d -m 0755 -- "${bundle_common}" "${bundle_arch}" "${bundle_packages}" "${output_dir}/keys" "${output_dir}/scripts"
install -m 0644 -- "${component_dir}/manifest.yaml" "${output_dir}/manifest.yaml"
find "${component_dir}/scripts" -maxdepth 1 -type f -name '*.sh' -exec install -m 0755 -- '{}' "${output_dir}/scripts/" \;
install -m 0644 -- "${jar_input}" "${bundle_common}/halo-${software_version}.jar"
install -m 0644 -- "${jre_input}" "${signature_input}" "${bundle_arch}/"
install -m 0644 -- "${key_input}" "${output_dir}/keys/adoptium-release-key.asc"
find "${package_dir}" -maxdepth 1 -type f \( -name '*.deb' -o -name '*.rpm' \) -exec install -m 0644 -- '{}' "${bundle_packages}/" \;
find "${bundle_packages}" -maxdepth 1 -type f \( -name '*.deb' -o -name '*.rpm' \) -print -quit | grep -q . || { printf 'no exact offline dependency packages supplied\n' >&2; exit 66; }
case "${os_id}" in
  ubuntu|debian)
    for direct_package in acl ca-certificates coreutils curl findutils gnupg gzip iproute2 passwd procps tar util-linux; do
      find "${bundle_packages}" -maxdepth 1 -type f -name "${direct_package}_*.deb" -print -quit | grep -q . || { printf 'missing direct offline package: %s\n' "${direct_package}" >&2; exit 66; }
    done
    ;;
  *)
    rpm_direct=(acl ca-certificates coreutils curl findutils gzip tar util-linux)
    case "${os_id}" in
      sles|opensuse|opensuse-leap|opensuse-tumbleweed) rpm_direct+=(gpg2 iproute2 procps shadow) ;;
      *) rpm_direct+=(gnupg2 iproute procps-ng shadow-utils) ;;
    esac
    for direct_package in "${rpm_direct[@]}"; do
      find "${bundle_packages}" -maxdepth 1 -type f -name "${direct_package}-[0-9]*.rpm" -print -quit | grep -q . || { printf 'missing direct offline package: %s\n' "${direct_package}" >&2; exit 66; }
    done
    ;;
esac
bundle_id="halo:${package_version}:${software_version}:${os_id}:${os_version}:${architecture}"
printf 'component=halo\npackageVersion=%s\nsoftwareVersion=%s\nosId=%s\nosVersion=%s\narchitecture=%s\nbundleId=%s\nhaloJarSha256=%s\njreVersion=%s\njreArchive=%s\njreSha256=%s\njreSignature=%s.sig\njreSignatureSha256=%s\njrePublisherFingerprint=%s\n' \
  "${package_version}" \
  "${software_version}" "${os_id}" "${os_version}" "${architecture}" "${bundle_id}" "${jar_sha256}" \
  "${jre_version}" "${jre_archive}" "${jre_sha256}" "${jre_archive}" "${jre_signature_sha256}" "${adoptium_key_fingerprint}" >"${output_dir}/bundle-info"
chmod 0644 "${output_dir}/bundle-info"
while IFS= read -r relative; do (cd -- "${output_dir}" && sha256sum "${relative}"); done \
  < <(cd -- "${output_dir}" && find . -type f ! -name files.sha256 -print | sed 's#^./##' | sort) >"${output_dir}/files.sha256"
chmod 0644 "${output_dir}/files.sha256"
(cd -- "${output_dir}" && sha256sum --check --strict files.sha256 >/dev/null)
printf 'Halo offline Bundle created: %s\nBundle ID: %s\nBundle digest: %s\n' \
  "${output_dir}" "${bundle_id}" "$(sha256sum "${output_dir}/files.sha256" | awk '{print $1}')"
