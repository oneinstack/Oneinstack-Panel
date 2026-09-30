#!/usr/bin/env bash
# shellcheck disable=SC2034,SC2154 # Target profile variables are consumed by sourced common.sh helpers.
set -Eeuo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
component_dir="$(cd -- "${script_dir}/.." && pwd)"
output_dir="${1:-}"
software_version_arg="${2:-}"
os_id="${3:-}"
os_version="${4:-}"
architecture="${5:-}"
database_dir="${6:-}"
package_dir="${7:-}"
max_bundle_bytes="${ONEINSTACK_OFFLINE_BUNDLE_MAX_BYTES:-67108864}"

usage() {
  printf 'Usage: %s OUTPUT SOFTWARE_VERSION OS_ID OS_VERSION ARCH DATABASE_DIR PACKAGE_DIR\n' "${BASH_SOURCE[0]}" >&2
  printf "%s\n" "DATABASE_DIR must contain main, daily, and bytecode CVD/CLD files. PACKAGE_DIR must contain the complete local DEB or RPM closure acquired from the profile's signed repository." >&2
  exit 64
}

[[ -n "${output_dir}" && -n "${software_version_arg}" && -n "${os_id}" && -n "${os_version}" && -n "${architecture}" && -n "${database_dir}" && -n "${package_dir}" ]] || usage
[[ "${software_version_arg}" == latest ]] || usage
[[ "${architecture}" == amd64 || "${architecture}" == arm64 ]] || usage
[[ "${os_id}" =~ ^[a-z0-9][a-z0-9._-]*$ && "${os_version}" =~ ^[A-Za-z0-9._-]+$ ]] || usage
[[ "${os_id}" == centos && "${os_version%%.*}" == 7 && "${architecture}" == amd64 ]] || {
  printf 'HOST_PROFILE_UNAVAILABLE: ClamAV component 1.0.26 only supports CentOS Linux 7 amd64; use the existing platform-specific package for other hosts.\n' >&2
  exit 66
}
[[ "${output_dir}" == /* && "$(realpath -m -- "${output_dir}")" == "${output_dir}" ]] || usage
[[ "${max_bundle_bytes}" =~ ^[0-9]+$ && "${max_bundle_bytes}" -ge 1048576 ]] || { printf 'ONEINSTACK_OFFLINE_BUNDLE_MAX_BYTES must be at least 1048576.\n' >&2; exit 64; }
case "${output_dir}" in
  /|/tmp|/private/tmp|/usr|/usr/local|/var|/var/tmp|/data|/home|/root|"${component_dir}"|"${component_dir}"/*)
    printf 'refusing unsafe Bundle output: %s\n' "${output_dir}" >&2
    exit 64
    ;;
esac
if [[ "${os_id}" == centos && "${os_version%%.*}" == 7 ]]; then
  exec "${script_dir}/build-centos7-bundle.sh" "${output_dir}" "${software_version_arg}" "${os_id}" "${os_version}" "${architecture}" "${database_dir}"
fi
[[ -f "${component_dir}/manifest.yaml" && -d "${database_dir}" && -d "${package_dir}" ]] || exit 66
database_real="$(realpath -m -- "${database_dir}")"
package_real="$(realpath -m -- "${package_dir}")"
case "${database_real}" in "${output_dir}"|"${output_dir}"/*) exit 64 ;; esac
case "${package_real}" in "${output_dir}"|"${output_dir}"/*) exit 64 ;; esac

# Reuse the lifecycle's exact matrix without reading the builder host state.
# shellcheck source=common.sh
SOFTWARE_VERSION="${software_version_arg}" ONEINSTACK_COMPONENT_STATE=/var/lib/oneinstack/components source "${script_dir}/common.sh"
software_version="${software_version_arg}"
system_id="${os_id}"
system_version="${os_version}"
host_arch="${architecture}"
if centos7_eol_runtime_profile; then
  printf '%s\n' 'CentOS Linux 7 cannot produce a verified ClamAV Bundle: archived EPEL only provides EOL ClamAV 0.103, which the official FreshClam CDN blocks from database updates.' >&2
  exit 66
fi
supported_host || { printf 'unsupported ClamAV Bundle target: %s %s %s\n' "${os_id}" "${os_version}" "${architecture}" >&2; exit 66; }
case "${os_id}" in
  ubuntu|debian) package_manager=apt ;;
  centos)
    [[ "${os_version%%.*}" == 7 ]] && package_manager=yum || package_manager=dnf
    ;;
  rhel|rocky|almalinux|ol|centos-stream|fedora|amzn) package_manager=dnf ;;
  sles|opensuse-leap|opensuse-tumbleweed|opensuse) package_manager=zypper ;;
  *) exit 66 ;;
esac
configure_runtime_packages
sigtool_binary="$(command -v sigtool 2>/dev/null || true)"
[[ -n "${sigtool_binary}" ]] || { printf 'sigtool is required to verify offline CVD/CLD inputs.\n' >&2; exit 69; }
validate_database_files "${database_real}"
daily_file="$(find "${database_real}" -maxdepth 1 -type f \( -name daily.cvd -o -name daily.cld \) -print -quit)"
database_timestamp="$("${sigtool_binary}" --info "${daily_file}" | awk -F': ' '/^Build time:/ {print $2; exit}')"
database_timestamp="$(date -u -d "${database_timestamp}" +%s 2>/dev/null || true)"
[[ "${database_timestamp}" =~ ^[0-9]+$ ]] || { printf 'unable to read signed daily database build time.\n' >&2; exit 65; }
now="$(date -u +%s)"
((database_timestamp <= now && now - database_timestamp <= offline_database_max_age_hours * 3600)) || {
  printf 'offline database is older than %s hours.\n' "${offline_database_max_age_hours}" >&2
  exit 65
}

case "${package_manager}" in
  apt)
    find "${package_real}" -maxdepth 1 -type f -name '*.deb' -print -quit | grep -q . || { printf 'no DEB packages supplied.\n' >&2; exit 66; }
    find "${package_real}" -maxdepth 1 -type f -name '*.rpm' -print -quit | grep -q . && { printf 'mixed DEB/RPM input is not allowed.\n' >&2; exit 66; }
    ;;
  *)
    find "${package_real}" -maxdepth 1 -type f -name '*.rpm' -print -quit | grep -q . || { printf 'no RPM packages supplied.\n' >&2; exit 66; }
    find "${package_real}" -maxdepth 1 -type f -name '*.deb' -print -quit | grep -q . && { printf 'mixed DEB/RPM input is not allowed.\n' >&2; exit 66; }
    ;;
esac

write_repository_lock() {
  local destination="$1" file package_name package_release package_arch signature key_file key_hash fingerprint='' found_key=false found_source=false
  {
    printf 'component=%s\npackageVersion=%s\npackageManager=%s\nosId=%s\nosVersion=%s\narchitecture=%s\n' \
      "${component_id}" "${package_version}" "${package_manager}" "${os_id}" "${os_version}" "${architecture}"
    case "${package_manager}" in
      apt)
        command -v dpkg-deb >/dev/null 2>&1 || { printf 'dpkg-deb is required to lock DEB packages.\n' >&2; exit 69; }
        while IFS= read -r file; do
          package_name="$(dpkg-deb -f "${file}" Package)"
          package_release="$(dpkg-deb -f "${file}" Version)"
          package_arch="$(dpkg-deb -f "${file}" Architecture)"
          printf 'package=%s|%s|%s|%s\n' "${package_name}" "${package_release}" "${package_arch}" "$(sha256sum "${file}" | awk '{print $1}')"
        done < <(find "${package_real}" -maxdepth 1 -type f -name '*.deb' -print | sort)
        for key_file in /etc/apt/trusted.gpg.d/* /usr/share/keyrings/*; do
          [[ -f "${key_file}" ]] || continue
          key_hash="$(sha256sum "${key_file}" | awk '{print $1}')"
          printf 'keySha256=%s\n' "${key_hash}"
          if command -v gpg >/dev/null 2>&1; then
            while IFS= read -r fingerprint; do [[ -z "${fingerprint}" ]] || printf 'gpgFingerprint=%s\n' "${fingerprint}"; done < <(gpg --batch --with-colons --show-keys "${key_file}" 2>/dev/null | awk -F: '$1 == "fpr" {print $10}')
          fi
          found_key=true
        done
        ;;
      dnf|yum|zypper)
        command -v rpm >/dev/null 2>&1 || { printf 'rpm is required to lock RPM packages.\n' >&2; exit 69; }
        while IFS= read -r file; do
          rpm --checksig "${file}" >/dev/null 2>&1 || { printf 'RPM signature check failed: %s\n' "${file}" >&2; exit 65; }
          package_name="$(rpm -qp --qf '%{NAME}' "${file}")"
          package_release="$(rpm -qp --qf '%{VERSION}-%{RELEASE}' "${file}")"
          package_arch="$(rpm -qp --qf '%{ARCH}' "${file}")"
          signature="$(rpm -qp --qf '%{SIGPGP:pgpsig}' "${file}" 2>/dev/null || true)"
          printf 'package=%s|%s|%s|%s\n' "${package_name}" "${package_release}" "${package_arch}" "$(sha256sum "${file}" | awk '{print $1}')"
          fingerprint="$(printf '%s' "${signature}" | sed -nE 's/.*key ID ([0-9A-Fa-f]+).*/\1/p' | head -n1)"
          [[ -z "${fingerprint}" ]] || { printf 'gpgFingerprint=%s\n' "${fingerprint}"; found_key=true; }
        done < <(find "${package_real}" -maxdepth 1 -type f -name '*.rpm' -print | sort)
        for key_file in /etc/pki/rpm-gpg/* /etc/zypp/keys/*; do
          [[ -f "${key_file}" ]] || continue
          key_hash="$(sha256sum "${key_file}" | awk '{print $1}')"
          printf 'keySha256=%s\n' "${key_hash}"
          if command -v gpg >/dev/null 2>&1; then
            while IFS= read -r fingerprint; do [[ -z "${fingerprint}" ]] || printf 'gpgFingerprint=%s\n' "${fingerprint}"; done < <(gpg --batch --with-colons --show-keys "${key_file}" 2>/dev/null | awk -F: '$1 == "fpr" {print $10}')
          fi
          found_key=true
        done
        ;;
    esac
    case "${package_manager}" in
      apt)
        for file in /etc/apt/sources.list /etc/apt/sources.list.d/*; do
          [[ -f "${file}" ]] || continue
          printf 'sourceConfigSha256=%s\n' "$(sha256sum "${file}" | awk '{print $1}')"
          found_source=true
        done
        ;;
      dnf|yum)
        for file in /etc/yum.repos.d/*; do
          [[ -f "${file}" ]] || continue
          printf 'sourceConfigSha256=%s\n' "$(sha256sum "${file}" | awk '{print $1}')"
          found_source=true
        done
        ;;
      zypper)
        for file in /etc/zypp/repos.d/*; do
          [[ -f "${file}" ]] || continue
          printf 'sourceConfigSha256=%s\n' "$(sha256sum "${file}" | awk '{print $1}')"
          found_source=true
        done
        ;;
    esac
    [[ "${found_source}" == true ]] || { printf 'no signed repository source configuration is available for this Bundle profile.\n' >&2; exit 69; }
    [[ "${found_key}" == true ]] || { printf 'no trusted repository key metadata is available for this Bundle profile.\n' >&2; exit 69; }
  } >"${destination}"
}

rm -rf -- "${output_dir}"
bundle_database="${output_dir}/database"
bundle_packages="${output_dir}/packages/${os_id}/${os_version}/${architecture}"
install -d -m 0755 -- "${bundle_database}" "${bundle_packages}" "${output_dir}/scripts"
install -m 0644 -- "${component_dir}/manifest.yaml" "${output_dir}/manifest.yaml"
find "${component_dir}/scripts" -maxdepth 1 -type f -name '*.sh' -exec install -m 0755 -- '{}' "${output_dir}/scripts/" \;
for database_name in main daily bytecode; do
  find "${database_real}" -maxdepth 1 -type f \( -name "${database_name}.cvd" -o -name "${database_name}.cld" \) -exec install -m 0640 -- '{}' "${bundle_database}/" \;
done
find "${package_real}" -maxdepth 1 -type f \( -name '*.deb' -o -name '*.rpm' \) -exec install -m 0644 -- '{}' "${bundle_packages}/" \;
find "${bundle_packages}" -maxdepth 1 -type f -print | sort | while IFS= read -r file; do
  printf '%s  %s\n' "$(sha256sum "${file}" | awk '{print $1}')" "${file#"${output_dir}"/}"
done >"${output_dir}/package-lock.sha256"
write_repository_lock "${output_dir}/repository-lock"
printf 'component=clamav\npackageVersion=%s\nsoftwareVersion=%s\nosId=%s\nosVersion=%s\narchitecture=%s\ndatabaseGeneratedAtUnix=%s\n' \
  "${package_version}" "${software_version_arg}" "${os_id}" "${os_version}" "${architecture}" "${database_timestamp}" >"${output_dir}/bundle-info"
chmod 0644 "${output_dir}/bundle-info" "${output_dir}/package-lock.sha256"
while IFS= read -r relative; do
  (cd -- "${output_dir}" && sha256sum "${relative}")
done < <(cd -- "${output_dir}" && find . -type f ! -name files.sha256 -print | sed 's#^./##' | sort) >"${output_dir}/files.sha256"
chmod 0644 "${output_dir}/files.sha256"
(cd -- "${output_dir}" && sha256sum --check --strict files.sha256 >/dev/null)
archive_path="${output_dir}.tar.gz"
[[ ! -e "${archive_path}" ]] || { printf 'refusing to overwrite existing Bundle archive: %s\n' "${archive_path}" >&2; exit 73; }
tar -C "${output_dir}" -czf "${archive_path}" manifest.yaml scripts database packages bundle-info package-lock.sha256 repository-lock files.sha256
archive_size="$(stat -c '%s' "${archive_path}")"
if ((archive_size > max_bundle_bytes)); then
  printf 'OFFLINE_BUNDLE_TOO_LARGE: archive is %s bytes, above the configured Panel import limit of %s bytes: %s\n' "${archive_size}" "${max_bundle_bytes}" "${archive_path}" >&2
  exit 75
fi
printf 'ClamAV offline Bundle created: %s\nArchive for Panel upload: %s (%s bytes)\n' "${output_dir}" "${archive_path}" "${archive_size}"
