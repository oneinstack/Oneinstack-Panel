#!/usr/bin/env bash
set -Eeuo pipefail
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"
require_root
validate_inputs
[[ -f "${state_dir}/installed.json" ]] ||
  die "Managed installation state is missing; refusing to remove unowned resources."
disable_service
systemctl disable --now mysqld 2>/dev/null || true
rm -f -- /etc/init.d/mysqld /etc/rc*.d/*mysqld
if [[ "${component_id}" == "apache" ]] && declare -F remove_managed_apache_init_scripts >/dev/null 2>&1; then
  remove_managed_apache_init_scripts
fi
if declare -F snapshot_web_server_config >/dev/null 2>&1; then
  snapshot_web_server_config
fi
preserve_component_data
remove_component_symlinks
if [[ "${component_id}" == "java" ]]; then
  remove_managed_java_runtime
else
  rm -rf -- "${install_dir}"
fi
case "${component_id}" in
  openresty) rm -f -- /etc/init.d/nginx ;;
  tomcat) rm -f -- /etc/init.d/tomcat /etc/logrotate.d/tomcat ;;
  nodejs) rm -f -- /etc/profile.d/nodejs.sh ;;
  memcached) rm -f -- /usr/bin/memcached ;;
esac
if [[ -n "${service_name}" ]]; then
  rm -f -- "/etc/systemd/system/${service_name}.service" "/lib/systemd/system/${service_name}.service"
  systemctl daemon-reload
  systemctl reset-failed "${service_name}.service" 2>/dev/null || true
fi
if [[ -n "${data_dir}" && "${preserve_data}" == "false" ]]; then
  rm -rf -- "${data_dir}"
fi

rm -f -- "${state_dir}/installed.json"
rmdir "${state_dir}" 2>/dev/null || true
emit_progress 100 uninstall_completed "${component_name} managed files removed"
