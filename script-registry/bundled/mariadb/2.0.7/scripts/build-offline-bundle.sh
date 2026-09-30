#!/usr/bin/env bash
set -Eeuo pipefail
umask 027

usage() {
  echo "Usage: $0 OUTPUT_ARCHIVE SOFTWARE_VERSION OS_ID OS_VERSION ARCH SOURCE_DIR PACKAGE_DIR" >&2
  exit 64
}

[[ "$#" -eq 7 ]] || usage
output_archive="$1"
software_version="$2"
os_id="$3"
os_version="$4"
architecture="$5"
source_dir="$6"
package_dir="$7"
script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
component_root="$(cd -- "${script_dir}/.." && pwd)"
release_key="${component_root}/assets/MariaDB-Server-GPG-KEY"
libfmt_archive="${component_root}/assets/fmt-12.2.0.zip"
libfmt_sha256="a2f4a8d51178f954e4c339007f77edd76ba0cb2e36f87a48e5a5403d9be5878f"
pcre2_archive="${component_root}/assets/pcre2-10.47.zip"
pcre2_sha256="d74c183c86c77248ad50017c7f45bae8f88106a6cca5d87ad09917e1c6fb0784"
fingerprint="177F4010FE56CA3336300305F1656F24C74CD1D8"
centos7_rpm_release="10.11.19-1.el7_9.x86_64"

die() { echo "ERROR: $*" >&2; exit 1; }
require_command() { command -v "$1" >/dev/null 2>&1 || die "Required command not found: $1"; }

case "${software_version}" in
  10.11.19) source_sha256="b8e543ee69d380fb1cfd563226f49e0fe96e4d67e7b7a9045ee514a168ed2066" ;;
  11.4.13) source_sha256="1bb254b106d0a7ca871cfa18fa6e18d4b80a7430f9ec9d1571ec4271a13def96" ;;
  *) usage ;;
esac
case "${architecture}" in amd64|arm64) ;; *) usage ;; esac
case "${os_id}:${os_version}" in
  ubuntu:22.04|ubuntu:24.04|ubuntu:26.04|debian:12|debian:13|\
  rhel:8|rhel:9|rhel:10|rocky:8|rocky:9|rocky:10|\
  almalinux:8|almalinux:9|almalinux:10|ol:8|ol:9|ol:10|\
  centos-stream:9|centos-stream:10|centos:7|amzn:2023|sles:15|sles:16) ;;
  *) usage ;;
esac
centos7_runtime=false
if [[ "${os_id}:${os_version}" == centos:7 ]]; then
  [[ "${software_version}:${architecture}" == 10.11.19:amd64 ]] ||
    die "CentOS 7 supports only MariaDB 10.11.19 on amd64."
  centos7_runtime=true
fi

