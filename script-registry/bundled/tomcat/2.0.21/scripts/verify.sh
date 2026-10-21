#!/usr/bin/env bash
set -Eeuo pipefail
source "$(dirname "$0")/common.sh"
require_root
load_persisted_parameters
validate_host
validate_scalar_inputs
validate_runtime
printf 'component=tomcat\nsoftwareVersion=%s\njdkVersion=%s\njavaHome=%s\nservice=active\nport=%s\n' \
  "${software_version}" "${jdk_version}" "${java_home}" "${tomcat_port}"
