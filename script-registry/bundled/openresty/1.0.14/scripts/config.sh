#!/usr/bin/env bash
# shellcheck source=common.sh
set -Eeuo pipefail
source "$(dirname "$0")/common.sh"

config_file="$(openresty_main_config || true)"
site_config_file="${install_dir}/nginx/conf/conf.d/default.conf"
operation="${ONEINSTACK_CONFIG_OPERATION:-get}"
expected_revision="${ONEINSTACK_CONFIG_REVISION:-}"
backup_root="${state_dir}/config-backups"

revision() {
  {
    sha256sum "${config_file}"
    if [[ -f "${site_config_file}" ]]; then
      sha256sum "${site_config_file}"
    else
      printf 'missing  %s\n' "${site_config_file}"
    fi
  } | sha256sum | awk '{print $1}'
}
read_value() {
  local expression="$1" fallback="$2" value
  value="$(sed -nE "${expression}" "${config_file}" | head -n1)"
  printf '%s' "${value:-${fallback}}"
}
sed_escape_replacement() {
  printf '%s' "$1" | sed 's/[\\&|]/\\&/g'
}
normalize_configured_web_root() {
  local value="${1%/}"
  if [[ "${value}" =~ ^/data/wwwroot(/default)+$ ]]; then
    printf '/data/wwwroot'
  else
    printf '%s' "${value}"
  fi
}
normalize_detected_web_root() {
  local value="${1%/}"
  if [[ "${value}" =~ ^/data/wwwroot(/default)+$ ]]; then
    printf '/data/wwwroot'
  elif [[ "${value}" == */default ]]; then
    printf '%s' "${value%/default}"
  else
    printf '%s' "${value}"
  fi
}
prune_backups() {
  local -a backups=()
  mapfile -t backups < <(find "${backup_root}" -mindepth 1 -maxdepth 1 -type d -printf '%T@ %p\n' 2>/dev/null | sort -rn | cut -d' ' -f2-)
  local index
  for ((index=20; index<${#backups[@]}; index++)); do rm -rf -- "${backups[index]}"; done
}

web_root="$(normalize_configured_web_root "${web_root}")"
validate_inputs
runtime_binary="$(openresty_runtime_binary || true)"
[[ -f "${config_file}" && -n "${runtime_binary}" ]] || die "OpenResty configuration is unavailable."

if [[ "${operation}" == "get" ]]; then
  worker_processes="$(read_value 's/^[[:space:]]*worker_processes[[:space:]]+([^;]+);/\1/p' auto)"
  worker_connections="$(grep -Eo 'worker_connections[[:space:]]+[0-9]+' "${config_file}" | head -n1 | grep -Eo '[0-9]+' || true)"
  keepalive_timeout="$(read_value 's/^[[:space:]]*keepalive_timeout[[:space:]]+([0-9]+);/\1/p' 65)"
  client_max_body_size="$(read_value 's/^[[:space:]]*client_max_body_size[[:space:]]+([0-9]+)[mM];/\1/p' 1)"
  if [[ -f "${site_config_file}" ]]; then
    detected_port="$(sed -nE 's/^[[:space:]]*listen[[:space:]]+([0-9]+)[[:space:]]+default_server.*;/\1/p' "${site_config_file}" | head -n1)"
    [[ -z "${detected_port}" ]] || openresty_port="${detected_port}"
    detected_web_root="$(sed -nE 's|^[[:space:]]*root[[:space:]]+([^;]+);|\1|p' "${site_config_file}" | head -n1)"
    if [[ -n "${detected_web_root}" && ! -r "${install_parameters_file}" ]]; then
      web_root="$(normalize_detected_web_root "${detected_web_root}")"
    fi
  fi
  detected_identity="$(sed -nE 's/^[[:space:]]*user[[:space:]]+([^;[:space:]]+)([[:space:]]+([^;[:space:]]+))?;.*/\1\t\3/p' "${config_file}" | head -n1)"
  if [[ -n "${detected_identity}" ]]; then
    IFS=$'\t' read -r detected_user detected_group <<<"${detected_identity}"
    run_user="${detected_user}"
    [[ -z "${detected_group}" ]] || run_group="${detected_group}"
  fi
  detected_log_dir="$(sed -nE 's|^[[:space:]]*error_log[[:space:]]+(.+)/openresty-error\.log([[:space:]].*)?;[[:space:]]*$|\1|p' "${config_file}" | head -n1)"
  [[ -z "${detected_log_dir}" ]] || log_dir="${detected_log_dir}"
  printf 'component=openresty\nrevision=%s\napply_mode=reload\n' "$(revision)"
  printf 'workerProcesses=%s\nworkerConnections=%s\nkeepaliveTimeout=%s\nclientMaxBodySize=%s\n' \
    "${worker_processes}" "${worker_connections:-4096}" "${keepalive_timeout}" "${client_max_body_size}"
  printf 'openrestyPort=%s\nphpFpmSocket=%s\ninstallDir=%s\nwebRoot=%s\nlogDir=%s\nrunUser=%s\nrunGroup=%s\n' \
    "${openresty_port}" "${php_fpm_socket}" "${install_dir}" "${web_root}" "${log_dir}" "${run_user}" "${run_group}"
  printf 'runtime.port=%s\nruntime.installDir=%s\nruntime.dataDir=\nruntime.logDir=%s\nruntime.runUser=%s\nruntime.runGroup=%s\n' \
    "${openresty_port}" "${install_dir}" "${log_dir}" "${run_user}" "${run_group}"
  exit 0
fi

if [[ -f "${site_config_file}" ]]; then
  current_configured_port="$(configured_default_site_port "${site_config_file}" || true)"
  [[ -z "${current_configured_port}" ]] || openresty_port="${current_configured_port}"
fi
[[ "${operation}" == "apply" ]] || die "Unsupported configuration operation."
require_root
[[ "${expected_revision}" =~ ^[0-9a-f]{64}$ ]] || die "Invalid configuration revision."
[[ "$(revision)" == "${expected_revision}" ]] || {
  printf 'Configuration changed since preview; refresh and try again.\n' >&2
  exit 75
}

worker_processes="${ONEINSTACK_CONFIG_WORKER_PROCESSES:-}"
worker_connections="${ONEINSTACK_CONFIG_WORKER_CONNECTIONS:-}"
keepalive_timeout="${ONEINSTACK_CONFIG_KEEPALIVE_TIMEOUT:-}"
client_max_body_size="${ONEINSTACK_CONFIG_CLIENT_MAX_BODY_SIZE:-}"
target_openresty_port="${ONEINSTACK_CONFIG_OPENRESTY_PORT:-${openresty_port}}"
target_php_fpm_socket="${ONEINSTACK_CONFIG_PHP_FPM_SOCKET:-${php_fpm_socket}}"
target_install_dir="${ONEINSTACK_CONFIG_INSTALL_DIR:-${install_dir}}"
target_web_root="$(normalize_configured_web_root "${ONEINSTACK_CONFIG_WEB_ROOT:-${web_root}}")"
target_log_dir="${ONEINSTACK_CONFIG_LOG_DIR:-${log_dir}}"
target_run_user="${ONEINSTACK_CONFIG_RUN_USER:-${run_user}}"
target_run_group="${ONEINSTACK_CONFIG_RUN_GROUP:-${run_group}}"
[[ "${worker_processes}" == "auto" || "${worker_processes}" =~ ^[1-9][0-9]?$ ]] || die "Invalid workerProcesses."
[[ "${worker_connections}" =~ ^[0-9]+$ && "${worker_connections}" -ge 512 && "${worker_connections}" -le 65535 ]] || die "Invalid workerConnections."
[[ "${keepalive_timeout}" =~ ^[0-9]+$ && "${keepalive_timeout}" -ge 5 && "${keepalive_timeout}" -le 300 ]] || die "Invalid keepaliveTimeout."
[[ "${client_max_body_size}" =~ ^[0-9]+$ && "${client_max_body_size}" -ge 1 && "${client_max_body_size}" -le 10240 ]] || die "Invalid clientMaxBodySize."
[[ "${target_openresty_port}" =~ ^[0-9]+$ && "${target_openresty_port}" -ge 1 && "${target_openresty_port}" -le 65535 ]] || die "Invalid openrestyPort."
validate_path "${target_php_fpm_socket}" PHP_FPM_SOCKET
[[ "${target_install_dir}" == "${install_dir}" ]] || die "INSTALL_DIR cannot be changed after OpenResty installation; reinstall with the new directory."
validate_path "${target_web_root}" WEB_ROOT
validate_path "${target_log_dir}" LOG_DIR
validate_identifier "${target_run_user}"
validate_identifier "${target_run_group}"
case "${target_web_root}${target_log_dir}" in
  *\\*) die "Configuration paths cannot contain backslashes." ;;
esac
if [[ "${target_openresty_port}" != "${openresty_port}" ]]; then
  require_command ss
  target_listener="$(ss -H -ltnp "sport = :${target_openresty_port}" 2>/dev/null || true)"
  [[ -z "${target_listener}" ]] || die "Port ${target_openresty_port} is already occupied; choose a free port."
fi
escaped_php_fpm_socket="$(sed_escape_replacement "${target_php_fpm_socket}")"
escaped_web_root="$(sed_escape_replacement "${target_web_root}")"
escaped_log_dir="$(sed_escape_replacement "${target_log_dir}")"
getent group "${target_run_group}" >/dev/null || groupadd --system "${target_run_group}"
id "${target_run_user}" >/dev/null 2>&1 ||
  useradd --system --gid "${target_run_group}" --home-dir /nonexistent --shell /usr/sbin/nologin "${target_run_user}"
emit_progress 8 config_snapshot "正在创建 OpenResty 配置快照"
install -d -m 0750 -- "${backup_root}"
backup_dir="$(mktemp -d "${backup_root}/config-$(date -u +%Y%m%dT%H%M%SZ)-XXXXXX")"
chmod 0700 "${backup_dir}"
cp -a -- "${config_file}" "${backup_dir}/nginx.conf"
[[ ! -f "${site_config_file}" ]] || cp -a -- "${site_config_file}" "${backup_dir}/default.conf"
printf '%s\n' "${expected_revision}" >"${backup_dir}/revision"
path_acl_transaction_file="${backup_dir}/path-acl-added"
: >"${path_acl_transaction_file}"
chmod 0600 "${path_acl_transaction_file}"

install -d -m 0755 -- "${target_web_root}" "${target_web_root}/default"
install -d -m 0750 -- "${target_log_dir}"
chown "${target_run_user}:${target_run_group}" "${target_web_root}" "${target_web_root}/default" "${target_log_dir}"

candidate="$(mktemp "$(dirname "${config_file}")/.oneinstack-openresty.XXXXXX")"
cp -p -- "${config_file}" "${candidate}"
sed -Ei \
  -e "s/^[[:space:]]*worker_processes[[:space:]]+[^;]+;/worker_processes ${worker_processes};/" \
  -e "s/worker_connections[[:space:]]+[0-9]+;/worker_connections ${worker_connections};/" \
  -e "s/^[[:space:]]*keepalive_timeout[[:space:]]+[0-9]+;/    keepalive_timeout ${keepalive_timeout};/" \
  -e "s|^[[:space:]]*user[[:space:]]+[^;]+;|user ${target_run_user} ${target_run_group};|" \
  -e "s|^[[:space:]]*error_log[[:space:]]+[^;]+;|error_log ${escaped_log_dir}/openresty-error.log warn;|" \
  -e "s|^[[:space:]]*access_log[[:space:]]+[^;]+;|    access_log ${escaped_log_dir}/openresty-access.log main;|" \
  "${candidate}"
if grep -Eq '^[[:space:]]*client_max_body_size[[:space:]]+' "${candidate}"; then
  sed -Ei "s/^[[:space:]]*client_max_body_size[[:space:]]+[^;]+;/    client_max_body_size ${client_max_body_size}m;/" "${candidate}"
else
  sed -i "/^[[:space:]]*server_tokens[[:space:]]/i\\    client_max_body_size ${client_max_body_size}m;" "${candidate}"
fi
site_candidate=""
if [[ -f "${site_config_file}" ]]; then
  site_candidate="$(mktemp "$(dirname "${site_config_file}")/.oneinstack-openresty-site.XXXXXX")"
  cp -p -- "${site_config_file}" "${site_candidate}"
  sed -Ei \
    -e "s|^([[:space:]]*listen[[:space:]]+)[0-9]+([[:space:]]+default_server.*;)|\\1${target_openresty_port}\\2|" \
    -e "s|^[[:space:]]*root[[:space:]]+[^;]+;|    root ${escaped_web_root}/default;|" \
	    -e "s|^[[:space:]]*fastcgi_pass[[:space:]]+unix:[^;]+;|        fastcgi_pass unix:${escaped_php_fpm_socket};|" \
    "${site_candidate}"
fi

emit_progress 35 config_validate "正在校验 OpenResty 候选配置"
candidate_root="$(mktemp -d "${state_dir}/.config-candidate.XXXXXX")"
cp -a -- "${install_dir}/nginx/conf" "${candidate_root}/conf"
install -m 0640 -- "${candidate}" "${candidate_root}/conf/nginx.conf"
if [[ -n "${site_candidate}" ]]; then
  install -m 0640 -- "${site_candidate}" "${candidate_root}/conf/conf.d/default.conf"
fi
if ! "${runtime_binary}" -t -p "${candidate_root}/" -c conf/nginx.conf; then
  rm -rf -- "${candidate_root}"
  rm -f -- "${candidate}" "${site_candidate:-}"
  printf 'OpenResty candidate configuration is invalid.\n' >&2
  exit 65
fi
rm -rf -- "${candidate_root}"
reload_configured_service() { reload_openresty; }
was_active=false
openresty_is_running && was_active=true
committed=false
rollback() {
  local code="${1:-$?}"
  set +e
  if [[ "${committed}" == "true" ]]; then
    restore="$(mktemp "$(dirname "${config_file}")/.oneinstack-openresty-restore.XXXXXX")"
    cp -p -- "${backup_dir}/nginx.conf" "${restore}"
    mv -f -- "${restore}" "${config_file}"
    if [[ -f "${backup_dir}/default.conf" ]]; then
      restore_site="$(mktemp "$(dirname "${site_config_file}")/.oneinstack-openresty-site-restore.XXXXXX")"
      cp -p -- "${backup_dir}/default.conf" "${restore_site}"
      mv -f -- "${restore_site}" "${site_config_file}"
    fi
    [[ "${was_active}" == "true" ]] && reload_configured_service
  fi
  rm -f -- "${candidate:-}" "${site_candidate:-}"
  restore_transaction_path_acl "${path_acl_transaction_file}"
  exit "${code}"
}
trap 'rollback $?' ERR
trap 'rollback 130' INT
trap 'rollback 143' TERM

ensure_runtime_path_traversal "${target_run_user}" "${target_web_root}" "WEB_ROOT"
ensure_runtime_path_traversal "${target_run_user}" "${target_log_dir}" "LOG_DIR"

emit_progress 62 config_publish "正在原子发布 OpenResty 配置"
committed=true
mv -f -- "${candidate}" "${config_file}"
[[ -z "${site_candidate}" ]] || mv -f -- "${site_candidate}" "${site_config_file}"
"${runtime_binary}" -t -p "${install_dir}/nginx/" -c "${config_file}"
if [[ "${was_active}" == "true" ]]; then
  emit_progress 82 config_reload "正在平滑重载 OpenResty"
  reload_configured_service
  openresty_is_running || die "OpenResty did not remain active after configuration reload."
fi
openresty_port="${target_openresty_port}"
web_root="${target_web_root}"
log_dir="${target_log_dir}"
php_fpm_socket="${target_php_fpm_socket}"
run_user="${target_run_user}"
run_group="${target_run_group}"
verify_runtime_permissions
persist_install_parameters
rm -f -- "${path_acl_transaction_file}"
path_acl_transaction_file=""
trap - ERR INT TERM
prune_backups
emit_progress 100 config_applied "OpenResty 配置已生效"
printf 'Configuration backup: %s\n' "$(basename "${backup_dir}")"
