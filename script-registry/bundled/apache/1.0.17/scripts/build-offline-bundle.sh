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
  printf 'SOURCE_DIR must contain the verified Apache, APR, APR-util, and nghttp2 archives.\n' >&2
  printf 'PACKAGE_DIR must contain the target offline .deb or .rpm dependency set.\n' >&2
  exit 64
}

[[ -n "${output_dir}" && -n "${os_id}" && -n "${os_version}" && -n "${architecture}" && -n "${source_dir}" && -n "${package_dir}" ]] || usage
[[ "${architecture}" == amd64 || "${architecture}" == arm64 ]] || usage
[[ "${os_id}" =~ ^[a-z0-9][a-z0-9._-]*$ && "${os_version}" =~ ^[A-Za-z0-9._-]+$ ]] || usage
[[ "${output_dir}" == /* && "$(realpath -m -- "${output_dir}")" == "${output_dir}" ]] || usage
case "${output_dir}" in
  /|/tmp|/private/tmp|/usr|/usr/local|/var|/var/tmp|/data|/home|/root|"${component_dir}"|"${component_dir}"/*)
    printf 'refusing to overwrite a broad or component source directory: %s\n' "${output_dir}" >&2
    exit 64
    ;;
esac
case "${component_dir}" in
  "${output_dir}"|"${output_dir}"/*)
    printf 'refusing to overwrite a parent of the component source directory: %s\n' "${output_dir}" >&2
    exit 64
    ;;
esac
[[ -f "${component_dir}/manifest.yaml" ]] || { printf 'manifest.yaml is missing.\n' >&2; exit 66; }
[[ -d "${source_dir}" && -d "${package_dir}" ]] || { printf 'source or package input directory is missing.\n' >&2; exit 66; }
source_real="$(realpath -m -- "${source_dir}")"
package_real="$(realpath -m -- "${package_dir}")"
case "${source_real}" in "${output_dir}"|"${output_dir}"/*) printf 'output directory overlaps source input.\n' >&2; exit 64 ;; esac
case "${package_real}" in "${output_dir}"|"${output_dir}"/*) printf 'output directory overlaps package input.\n' >&2; exit 64 ;; esac

source_sha256() {
  case "$1" in
    httpd-2.4.66.tar.gz) printf '%s\n' 442184763b60936471b88a91275f79d2407733b7aac27e345f270e8bc31c3d49 ;;
    apr-1.7.6.tar.gz) printf '%s\n' 6a10e7f7430510600af25fabf466e1df61aaae910bf1dc5d10c44a4433ccc81d ;;
    apr-util-1.6.3.tar.gz) printf '%s\n' 2b74d8932703826862ca305b094eef2983c27b39d5c9414442e9976a9acf1983 ;;
    nghttp2-1.64.0.tar.gz) printf '%s\n' 20e73f3cf9db3f05988996ac8b3a99ed529f4565ca91a49eb0550498e10621e8 ;;
    *) return 1 ;;
  esac
}

offline_rpm_package_present() {
  local package_dir="$1" package_name="$2" package_file
  if command -v rpm >/dev/null 2>&1; then
    while IFS= read -r -d '' package_file; do
      if rpm -qp --qf '%{NAME}\n' "${package_file}" 2>/dev/null | grep -Fxq "${package_name}"; then
        return 0
      fi
    done < <(find "${package_dir}" -maxdepth 1 -type f -name '*.rpm' -print0 2>/dev/null)
  else
    find "${package_dir}" -maxdepth 1 -type f -name "${package_name}-*.rpm" -print -quit 2>/dev/null | grep -q .
  fi
  return 1
}

bundle_file="${output_dir}/artifacts/${architecture}"
bundle_packages="${output_dir}/packages/${os_id}/${os_version}/${architecture}"
rm -rf -- "${output_dir}"
install -d -m 0755 -- "${bundle_file}" "${bundle_packages}"
install -m 0644 -- "${component_dir}/manifest.yaml" "${output_dir}/manifest.yaml"
printf 'component=apache\npackageVersion=1.0.16\nsoftwareVersion=2.4.66\nosId=%s\nosVersion=%s\narchitecture=%s\n' \
  "${os_id}" "${os_version}" "${architecture}" >"${output_dir}/bundle-info"
chmod 0644 "${output_dir}/bundle-info"

for archive in httpd-2.4.66.tar.gz apr-1.7.6.tar.gz apr-util-1.6.3.tar.gz nghttp2-1.64.0.tar.gz; do
  input="${source_dir}/${archive}"
  [[ -f "${input}" ]] || { printf 'missing source archive: %s\n' "${archive}" >&2; exit 66; }
  printf '%s  %s\n' "$(source_sha256 "${archive}")" "${input}" | sha256sum --check --status || {
    printf 'source archive checksum mismatch: %s\n' "${archive}" >&2
    exit 65
  }
  install -m 0644 -- "${input}" "${bundle_file}/${archive}"
done

find "${package_dir}" -maxdepth 1 -type f \( -name '*.deb' -o -name '*.rpm' \) -exec install -m 0644 -- '{}' "${bundle_packages}/" \;
find "${bundle_packages}" -maxdepth 1 -type f \( -name '*.deb' -o -name '*.rpm' \) -print -quit | grep -q . || {
  printf 'no offline .deb or .rpm packages were supplied.\n' >&2
  exit 66
}
if find "${bundle_packages}" -maxdepth 1 -type f -name '*.rpm' -print -quit | grep -q . &&
  ! offline_rpm_package_present "${bundle_packages}" libnghttp2-devel; then
  printf 'missing required RPM package: libnghttp2-devel (offline Apache Bundle for %s %s)\n' \
    "${os_id}" "${os_version}" >&2
  exit 66
fi

checksum_file="${output_dir}/files.sha256"
while IFS= read -r relative; do
  (cd -- "${output_dir}" && sha256sum "${relative}")
done < <(cd -- "${output_dir}" && find . -type f ! -name files.sha256 -print | sed 's#^\./##' | sort) >"${checksum_file}"
chmod 0644 "${checksum_file}"
printf 'Offline Apache Bundle created: %s\n' "${output_dir}"