[[ "${output_archive}" == /* && "$(realpath -m -- "${output_archive}")" == "${output_archive}" ]] ||
  die "OUTPUT_ARCHIVE must be a normalized absolute path."
case "${output_archive}" in /|/usr|/etc|/var|/home|/root) die "Unsafe OUTPUT_ARCHIVE." ;; esac
[[ "${output_archive}" == *.tar.gz ]] || die "OUTPUT_ARCHIVE must end with .tar.gz."
[[ ! -e "${output_archive}" ]] || die "OUTPUT_ARCHIVE already exists."
[[ -d "$(dirname -- "${output_archive}")" ]] || die "OUTPUT_ARCHIVE parent directory does not exist."
[[ -d "${source_dir}" && -d "${package_dir}" ]] || die "SOURCE_DIR and PACKAGE_DIR must exist."
[[ "${source_dir}" == /* && "$(realpath -m -- "${source_dir}")" == "${source_dir}" ]] || die "SOURCE_DIR must be a normalized absolute path."
[[ "${package_dir}" == /* && "$(realpath -m -- "${package_dir}")" == "${package_dir}" ]] || die "PACKAGE_DIR must be a normalized absolute path."
[[ "$(find "${component_root}/scripts" -maxdepth 1 -type f -name '*.sh' ! -perm -u+x -print -quit)" == "" ]] ||
  die "All MariaDB action and Bundle scripts must be executable."

require_command sha256sum
require_command tar
[[ -f "${release_key}" ]] || die "Embedded MariaDB release key is missing."
[[ -f "${libfmt_archive}" ]] || die "Bundled fmt 12.2.0 archive is missing."
[[ "$(sha256sum "${libfmt_archive}" | awk '{print $1}')" == "${libfmt_sha256}" ]] ||
  die "Bundled fmt 12.2.0 archive checksum verification failed."
[[ -f "${pcre2_archive}" ]] || die "Bundled PCRE2 10.47 archive is missing."
[[ "$(sha256sum "${pcre2_archive}" | awk '{print $1}')" == "${pcre2_sha256}" ]] ||
  die "Bundled PCRE2 10.47 archive checksum verification failed."

temporary_root="$(mktemp -d)"
trap 'rm -rf -- "${temporary_root}"' EXIT
artifacts=()
if [[ "${centos7_runtime}" == true ]]; then
  require_command rpm
  rpm_db="${temporary_root}/rpmdb"
  install -d -m 0700 -- "${rpm_db}"
  rpm --dbpath "${rpm_db}" --initdb
  rpm --dbpath "${rpm_db}" --import "${release_key}"
  for name_checksum in \
    "MariaDB-common-${centos7_rpm_release}.rpm:50de147a93083e3b8d13dc8c6cb690cf2dd3924b3e989cf386933f5ba63b449d" \
    "MariaDB-shared-${centos7_rpm_release}.rpm:23402bec671faf0eb2c84391462d074f74b67d4059c9ce8f8d008cf65054ec22" \
    "MariaDB-client-${centos7_rpm_release}.rpm:874d0d7e8d6fd7c807cb05a95a2e50925a3913a9eca3f794fd27ac8413377a24" \
    "MariaDB-server-${centos7_rpm_release}.rpm:44f2d9ab69ce7f9bb51e7bad4ece2982f1cf1ad9882a0d55f5362456e54e8a21"; do
    archive="${name_checksum%%:*}"
    checksum="${name_checksum#*:}"
    artifact="${source_dir}/${archive}"
    [[ -f "${artifact}" ]] || die "SOURCE_DIR must contain ${archive}."
    printf '%s  %s\n' "${checksum}" "${artifact}" | sha256sum --check --status ||
      die "MariaDB CentOS 7 RPM checksum verification failed: ${archive}."
    LC_ALL=C rpm --dbpath "${rpm_db}" --checksig "${artifact}" >/dev/null ||
      die "MariaDB CentOS 7 RPM signature verification failed: ${archive}."
    artifacts+=("${artifact}")
  done
else
  gpg_command=gpg
  command -v gpg >/dev/null 2>&1 || gpg_command=gpg2
  require_command "${gpg_command}"
  source_archive="mariadb-${software_version}.tar.gz"
  source_file="${source_dir}/${source_archive}"
  signature_file="${source_file}.asc"
  [[ -f "${source_file}" && -f "${signature_file}" ]] ||
    die "SOURCE_DIR must contain ${source_archive} and its detached signature."
  printf '%s  %s\n' "${source_sha256}" "${source_file}" | sha256sum --check --status ||
    die "MariaDB source checksum verification failed."
  gpg_home="${temporary_root}/gnupg"
  install -d -m 0700 -- "${gpg_home}"
  "${gpg_command}" --batch --homedir "${gpg_home}" --import "${release_key}" >/dev/null 2>&1
  key_verified=false
  while IFS= read -r current; do
    [[ "${current}" == "${fingerprint}" ]] && key_verified=true
  done < <("${gpg_command}" --batch --homedir "${gpg_home}" --with-colons --fingerprint 2>/dev/null |
    awk -F: '$1 == "fpr" {print toupper($10)}')
  [[ "${key_verified}" == true ]] || die "MariaDB release key fingerprint verification failed."
  signature_status="$("${gpg_command}" --batch --homedir "${gpg_home}" --status-fd=1 \
    --verify "${signature_file}" "${source_file}" 2>/dev/null)" ||
    die "MariaDB source signature verification failed."
  signature_verified=false
  while IFS=' ' read -r marker status signing_fingerprint _ _ _ _ _ _ _ _ primary_fingerprint _; do
    [[ "${marker}" == '[GNUPG:]' && "${status}" == VALIDSIG ]] || continue
    signing_fingerprint="${signing_fingerprint^^}"
    primary_fingerprint="${primary_fingerprint^^}"
    [[ "${signing_fingerprint}" == "${fingerprint}" || "${primary_fingerprint}" == "${fingerprint}" ]] &&
      signature_verified=true
  done <<<"${signature_status}"
  [[ "${signature_verified}" == true ]] ||
    die "MariaDB source signature is not rooted in the pinned publisher key."
  artifacts=("${source_file}" "${signature_file}")
fi

bundle_root="${temporary_root}/bundle"
install -d -m 0755 -- "${bundle_root}/scripts" "${bundle_root}/assets" \
  "${bundle_root}/artifacts/${architecture}" \
  "${bundle_root}/packages/${os_id}/${os_version}/${architecture}"
cp -a -- "${component_root}/manifest.yaml" "${bundle_root}/manifest.yaml"
cp -a -- "${component_root}/scripts/." "${bundle_root}/scripts/"
cp -a -- "${release_key}" "${bundle_root}/assets/MariaDB-Server-GPG-KEY"
cp -a -- "${libfmt_archive}" "${bundle_root}/assets/fmt-12.2.0.zip"
cp -a -- "${pcre2_archive}" "${bundle_root}/assets/pcre2-10.47.zip"
cp -a -- "${artifacts[@]}" "${bundle_root}/artifacts/${architecture}/"

case "${os_id}" in
  ubuntu|debian) package_pattern='*.deb' ;;
  *) package_pattern='*.rpm' ;;
esac
mapfile -t dependency_packages < <(find "${package_dir}" -maxdepth 1 -type f -name "${package_pattern}" -print | sort)
((${#dependency_packages[@]} > 0)) || die "PACKAGE_DIR has no ${package_pattern} dependency packages."
[[ "$(find "${package_dir}" -maxdepth 1 -type f ! -name "${package_pattern}" -print -quit)" == "" ]] ||
  die "PACKAGE_DIR contains an unexpected dependency package type."
for dependency_package in "${dependency_packages[@]}"; do
  if [[ "${package_pattern}" == '*.deb' ]]; then
    require_command dpkg-deb
    package_arch="$(dpkg-deb -f "${dependency_package}" Architecture 2>/dev/null)" ||
      die "Invalid Debian dependency package: ${dependency_package##*/}."
    [[ "${package_arch}" == all || "${package_arch}" == "${architecture}" ]] ||
      die "Dependency package architecture does not match: ${dependency_package##*/}."
  else
    require_command rpm
    package_arch="$(rpm -qp --qf '%{ARCH}' "${dependency_package}" 2>/dev/null)" ||
      die "Invalid RPM dependency package: ${dependency_package##*/}."
    case "${architecture}:${package_arch}" in
      amd64:x86_64|amd64:noarch|arm64:aarch64|arm64:noarch) ;;
      *) die "Dependency package architecture does not match: ${dependency_package##*/}." ;;
    esac
  fi
done
cp -a -- "${dependency_packages[@]}" "${bundle_root}/packages/${os_id}/${os_version}/${architecture}/"

printf 'component=mariadb\npackageVersion=2.0.7\nsoftwareVersion=%s\nosId=%s\nosVersion=%s\narchitecture=%s\n' \
  "${software_version}" "${os_id}" "${os_version}" "${architecture}" >"${bundle_root}/bundle-info"
find "${bundle_root}/scripts" -type f -name '*.sh' -exec chmod 0755 {} +
(
  cd "${bundle_root}"
  # files.sha256 is created only after find has enumerated the Bundle payload.
  # shellcheck disable=SC2094
  find . -type f ! -name files.sha256 -printf '%P\0' | sort -z |
    xargs -0 sha256sum >files.sha256
  sha256sum -c files.sha256 --status
)
tar -C "${bundle_root}" -czf "${output_archive}" \
  manifest.yaml files.sha256 bundle-info assets scripts artifacts packages
printf 'Created MariaDB offline Bundle: %s\n' "${output_archive}"
