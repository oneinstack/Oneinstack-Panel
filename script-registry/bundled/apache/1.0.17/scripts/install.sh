#!/usr/bin/env bash
set -Eeuo pipefail
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"
require_root
validate_inputs
validate_database_password
check_host
check_component_prerequisites
if [[ -f "${state_dir}/installed.json" ]] && verify_installed; then
  disable_service

  emit_progress 100 already_installed "${component_name} ${software_version} is already installed"
  exit 0
fi
if [[ "${component_id}" == "java" && ! -f "${state_dir}/installed.json" && -e "${install_dir}" ]]; then
  die "External Java runtime ownership cannot be identified safely; refusing to replace the shared JVM directory."
fi
snapshot_existing_component
	trap cleanup_work EXIT
	if [[ "${component_id}" == "phpmyadmin" && "${install_mode}" == "offline" ]]; then
		emit_progress 15 fetch_offline_bundle "Reading phpMyAdmin offline bundle"
		install_phpmyadmin_offline
	elif [[ "${component_id}" == "phpmyadmin" ]]; then
		emit_progress 15 fetch_phpmyadmin "Fetching verified phpMyAdmin release"
		install_phpmyadmin_online
	else
		emit_progress 15 fetch_oneinstack "Fetching pinned OneinStack installer"
		download_upstream
		patch_upstream_java_installers
		patch_upstream_optional_database_service_actions
		patch_upstream_caddy_service_actions
		prepare_verified_source_override
	fi
upstream_status=0
if [[ "${component_id}" == "phpmyadmin" ]]; then
  if [[ "${install_mode}" == "offline" ]]; then
    emit_progress 30 offline_install_completed "Installed phpMyAdmin from the verified offline bundle"
  else
    emit_progress 30 phpmyadmin_install_completed "Installed phpMyAdmin from the verified release archive"
  fi
else
  prepare_java_runtime
  prepare_tomcat_runtime
  prepare_php_sources
  prepare_mongodb_sources
  patch_mongodb_installer
  patch_database_secret_transport
  mapfile -t arguments < <(install_arguments)
  emit_progress 30 run_oneinstack "Running independent ${component_name} OneinStack installation"
  (
    cd "${work_dir}"
    export TERM=xterm DEBIAN_FRONTEND=noninteractive
    bash ./install.sh "${arguments[@]}"
  ) || upstream_status=$?
fi
normalize_runtime_permissions
secure_database_network
if [[ "${component_id}" == "phpmyadmin" ]]; then
  phpmyadmin_configure_web_route
fi
prepare_web_service_unit
if declare -F migrate_existing_web_server_configs >/dev/null 2>&1; then
  migrate_existing_web_server_configs
fi
emit_progress 90 verify_installation "Verifying ${component_name} installation"
if ! verify_installed; then
  if declare -F caddy_verification_diagnostics >/dev/null 2>&1; then
    caddy_verification_diagnostics
  fi
  mongodb_service_diagnostics
  if [[ "${upstream_status}" -ne 0 ]]; then
    die "${component_name} installer exited with status ${upstream_status} and target verification failed."
  fi
  die "${component_name} installation verification failed."
fi
if declare -F migrate_legacy_web_service >/dev/null 2>&1; then
  migrate_legacy_web_service
fi
disable_service
if [[ "${upstream_status}" -ne 0 ]]; then
  printf 'WARNING: OneinStack exited with status %s after %s became healthy; continuing with verified target.\n' \
    "${upstream_status}" "${component_name}" >&2
fi
write_state
commit_existing_component
emit_progress 100 install_completed "${component_name} ${software_version} installed"
