#!/usr/bin/env bash
set -Eeuo pipefail
source "$(dirname "$0")/common.sh"

operation="${ONEINSTACK_CONFIG_OPERATION:-get}"
expected_revision="${ONEINSTACK_CONFIG_REVISION:-}"
backup_root="${state_dir}/config-backups"

effective_ini_file="${managed_ini_file}"
[[ -f "${effective_ini_file}" ]] || effective_ini_file="${php_ini_file}"
effective_pool_file="${pool_config_file}"
[[ -f "${effective_pool_file}" ]] || effective_pool_file="${install_dir}/etc/php-fpm.d/www.conf"

revision() {
  {
    sha256sum "${effective_ini_file}"
    sha256sum "${effective_pool_file}"
    sha256sum "${fpm_config_file}"
  } | sha256sum | awk '{print $1}'
}

php_number_mb() {
  local key="$1" fallback="$2" value
  value="$(sed -nE "s/^[[:space:]]*${key}[[:space:]]*=[[:space:]]*([0-9]+)[mM].*/\1/p" "${effective_ini_file}" | tail -n1)"
  printf '%s' "${value:-${fallback}}"
}

php_number() {
  local key="$1" fallback="$2" value
  value="$(sed -nE "s/^[[:space:]]*${key}[[:space:]]*=[[:space:]]*([0-9]+).*/\1/p" "${effective_ini_file}" | tail -n1)"
  printf '%s' "${value:-${fallback}}"
}

fpm_number() {
  local key="$1" fallback="$2" value
  value="$(sed -nE "s/^[[:space:]]*${key}[[:space:]]*=[[:space:]]*([0-9]+).*/\1/p" "${effective_pool_file}" | tail -n1)"
  printf '%s' "${value:-${fallback}}"
}

