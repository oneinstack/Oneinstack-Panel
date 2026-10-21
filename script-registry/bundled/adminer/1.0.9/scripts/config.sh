#!/usr/bin/env bash
set -Eeuo pipefail
# shellcheck source-path=SCRIPTDIR
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"

require_root
[[ -f "${installed_state}" && -r "${runtime_state}" ]] || die_code ADMINER_STATE_MISSING "Managed Adminer installation state is missing."
load_runtime_state
validate_common
validate_runtime_prerequisites

print_configuration() {
  local scheme=http access_url
  [[ "${web_port}" == 443 ]] && scheme=https
  access_url="${scheme}://${web_host}:${web_port}${public_path}"
  printf 'component=adminer\nrevision=%s\napply_mode=reload\n' "$(config_revision)"
  printf 'publicPath=%s\naccessPolicy=%s\nallowedCidrs=%s\ndefaultDriver=%s\ndefaultServer=%s\n' "${public_path}" "${access_policy}" "${allowed_cidrs}" "${default_driver}" "${default_server}"
  printf 'runtime.port=%s\nruntime.bindAddress=%s\nruntime.socketPath=%s\nruntime.installDir=%s\n' "${web_port}" "${web_host}" "${php_socket}" "${install_dir}"
  printf 'runtime.configFile=%s\nruntime.serviceName=%s\nruntime.version=%s\n' "${web_config}" "${web_service}" "${software_version}"
  printf 'runtime.packageVersion=%s\nruntime.artifactSha256=%s\nruntime.publicPath=%s\n' "${package_version}" "${source_sha256}" "${public_path}"
  printf 'runtime.accessUrl=%s\nruntime.webServer=%s\nruntime.documentRoot=%s\n' "${access_url}" "${web_component}" "${web_document_root}"
  printf 'runtime.phpVersion=%s\nruntime.phpService=%s\n' "${php_version}" "${php_service}"
}

operation="${ONEINSTACK_CONFIG_OPERATION:-get}"
case "${operation}" in
  get)
    verify_managed_installation
    print_configuration
    ;;
  apply)
    expected_revision="${ONEINSTACK_CONFIG_REVISION:-}"
    [[ "${expected_revision}" =~ ^[0-9a-f]{64}$ ]] || die_code ADMINER_CONFIG_REVISION_INVALID "A valid configuration revision is required."
    [[ "${expected_revision}" == "$(config_revision)" ]] || die_code ADMINER_CONFIG_REVISION_CONFLICT "Adminer configuration changed; reload it before applying."
    old_route_path="${route_path}"
    public_path="${ONEINSTACK_CONFIG_PUBLIC_PATH:-}"
    access_policy="${ONEINSTACK_CONFIG_ACCESS_POLICY:-}"
    allowed_cidrs="${ONEINSTACK_CONFIG_ALLOWED_CIDRS:-}"
    default_driver="${ONEINSTACK_CONFIG_DEFAULT_DRIVER:-}"
    default_server="${ONEINSTACK_CONFIG_DEFAULT_SERVER:-}"
    validate_configuration_values
    detect_managed_php
    detect_managed_web_server
    check_unmanaged_web_servers
    check_route_conflict
    config_backup="${state_dir}/config-rollback"
    rm -rf -- "${config_backup}"
    install -d -m 0700 -- "${config_backup}"
    php_path_acl_transaction_file="${config_backup}/managed-php-path-acl.added"
    cp -a -- "${install_dir}/index.php" "${install_dir}/adminer-plugins.php" "${install_dir}/oneinstack-config.php" "${runtime_state}" "${installed_state}" "${config_backup}/"
    new_route_created=false
    restore_config() {
      local rc=$?
      trap - ERR
      cp -a -- "${config_backup}/index.php" "${install_dir}/index.php" 2>/dev/null || true
      cp -a -- "${config_backup}/adminer-plugins.php" "${install_dir}/adminer-plugins.php" 2>/dev/null || true
      cp -a -- "${config_backup}/oneinstack-config.php" "${install_dir}/oneinstack-config.php" 2>/dev/null || true
      cp -a -- "${config_backup}/runtime-config" "${runtime_state}" 2>/dev/null || true
      cp -a -- "${config_backup}/installed.json" "${installed_state}" 2>/dev/null || true
      if [[ "${new_route_created}" == true && "${route_path}" != "${old_route_path}" ]]; then remove_managed_public_route "${route_path}" 2>/dev/null || true; fi
      restore_php_path_acl_transaction "${config_backup}/managed-php-path-acl.added"
      exit "${rc}"
    }
    trap restore_config ERR
    config_candidate="${install_dir}/.oneinstack-config.php.${BASHPID}"
    index_candidate="${install_dir}/.index.php.${BASHPID}"
    plugin_candidate="${install_dir}/.adminer-plugins.php.${BASHPID}"
    render_runtime_config "${config_candidate}"
    render_entrypoint "${index_candidate}"
    render_plugin_config "${plugin_candidate}"
    "${php_binary}" -l "${config_candidate}" >/dev/null
    "${php_binary}" -l "${index_candidate}" >/dev/null
    "${php_binary}" -l "${plugin_candidate}" >/dev/null
    mv -f -- "${config_candidate}" "${install_dir}/oneinstack-config.php"
    mv -f -- "${index_candidate}" "${install_dir}/index.php"
    mv -f -- "${plugin_candidate}" "${install_dir}/adminer-plugins.php"
    if [[ "${route_path}" != "${old_route_path}" ]]; then
      create_public_route
      new_route_created=true
    fi
    ensure_php_fpm_route_access
    verify_managed_installation
    if [[ "${route_path}" != "${old_route_path}" ]]; then
      remove_managed_public_route "${old_route_path}" ||
        die_code ADMINER_ROUTE_INVALID "The previous managed public route could not be removed safely."
    fi
    write_runtime_state
    write_installed_state
    rm -rf -- "${config_backup}"
    trap - ERR
    print_configuration
    ;;
  *) die_code ADMINER_CONFIG_OPERATION_INVALID "ONEINSTACK_CONFIG_OPERATION must be get or apply." ;;
esac
