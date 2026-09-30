#!/usr/bin/env bash
set -Eeuo pipefail

usage() {
  printf 'usage: %s OUTPUT SOFTWARE_VERSION OS_ID OS_VERSION ARCH SOURCE_DIR PACKAGE_DIR\n' "$0" >&2
  exit 64
}
[[ "$#" -eq 7 ]] || usage
output="$1"
software_version="$2"
os_id="$3"
os_version="$4"
architecture="$5"
source_dir="$6"
package_dir="$7"
package_version="2.0.18"
component_root="$(cd "$(dirname "$0")/.." && pwd -P)"

die() { printf 'TOMCAT_BUNDLE_ERROR: %s\n' "$*" >&2; exit 1; }
[[ "${output}" == /* && "${output}" != / ]] || die "OUTPUT must be an absolute directory"
[[ "${source_dir}" == /* && -d "${source_dir}" ]] || die "SOURCE_DIR must be an existing absolute directory"
[[ "${package_dir}" == /* && -d "${package_dir}" ]] || die "PACKAGE_DIR must be an existing absolute directory"
[[ "${software_version}" =~ ^(7\.0\.109|8\.5\.96|9\.0\.113|10\.1\.50|11\.0.15)$ ]] || die "TOMCAT_ARTIFACT_MISSING: unsupported version"
[[ "${architecture}" == amd64 || "${architecture}" == arm64 ]] || die "HOST_PLATFORM_UNSUPPORTED: architecture"

case "${os_id}" in
  rhel|rocky|almalinux|ol|centos|centos-stream|sles) os_version="${os_version%%.*}" ;;
esac

case "${os_id}:${os_version}" in
  ubuntu:22.04|ubuntu:24.04|ubuntu:26.04|debian:11|debian:12|debian:13|\
  rhel:8|rhel:9|rhel:10|rocky:8|rocky:9|rocky:10|almalinux:8|almalinux:9|almalinux:10|\
  ol:8|ol:9|ol:10|centos:7|centos-stream:8|centos-stream:9|centos-stream:10|\
  amzn:2023|sles:15|sles:16|fedora:*|opensuse-leap:*|opensuse-tumbleweed:*|opensuse:*) ;;
  *) die "HOST_PLATFORM_UNSUPPORTED: ${os_id} ${os_version}" ;;
esac

case "${os_id}" in
  ubuntu|debian) package_extension="deb" ;;
  *) package_extension="rpm" ;;
esac
find_source() {
  local name="$1" result
  result="$(find "${source_dir}" -maxdepth 1 -type f -name "${name}" -print -quit)"
  [[ -n "${result}" ]] || return 1
  printf '%s' "${result}"
}

expected_sha256() {
  local runtime="$1"
  if [[ "${runtime}" == tomcat ]]; then
    case "${software_version}" in
      7.0.109) printf '%s' ebfeb051e6da24bce583a4105439bfdafefdc7c5bdd642db2ab07e056211cb31 ;;
      8.5.96) printf '%s' 0307cfb85e58a2ff3d033d464bdc01f03fd5452618b17baf64368b1e6b02e886 ;;
      9.0.113) printf '%s' 790db2b8092b7954dec2afc6af71a7bbb6c67998198516dd6a9f865661b5d2a7 ;;
      10.1.50) printf '%s' f74f9f1a7ac2cf6eeede2c50f45088d9c3e55f77d5777f9f7033ed3d43ef529c ;;
      11.0.15) printf '%s' c515a0edb273846b4d7926fa8175aaa46905f45d5e2af588e01783e35a89a69c ;;
    esac
  elif [[ "${jdk_version}:${architecture}" == 8:amd64 ]]; then
    printf '%s' 9c70e102f527ac674ac2fe9c7d47b9a04e2d19842ba5ab8e9b33f368bbadfaea
  elif [[ "${jdk_version}:${architecture}" == 8:arm64 ]]; then
    printf '%s' 57b7ed8af9d48542bb49ff7894448040b17bea0a48b41677d11ecaec6129768d
  elif [[ "${jdk_version}:${architecture}" == 17:amd64 ]]; then
    printf '%s' 992f96e7995075ac7636bb1a8de52b0c61d71ed3137fafc979ab96b4ab78dd75
  else
    printf '%s' dc29ca6d35beb4419b4b00419b8a3dfbf5ae551e1ae2b046b516d9a579d04533
  fi
}

tomcat_source=""
for candidate in "tomcat-${software_version}-${architecture}.tar.gz" "apache-tomcat-${software_version}.tar.gz"; do
  tomcat_source="$(find_source "${candidate}" || true)"
  [[ -n "${tomcat_source}" ]] && break
done
[[ -n "${tomcat_source}" ]] || die "TOMCAT_ARTIFACT_MISSING: Tomcat archive"

if [[ "${software_version}" == 7.* || "${software_version}" == 8.5.* ]]; then jdk_version="8"; else jdk_version="17"; fi
case "${jdk_version}:${architecture}" in
  8:amd64) jdk_source="$(find_source 'jdk8-amd64.tar.gz' || find_source 'OpenJDK8U-jdk_x64_linux_hotspot_8u504b01.tar.gz' || true)" ;;
  8:arm64) jdk_source="$(find_source 'jdk8-arm64.tar.gz' || find_source 'OpenJDK8U-jdk_aarch64_linux_hotspot_8u504b01.tar.gz' || true)" ;;
  17:amd64) jdk_source="$(find_source 'jdk17-amd64.tar.gz' || find_source 'OpenJDK17U-jdk_x64_linux_hotspot_17.0.17_10.tar.gz' || true)" ;;
  17:arm64) jdk_source="$(find_source 'jdk17-arm64.tar.gz' || find_source 'OpenJDK17U-jdk_aarch64_linux_hotspot_17.0.17_10.tar.gz' || true)" ;;
esac
[[ -n "${jdk_source}" ]] || die "TOMCAT_ARTIFACT_MISSING: Temurin JDK ${jdk_version}/${architecture} archive"
[[ "$(sha256sum "${tomcat_source}" | awk '{print $1}')" == "$(expected_sha256 tomcat)" ]] || die "OFFLINE_BUNDLE_CHECKSUM_MISMATCH: Tomcat archive"
[[ "$(sha256sum "${jdk_source}" | awk '{print $1}')" == "$(expected_sha256 jdk)" ]] || die "OFFLINE_BUNDLE_CHECKSUM_MISMATCH: JDK archive"

package_files=()
while IFS= read -r package; do package_files+=("${package}"); done < <(find "${package_dir}" -maxdepth 1 -type f -name "*.${package_extension}" -print | sort)
((${#package_files[@]} > 0)) || die "OFFLINE_DEPENDENCY_MISSING: no .${package_extension} packages"
if [[ "${package_extension}" == deb ]]; then
  required_packages=(acl ca-certificates curl gzip iproute2 policycoreutils procps tar util-linux xz-utils)
elif [[ "${os_id}" == sles || "${os_id}" == opensuse* ]]; then
  required_packages=(acl ca-certificates curl gzip iproute2 policycoreutils procps tar util-linux xz)
else
  required_packages=(acl ca-certificates curl gzip iproute policycoreutils procps-ng tar util-linux xz)
fi
for required_package in "${required_packages[@]}"; do
  if [[ "${package_extension}" == deb ]]; then package_pattern="${required_package}_*.deb"; else package_pattern="${required_package}-[0-9]*.rpm"; fi
  find "${package_dir}" -maxdepth 1 -type f -name "${package_pattern}" -print -quit | grep -q . || die "OFFLINE_DEPENDENCY_MISSING: ${required_package} package"
done

rm -rf -- "${output}"
mkdir -p "${output}/scripts" "${output}/artifacts/amd64" "${output}/artifacts/arm64" "${output}/packages/${os_id}/${os_version}/${architecture}"
cp -p -- "${component_root}/manifest.yaml" "${output}/manifest.yaml"
while IFS= read -r script; do
  [[ -f "${script}" ]] || continue
  cp -p -- "${script}" "${output}/scripts/$(basename "${script}")"
  chmod 0755 "${output}/scripts/$(basename "${script}")"
done < <(find "${component_root}/scripts" -maxdepth 1 -type f -print | sort)
cp -p -- "${tomcat_source}" "${output}/artifacts/${architecture}/tomcat-${software_version}-${architecture}.tar.gz"
cp -p -- "${jdk_source}" "${output}/artifacts/${architecture}/jdk${jdk_version}-${architecture}.tar.gz"
for package in "${package_files[@]}"; do cp -p -- "${package}" "${output}/packages/${os_id}/${os_version}/${architecture}/"; done

cat >"${output}/bundle-info" <<EOF
component=tomcat
packageVersion=${package_version}
softwareVersion=${software_version}
jdkVersion=${jdk_version}
osId=${os_id}
osVersion=${os_version}
architecture=${architecture}
EOF
chmod 0640 "${output}/bundle-info"

(
  cd "${output}"
  : >files.sha256
  while IFS= read -r relative; do
    [[ "${relative}" == files.sha256 ]] && continue
    sha256sum "${relative}" >>files.sha256
  done < <(find . -type f ! -name files.sha256 -print | sed 's#^\./##' | sort)
)
chmod 0640 "${output}/files.sha256"

while IFS= read -r path; do
  [[ -L "${path}" || -f "${path}" || -d "${path}" ]] || die "OFFLINE_BUNDLE_INVALID: special file"
  [[ -L "${path}" ]] && die "OFFLINE_BUNDLE_INVALID: symlink ${path}"
done < <(find "${output}" -mindepth 1 -print)
(cd "${output}" && sha256sum -c files.sha256 >/dev/null) || die "OFFLINE_BUNDLE_CHECKSUM_MISMATCH"
printf 'Tomcat offline bundle created: %s\n' "${output}"