prune_backups() {
  local -a backups=()
  mapfile -t backups < <(find "${backup_root}" -mindepth 1 -maxdepth 1 -type d -printf '%T@ %p\n' 2>/dev/null | sort -rn | cut -d' ' -f2-)
  local index
  for ((index=20; index<${#backups[@]}; index++)); do
    [[ "${backups[index]}" == "${backup_root}"/* ]] || continue
    rm -rf -- "${backups[index]}"
  done
}

set_ini_value() {
  local file="$1" key="$2" value="$3" temporary
  temporary="$(mktemp "${file}.tmp.XXXXXX")"
  awk -v key="${key}" -v value="${value}" '
    BEGIN { updated=0 }
    $0 ~ "^[[:space:]]*" key "[[:space:]]*=" {
      if (!updated) { print key " = " value; updated=1 }
      next
    }
    { print }
    END { if (!updated) print key " = " value }
  ' "${file}" >"${temporary}"
  chmod --reference="${file}" "${temporary}"
  chown --reference="${file}" "${temporary}"
  mv -- "${temporary}" "${file}"
}

set_fpm_value() {
  set_ini_value "$@"
}

validate_inputs
[[ -f "${effective_ini_file}" && -f "${effective_pool_file}" && -f "${fpm_config_file}" && -x "${install_dir}/sbin/php-fpm" ]] ||
  die_code CONFIG_UNAVAILABLE "PHP-FPM configuration is unavailable"

if [[ "${operation}" == "get" ]]; then
  printf 'component=php\n'
  printf 'revision=%s\n' "$(revision)"
  printf 'apply_mode=reload\n'
  printf 'memoryLimit=%s\n' "$(php_number_mb memory_limit 256)"
  printf 'uploadMaxFilesize=%s\n' "$(php_number_mb upload_max_filesize 2)"
  printf 'postMaxSize=%s\n' "$(php_number_mb post_max_size 8)"
  printf 'maxExecutionTime=%s\n' "$(php_number max_execution_time 30)"
  printf 'pmMaxChildren=%s\n' "$(fpm_number 'pm\.max_children' 32)"
  printf 'pmStartServers=%s\n' "$(fpm_number 'pm\.start_servers' 4)"
  printf 'pmMinSpareServers=%s\n' "$(fpm_number 'pm\.min_spare_servers' 2)"
  printf 'pmMaxSpareServers=%s\n' "$(fpm_number 'pm\.max_spare_servers' 8)"
  printf 'runtime.port=\n'
  printf 'runtime.bindAddress=unix\n'
  printf 'runtime.socketPath=%s\n' "${socket_path}"
  printf 'runtime.installDir=%s\n' "${install_dir}"
  printf 'runtime.dataDir=\n'
  printf 'runtime.logDir=%s\n' "${log_dir}"
  printf 'runtime.runUser=%s\n' "${run_user}"
  printf 'runtime.runGroup=%s\n' "${run_group}"
  exit 0
fi

[[ "${operation}" == "apply" ]] || die_code INVALID_ACTION "Unsupported PHP configuration operation"
require_root
[[ "${expected_revision}" =~ ^[0-9a-f]{64}$ ]] || die_code CONFIG_REVISION_CONFLICT "Configuration revision is invalid"
[[ "$(revision)" == "${expected_revision}" ]] || die_code CONFIG_REVISION_CONFLICT "Configuration changed since preview; refresh and try again"

memory_value="${ONEINSTACK_CONFIG_MEMORY_LIMIT:-}"
upload_max="${ONEINSTACK_CONFIG_UPLOAD_MAX_FILESIZE:-}"
post_max="${ONEINSTACK_CONFIG_POST_MAX_SIZE:-}"
execution_time="${ONEINSTACK_CONFIG_MAX_EXECUTION_TIME:-}"
pm_max_children="${ONEINSTACK_CONFIG_PM_MAX_CHILDREN:-}"
pm_start="${ONEINSTACK_CONFIG_PM_START_SERVERS:-}"
pm_min="${ONEINSTACK_CONFIG_PM_MIN_SPARE_SERVERS:-}"
pm_max="${ONEINSTACK_CONFIG_PM_MAX_SPARE_SERVERS:-}"
for value in "${memory_value}" "${upload_max}" "${post_max}" "${execution_time}" "${pm_max_children}" "${pm_start}" "${pm_min}" "${pm_max}"; do
  [[ "${value}" =~ ^[0-9]+$ ]] || die_code INVALID_PARAMETER "PHP configuration fields must be integers"
done
((memory_value >= 32 && memory_value <= 8192)) || die_code INVALID_PARAMETER "memoryLimit is outside the supported range"
((upload_max >= 1 && upload_max <= 2048)) || die_code INVALID_PARAMETER "uploadMaxFilesize is outside the supported range"
((post_max >= upload_max && post_max <= 4096)) || die_code INVALID_PARAMETER "postMaxSize must be at least uploadMaxFilesize"
((execution_time >= 10 && execution_time <= 3600)) || die_code INVALID_PARAMETER "maxExecutionTime is outside the supported range"
((pm_max_children >= 1 && pm_max_children <= 10000)) || die_code INVALID_PARAMETER "pmMaxChildren is outside the supported range"
((pm_min >= 1 && pm_min <= pm_start && pm_start <= pm_max && pm_max <= pm_max_children)) ||
  die_code INVALID_PARAMETER "PHP-FPM process counts are not ordered correctly"

emit_progress 8 config_snapshot "正在创建 PHP-FPM 配置快照"
install -d -m 0750 -- "${backup_root}"
backup_dir="$(mktemp -d "${backup_root}/config-$(date -u +%Y%m%dT%H%M%SZ)-XXXXXX")"
chmod 0700 "${backup_dir}"
cp -a -- "${effective_ini_file}" "${backup_dir}/php.ini"
cp -a -- "${effective_pool_file}" "${backup_dir}/pool.conf"
cp -a -- "${fpm_config_file}" "${backup_dir}/php-fpm.conf"
printf '%s\n' "${expected_revision}" >"${backup_dir}/revision"

php_candidate="$(mktemp "${effective_ini_file}.candidate.XXXXXX")"
fpm_candidate="$(mktemp "${effective_pool_file}.candidate.XXXXXX")"
cp -p -- "${effective_ini_file}" "${php_candidate}"
cp -p -- "${effective_pool_file}" "${fpm_candidate}"
set_ini_value "${php_candidate}" memory_limit "${memory_value}M"
set_ini_value "${php_candidate}" upload_max_filesize "${upload_max}M"
set_ini_value "${php_candidate}" post_max_size "${post_max}M"
set_ini_value "${php_candidate}" max_execution_time "${execution_time}"
set_fpm_value "${fpm_candidate}" pm.max_children "${pm_max_children}"
set_fpm_value "${fpm_candidate}" pm.start_servers "${pm_start}"
set_fpm_value "${fpm_candidate}" pm.min_spare_servers "${pm_min}"
set_fpm_value "${fpm_candidate}" pm.max_spare_servers "${pm_max}"

test_main="$(mktemp "${state_dir}/php-fpm-test.XXXXXX")"
cat >"${test_main}" <<EOF
[global]
pid = ${pid_file}
error_log = ${log_file}
include=${fpm_candidate}
EOF

emit_progress 35 config_validate "正在校验 PHP-FPM 候选配置"
"${install_dir}/sbin/php-fpm" --test --fpm-config "${test_main}" || {
  rm -f -- "${php_candidate}" "${fpm_candidate}" "${test_main}"
  die_code CONFIG_APPLY_FAILED "PHP-FPM candidate configuration is invalid"
}
rm -f -- "${test_main}"

was_active=false
service_is_active && was_active=true
committed=false
rollback_configuration() {
  local code="${1:-$?}"
  set +e
  if [[ "${committed}" == "true" ]]; then
    cp -p -- "${backup_dir}/php.ini" "${effective_ini_file}"
    cp -p -- "${backup_dir}/pool.conf" "${effective_pool_file}"
    cp -p -- "${backup_dir}/php-fpm.conf" "${fpm_config_file}"
    if [[ "${was_active}" == "true" ]]; then
      if systemd_available && managed_unit_matches; then
        systemctl reload "${service_name}.service"
      elif managed_pid_running; then
        kill -USR2 "$(cat -- "${pid_file}")" 2>/dev/null || true
      fi
    fi
  fi
  rm -f -- "${php_candidate:-}" "${fpm_candidate:-}" "${test_main:-}"
  exit "${code}"
}
trap 'rollback_configuration $?' ERR
trap 'rollback_configuration 130' INT
trap 'rollback_configuration 143' TERM

emit_progress 62 config_publish "正在原子发布 PHP-FPM 配置"
committed=true
mv -f -- "${php_candidate}" "${effective_ini_file}"
mv -f -- "${fpm_candidate}" "${effective_pool_file}"
if [[ "${was_active}" == "true" ]]; then
  emit_progress 82 config_reload "正在平滑重载 PHP-FPM"
  if systemd_available && managed_unit_matches; then
    systemctl reload "${service_name}.service"
  else
    pid="$(cat -- "${pid_file}")"
    pid_is_managed "${pid}" || die_code SERVICE_RELOAD_FAILED "Managed PHP-FPM process identity is unavailable"
    kill -USR2 "${pid}"
  fi
  service_is_active || die_code SERVICE_RELOAD_FAILED "PHP-FPM did not remain active after reload"
  wait_for_socket || die_code SERVICE_RELOAD_FAILED "PHP-FPM socket did not remain ready after reload"
fi
trap - ERR INT TERM
prune_backups
emit_progress 100 config_applied "PHP-FPM 配置已生效"
printf 'component=php\n'
printf 'configuration=applied\n'
printf 'revision=%s\n' "$(revision)"
