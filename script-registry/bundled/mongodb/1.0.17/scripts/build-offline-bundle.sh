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
  printf 'SOURCE_DIR must contain the exact MongoDB/mongosh archives, .sig files, server-8.0.asc, and mongosh.asc.\n' >&2
  exit 64
}
[[ -n "${output_dir}" && -n "${software_version_arg}" && -n "${os_id}" && -n "${os_version}" && -n "${architecture}" && -n "${source_dir}" && -n "${package_dir}" ]] || usage
[[ "${software_version_arg}" == 8.0.17 || "${software_version_arg}" == 8.0.32 ]] || usage
[[ "${architecture}" == amd64 || "${architecture}" == arm64 ]] || usage
[[ "${os_id}" =~ ^[a-z0-9][a-z0-9._-]*$ && "${os_version}" =~ ^[A-Za-z0-9._-]+$ ]] || usage
[[ "${output_dir}" == /* && "$(realpath -m -- "${output_dir}")" == "${output_dir}" ]] || usage
case "${output_dir}" in /|/tmp|/private/tmp|/usr|/usr/local|/var|/var/tmp|/data|/home|/root|"${component_dir}"|"${component_dir}"/*) printf 'refusing unsafe Bundle output: %s\n' "${output_dir}" >&2; exit 64 ;; esac
[[ -f "${component_dir}/manifest.yaml" && -d "${source_dir}" && -d "${package_dir}" ]] || exit 66
source_real="$(realpath -m -- "${source_dir}")"; package_real="$(realpath -m -- "${package_dir}")"
case "${source_real}" in "${output_dir}"|"${output_dir}"/*) exit 64 ;; esac
case "${package_real}" in "${output_dir}"|"${output_dir}"/*) exit 64 ;; esac

# Reuse the lifecycle's exact target and checksum table without reading host state.
SOFTWARE_VERSION="${software_version_arg}" ONEINSTACK_COMPONENT_STATE=/var/lib/oneinstack/components source "${script_dir}/common.sh"
software_version="${software_version_arg}"; system_id="${os_id}"; system_version="${os_version}"; host_arch="${architecture}"
select_source
for required in "${server_archive}" "${server_archive}.sig" "${mongosh_archive}" "${mongosh_archive}.sig" server-8.0.asc mongosh.asc; do
  [[ -f "${source_dir}/${required}" ]] || { printf 'missing Bundle input: %s\n' "${required}" >&2; exit 66; }
done
verify_checksum "${source_dir}/${server_archive}" "${server_sha256}" || exit 65
verify_checksum "${source_dir}/${mongosh_archive}" "${mongosh_sha256}" || exit 65
verify_checksum "${source_dir}/server-8.0.asc" "${server_key_sha256}" || exit 65
verify_checksum "${source_dir}/mongosh.asc" "${mongosh_key_sha256}" || exit 65
verify_signature "${source_dir}/${server_archive}" "${source_dir}/${server_archive}.sig" "${source_dir}/server-8.0.asc" "${server_key_fingerprint}"
verify_signature "${source_dir}/${mongosh_archive}" "${source_dir}/${mongosh_archive}.sig" "${source_dir}/mongosh.asc" "${mongosh_key_fingerprint}"

rm -rf -- "${output_dir}"
bundle_artifacts="${output_dir}/artifacts/${architecture}"
bundle_packages="${output_dir}/packages/${os_id}/${os_version}/${architecture}"
install -d -m 0755 -- "${bundle_artifacts}" "${bundle_packages}" "${output_dir}/keys" "${output_dir}/scripts"
install -m 0644 -- "${component_dir}/manifest.yaml" "${output_dir}/manifest.yaml"
find "${component_dir}/scripts" -maxdepth 1 -type f -name '*.sh' -exec install -m 0755 -- '{}' "${output_dir}/scripts/" \;
install -m 0644 -- "${source_dir}/${server_archive}" "${source_dir}/${server_archive}.sig" "${source_dir}/${mongosh_archive}" "${source_dir}/${mongosh_archive}.sig" "${bundle_artifacts}/"
install -m 0644 -- "${source_dir}/server-8.0.asc" "${source_dir}/mongosh.asc" "${output_dir}/keys/"
find "${package_dir}" -maxdepth 1 -type f \( -name '*.deb' -o -name '*.rpm' \) -exec install -m 0644 -- '{}' "${bundle_packages}/" \;
find "${bundle_packages}" -maxdepth 1 -type f \( -name '*.deb' -o -name '*.rpm' \) -print -quit | grep -q . || { printf 'no offline dependency packages supplied\n' >&2; exit 66; }
case "${os_id}" in
  ubuntu|debian)
    find "${bundle_packages}" -maxdepth 1 -type f -name 'acl_*.deb' -print -quit | grep -q . || { printf 'missing offline ACL package\n' >&2; exit 66; }
    ;;
  *)
    find "${bundle_packages}" -maxdepth 1 -type f -name 'acl-[0-9]*.rpm' -print -quit | grep -q . || { printf 'missing offline ACL package\n' >&2; exit 66; }
    ;;
esac
printf 'component=mongodb\npackageVersion=1.0.17\nsoftwareVersion=%s\nosId=%s\nosVersion=%s\narchitecture=%s\n' \
  "${software_version_arg}" "${os_id}" "${os_version}" "${architecture}" >"${output_dir}/bundle-info"
chmod 0644 "${output_dir}/bundle-info"
while IFS= read -r relative; do (cd -- "${output_dir}" && sha256sum "${relative}"); done \
  < <(cd -- "${output_dir}" && find . -type f ! -name files.sha256 -print | sed 's#^./##' | sort) >"${output_dir}/files.sha256"
chmod 0644 "${output_dir}/files.sha256"
(cd -- "${output_dir}" && sha256sum --check --strict files.sha256 >/dev/null)
printf 'MongoDB offline Bundle created: %s\n' "${output_dir}"
