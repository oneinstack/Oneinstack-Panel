#!/usr/bin/env bash
set -Eeuo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
component_dir="$(cd -- "${script_dir}/.." && pwd)"
output_dir="${1:-}"; software_version="${2:-}"; os_id="${3:-}"; os_version="${4:-}"
architecture="${5:-}"; source_dir="${6:-}"; package_dir="${7:-}"
usage() {
  printf 'Usage: %s OUTPUT SOFTWARE_VERSION OS_ID OS_VERSION ARCH SOURCE_DIR PACKAGE_DIR\n' "${BASH_SOURCE[0]}" >&2
  printf 'SOURCE_DIR must contain the exact OpenSearch tar archive and .sig file; PACKAGE_DIR must contain the target host dependency closure.\n' >&2
  exit 64
}
[[ -n "${output_dir}" && -n "${software_version}" && -n "${os_id}" && -n "${os_version}" && -n "${architecture}" && -n "${source_dir}" && -n "${package_dir}" ]] || usage
[[ "${software_version}" == 3.7.0 && ( "${architecture}" == amd64 || "${architecture}" == arm64 ) ]] || usage
[[ "${os_id}" =~ ^[a-z0-9][a-z0-9._-]*$ && "${os_version}" =~ ^[A-Za-z0-9._-]+$ ]] || usage
case "${os_id}:${os_version}" in
  ubuntu:22.04|ubuntu:24.04|ubuntu:26.04|debian:11|debian:12|debian:13|\
  rhel:8|rhel:9|rhel:10|rocky:8|rocky:9|rocky:10|almalinux:8|almalinux:9|almalinux:10|\
  ol:8|ol:9|ol:10|centos-stream:8|centos-stream:9|centos-stream:10|amzn:2023|sles:15|sles:16) ;;
  *) usage ;;
esac
[[ "${output_dir}" == /* && "$(realpath -m -- "${output_dir}")" == "${output_dir}" ]] || usage
case "${output_dir}" in /|/tmp|/private/tmp|/usr|/usr/local|/var|/var/tmp|/data|/home|/root|"${component_dir}"|"${component_dir}"/*) printf 'refusing unsafe Bundle output: %s\n' "${output_dir}" >&2; exit 64 ;; esac
[[ -f "${component_dir}/manifest.yaml" && -f "${component_dir}/assets/opensearch-release.pgp" && -d "${source_dir}" && -d "${package_dir}" ]] || exit 66
source_real="$(realpath -m -- "${source_dir}")"; package_real="$(realpath -m -- "${package_dir}")"
case "${source_real}" in "${output_dir}"|"${output_dir}"/*) exit 64 ;; esac
case "${package_real}" in "${output_dir}"|"${output_dir}"/*) exit 64 ;; esac
case "${architecture}" in
  amd64)
    release_arch=x64
    release_sha512="9e481db6495cbdacd6704a19c6ed7679e1138d40dbe283642c599bf70441c968bbd7f6faf7ecfa6c9ecf0685141037079b53647c5df3595bd5b9647bcbad1a04"
    ;;
  arm64)
    release_arch=arm64
    release_sha512="fbc2dcb98ab909c113f70473861f9a7e62d9b3c59c6c06201617a3e3d1ce7ec9707ecba217ead62ea553f2486bb91b87f2d34a99d498dc5c5d4f0659fe01db50"
    ;;
esac
archive="opensearch-${software_version}-linux-${release_arch}.tar.gz"
[[ -f "${source_dir}/${archive}" && -f "${source_dir}/${archive}.sig" ]] || { printf 'missing OpenSearch archive or signature: %s\n' "${archive}" >&2; exit 66; }
printf '%s  %s\n' "${release_sha512}" "${source_dir}/${archive}" | sha512sum --check --status || { printf 'OpenSearch archive SHA-512 mismatch\n' >&2; exit 65; }
printf '%s  %s\n' "6571e52a5da71e18bd1ca4cdef4eae9c6482b50e146e03a611b15e85bfbead29" "${component_dir}/assets/opensearch-release.pgp" | sha256sum --check --status || exit 65
gpg_command="$(command -v gpg 2>/dev/null || command -v gpg2 2>/dev/null)" || { printf 'gpg or gpg2 is required\n' >&2; exit 69; }
gpg_home="$(mktemp -d)"; trap 'rm -rf -- "${gpg_home}"' EXIT; chmod 0700 "${gpg_home}"
GNUPGHOME="${gpg_home}" "${gpg_command}" --batch --quiet --import "${component_dir}/assets/opensearch-release.pgp" >/dev/null 2>&1
fingerprint="$(GNUPGHOME="${gpg_home}" "${gpg_command}" --batch --with-colons --fingerprint 2>/dev/null | awk -F: '$1 == "fpr" { print toupper($10); exit }')"
[[ "${fingerprint}" == A8B2D9E04CD51FEF6AA2DB53BA81D99981191457 ]] || { printf 'OpenSearch release key fingerprint mismatch\n' >&2; exit 65; }
GNUPGHOME="${gpg_home}" "${gpg_command}" --batch --verify "${source_dir}/${archive}.sig" "${source_dir}/${archive}" >/dev/null 2>&1 || { printf 'OpenSearch archive signature mismatch\n' >&2; exit 65; }
rm -rf -- "${gpg_home}"; trap - EXIT

rm -rf -- "${output_dir}"
bundle_artifacts="${output_dir}/artifacts/${architecture}"
bundle_packages="${output_dir}/packages/${os_id}/${os_version}/${architecture}"
install -d -m 0755 -- "${bundle_artifacts}" "${bundle_packages}" "${output_dir}/keys" "${output_dir}/scripts"
install -m 0644 -- "${component_dir}/manifest.yaml" "${output_dir}/manifest.yaml"
find "${component_dir}/scripts" -maxdepth 1 -type f -name '*.sh' -exec install -m 0755 -- '{}' "${output_dir}/scripts/" \;
install -m 0644 -- "${source_dir}/${archive}" "${source_dir}/${archive}.sig" "${bundle_artifacts}/"
install -m 0644 -- "${component_dir}/assets/opensearch-release.pgp" "${output_dir}/keys/opensearch-release.pgp"
find "${package_dir}" -maxdepth 1 -type f \( -name '*.deb' -o -name '*.rpm' \) -exec install -m 0644 -- '{}' "${bundle_packages}/" \;
find "${bundle_packages}" -maxdepth 1 -type f \( -name '*.deb' -o -name '*.rpm' \) -print -quit | grep -q . || { printf 'no offline dependency packages supplied\n' >&2; exit 66; }
printf 'component=opensearch\npackageVersion=1.0.7\nsoftwareVersion=%s\nosId=%s\nosVersion=%s\narchitecture=%s\n' \
  "${software_version}" "${os_id}" "${os_version}" "${architecture}" >"${output_dir}/bundle-info"
chmod 0644 "${output_dir}/bundle-info"
while IFS= read -r relative; do (cd -- "${output_dir}" && sha256sum "${relative}"); done \
  < <(cd -- "${output_dir}" && find . -type f ! -name files.sha256 -print | sed 's#^./##' | sort) >"${output_dir}/files.sha256"
chmod 0644 "${output_dir}/files.sha256"
(cd -- "${output_dir}" && sha256sum --check --strict files.sha256 >/dev/null)
printf 'OpenSearch offline Bundle created: %s\n' "${output_dir}"
