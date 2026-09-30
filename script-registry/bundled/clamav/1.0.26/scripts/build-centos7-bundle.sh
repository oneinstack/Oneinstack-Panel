#!/usr/bin/env bash
# shellcheck disable=SC2154 # Container profile constants and version are sourced below.
set -Eeuo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
component_dir="$(cd -- "${script_dir}/.." && pwd)"
output_dir="${1:-}"
target_software_version="${2:-}"
target_system_id="${3:-}"
target_system_version="${4:-}"
target_arch="${5:-}"
database_source="${6:-}"
max_bundle_bytes="${ONEINSTACK_OFFLINE_BUNDLE_MAX_BYTES:-268435456}"

# shellcheck source=common.sh
source "${script_dir}/common.sh"
# shellcheck source=container.sh
source "${script_dir}/container.sh"
software_version="${target_software_version}"
system_id="${target_system_id}"
system_version="${target_system_version}"
host_arch="${target_arch}"

[[ "${software_version}" == latest && "${system_id}" == centos && "${system_version%%.*}" == 7 && "${host_arch}" == amd64 ]] ||
  die 'OFFLINE_BUNDLE_PLATFORM_MISMATCH: only CentOS Linux 7 amd64 is verified for the container profile.'
validate_path "${output_dir}" OUTPUT_DIR
[[ "${output_dir}" != "${component_dir}" && "${output_dir}" != "${component_dir}"/* ]] ||
  die 'OFFLINE_BUNDLE_INVALID: output directory cannot overlap component source.'
[[ ! -e "${output_dir}" && ! -e "${output_dir}.tar.gz" ]] ||
  die 'OFFLINE_BUNDLE_EXISTS: refusing to overwrite an existing Bundle.'
[[ -d "${database_source}" && ! -L "${database_source}" ]] || die 'OFFLINE_DATABASE_MISSING: database directory is unavailable.'
[[ "${max_bundle_bytes}" =~ ^[0-9]+$ && "${max_bundle_bytes}" -ge 157286400 ]] ||
  die 'OFFLINE_BUNDLE_TOO_LARGE: CentOS 7 image and signed databases require at least a 150 MiB limit.'
container_docker="$(command -v docker 2>/dev/null || true)"
[[ -n "${container_docker}" ]] || die 'HOST_DEPENDENCY_UNSUPPORTED: Docker is required to export the verified image.'
container_image_present || die 'RUNTIME_IMAGE_UNAVAILABLE: pinned ClamAV 1.4.6 image is not available locally.'
container_validate_database "${database_source}"
daily_file="$(find "${database_source}" -maxdepth 1 -type f \( -name daily.cvd -o -name daily.cld \) -print -quit)"
database_timestamp="$("${container_docker}" run --rm --pull never --network none --read-only --cap-drop ALL --user "${container_uid}:${container_gid}" \
  --mount "type=bind,source=${database_source},target=/oneinstack-database,readonly" \
  --entrypoint /usr/bin/sigtool "${container_image_id}" --info "/oneinstack-database/$(basename -- "${daily_file}")" |
  awk -F': ' '/^Build time:/ && !seen {print $2; seen=1}')"
database_timestamp="$(date -u -d "${database_timestamp}" +%s 2>/dev/null || true)"
now="$(date -u +%s)"
[[ "${database_timestamp}" =~ ^[0-9]+$ ]] &&
  ((database_timestamp <= now && now - database_timestamp <= offline_database_max_age_hours * 3600)) ||
  die 'OFFLINE_DATABASE_EXPIRED: daily signature database must be no older than seven days.'

install -d -m 0750 -- "${output_dir}" "${output_dir}/scripts"
install -d -m 0755 -- "${output_dir}/database"
install -m 0644 -- "${component_dir}/manifest.yaml" "${output_dir}/manifest.yaml"
find "${component_dir}/scripts" -maxdepth 1 -type f -name '*.sh' -exec install -m 0755 -- '{}' "${output_dir}/scripts/" \;
for stem in main daily bytecode; do
  file="$(find "${database_source}" -maxdepth 1 -type f \( -name "${stem}.cvd" -o -name "${stem}.cld" \) -print -quit)"
  install -m 0644 -- "${file}" "${output_dir}/database/$(basename -- "${file}")"
done
"${container_docker}" save "${container_image_id}" | gzip -1 >"${output_dir}/image.tar.gz"
printf 'component=clamav\npackageVersion=%s\nsoftwareVersion=latest\nosId=centos\nosVersion=%s\narchitecture=amd64\nimageId=%s\ndatabaseGeneratedAtUnix=%s\n' \
  "${package_version}" "${system_version}" "${container_image_id}" "${database_timestamp}" >"${output_dir}/bundle-info"
while IFS= read -r relative; do
  (cd -- "${output_dir}" && sha256sum "${relative}")
done < <(cd -- "${output_dir}" && find . -type f ! -name files.sha256 -print | sed 's#^./##' | sort) >"${output_dir}/files.sha256"
(cd -- "${output_dir}" && sha256sum --check --strict files.sha256 >/dev/null)
tar -C "${output_dir}" -czf "${output_dir}.tar.gz" manifest.yaml scripts database image.tar.gz bundle-info files.sha256
archive_size="$(stat -c '%s' "${output_dir}.tar.gz")"
((archive_size <= max_bundle_bytes)) ||
  die "OFFLINE_BUNDLE_TOO_LARGE: archive is ${archive_size} bytes, above the limit of ${max_bundle_bytes} bytes."
printf 'ClamAV CentOS 7 offline Bundle: %s\nSHA-256: %s\nSize: %s bytes\n' \
  "${output_dir}.tar.gz" "$(sha256sum "${output_dir}.tar.gz" | awk '{print $1}')" "${archive_size}"
