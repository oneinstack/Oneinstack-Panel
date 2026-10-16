#!/usr/bin/env bash
set -Eeuo pipefail
source "$(dirname "$0")/common.sh"
require_root
validate_inputs
validate_install_port
if [[ "${install_mode}" == offline ]]; then
  printf 'component=tomcat\npackageVersion=%s\nsoftwareVersion=%s\njdkVersion=%s\nosId=%s\nosVersion=%s\narchitecture=%s\nmode=offline\n' \
    "${package_version}" "${software_version}" "${jdk_version}" "${os_id}" "${os_version}" "${architecture}"
else
  printf 'component=tomcat\npackageVersion=%s\nsoftwareVersion=%s\njdkVersion=%s\nosId=%s\nosVersion=%s\narchitecture=%s\nmode=center\n' \
    "${package_version}" "${software_version}" "${jdk_version}" "${os_id}" "${os_version}" "${architecture}"
fi
