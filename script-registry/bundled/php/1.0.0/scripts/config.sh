#!/usr/bin/env bash
set -Eeuo pipefail
source "$(dirname "$0")/common.sh"

php_ini="${install_dir}/lib/php.ini"
fpm_pool="${install_dir}/etc/php-fpm.d/www.conf"
operation="${ONEINSTACK_CONFIG_OPERATION:-get}"
expected_revision="${ONEINSTACK_CONFIG_REVISION:-}"
backup_root="${state_dir}/config-backups"
begin_marker="; BEGIN ONEINSTACK PANEL RUNTIME"
end_marker="; END ONEINSTACK PANEL RUNTIME"

revision() { { sha256sum "${php_ini}"; sha256sum "${fpm_pool}"; } | sha256sum | awk '{print $1}'; }
php_number_mb() {
  local key="$1" fallback="$2" value
  value="$(sed -nE "s/^[[:space:]]*${key}[[:space:]]*=[[:space:]]*([0-9]+)[mM].*/\\1/p" "${php_ini}" | tail -n1)"
  printf '%s' "${value:-${fallback}}"
}
php_number() {
  local key="$1" fallback="$2" value
  value="$(sed -nE "s/^[[:space:]]*${key}[[:space:]]*=[[:space:]]*([0-9]+).*/\\1/p" "${php_ini}" | tail -n1)"
  printf '%s' "${value:-${fallback}}"
}
fpm_number() {
  local key="$1" fallback="$2" value
  value="$(sed -nE "s/^[[:space:]]*${key}[[:space:]]*=[[:space:]]*([0-9]+).*/\\1/p" "${fpm_pool}" | tail -n1)"
  printf '%s' "${value:-${fallback}}"
}
prune_backups() {
  local -a backups=()
  mapfile -t backups < <(find "${backup_root}" -mindepth 1 -maxdepth 1 -type d -printf '%T@ %p\n' 2>/dev/null | sort -rn | cut -d' ' -f2-)
  local index
  for ((index=20; index<${#backups[@]}; index++)); do rm -rf -- "${backups[index]}"; done
}

validate_inputs
[[ -f "${php_ini}" && -f "${fpm_pool}" && -x "${install_dir}/sbin/php-fpm" ]] || die "PHP-FPM configuration is unavailable."
if [[ "${operation}" == "get" ]]; then
  printf 'component=php\nrevision=%s\napply_mode=reload\n' "$(revision)"
  printf 'memoryLimit=%s\nuploadMaxFilesize=%s\npostMaxSize=%s\nmaxExecutionTime=%s\n' \
    "$(php_number_mb memory_limit 256)" "$(php_number_mb upload_max_filesize 2)" \
    "$(php_number_mb post_max_size 8)" "$(php_number max_execution_time 30)"
  printf 'pmMaxChildren=%s\npmStartServers=%s\npmMinSpareServers=%s\npmMaxSpareServers=%s\n' \
    "$(fpm_number 'pm\.max_children' 32)" "$(fpm_number 'pm\.start_servers' 4)" \
    "$(fpm_number 'pm\.min_spare_servers' 2)" "$(fpm_number 'pm\.max_spare_servers' 8)"
  exit 0
fi

[[ "${operation}" == "apply" ]] || die "Unsupported configuration operation."
require_root
[[ "${expected_revision}" =~ ^[0-9a-f]{64}$ ]] || die "Invalid configuration revision."
[[ "$(revision)" == "${expected_revision}" ]] || {
  printf 'Configuration changed since preview; refresh and try again.\n' >&2
  exit 75
}
memory_limit="${ONEINSTACK_CONFIG_MEMORY_LIMIT:-}"
upload_max="${ONEINSTACK_CONFIG_UPLOAD_MAX_FILESIZE:-}"
post_max="${ONEINSTACK_CONFIG_POST_MAX_SIZE:-}"
execution_time="${ONEINSTACK_CONFIG_MAX_EXECUTION_TIME:-}"
pm_max_children="${ONEINSTACK_CONFIG_PM_MAX_CHILDREN:-}"
pm_start="${ONEINSTACK_CONFIG_PM_START_SERVERS:-}"
pm_min="${ONEINSTACK_CONFIG_PM_MIN_SPARE_SERVERS:-}"
pm_max="${ONEINSTACK_CONFIG_PM_MAX_SPARE_SERVERS:-}"
for value in "${memory_limit}" "${upload_max}" "${post_max}" "${execution_time}" "${pm_max_children}" "${pm_start}" "${pm_min}" "${pm_max}"; do
  [[ "${value}" =~ ^[0-9]+$ ]] || die "PHP configuration values must be integers."
done
((memory_limit >= 32 && memory_limit <= 8192)) || die "Invalid memoryLimit."
((upload_max >= 1 && upload_max <= 2048)) || die "Invalid uploadMaxFilesize."
((post_max >= upload_max && post_max <= 4096)) || die "postMaxSize must be at least uploadMaxFilesize."
((execution_time >= 10 && execution_time <= 3600)) || die "Invalid maxExecutionTime."
((pm_max_children >= 1 && pm_max_children <= 10000)) || die "Invalid pmMaxChildren."
((pm_min >= 1 && pm_min <= pm_start && pm_start <= pm_max && pm_max <= pm_max_children)) || die "Invalid PHP-FPM process manager ordering."

emit_progress 8 config_snapshot "正在创建 PHP-FPM 配置快照"
install -d -m 0750 -- "${backup_root}"
backup_dir="$(mktemp -d "${backup_root}/config-$(date -u +%Y%m%dT%H%M%SZ)-XXXXXX")"
chmod 0700 "${backup_dir}"
cp -a -- "${php_ini}" "${backup_dir}/php.ini"
cp -a -- "${fpm_pool}" "${backup_dir}/www.conf"
printf '%s\n' "${expected_revision}" >"${backup_dir}/revision"
php_candidate="$(mktemp "$(dirname "${php_ini}")/.oneinstack-phpini.XXXXXX")"
fpm_candidate="$(mktemp "$(dirname "${fpm_pool}")/.oneinstack-fpm.XXXXXX")"
sed "/^${begin_marker}$/,/^${end_marker}$/d" "${php_ini}" >"${php_candidate}"
cat >>"${php_candidate}" <<EOF

${begin_marker}
memory_limit = ${memory_limit}M
upload_max_filesize = ${upload_max}M
post_max_size = ${post_max}M
max_execution_time = ${execution_time}
${end_marker}
EOF
sed -E \
  -e "s/^[[:space:]]*pm\\.max_children[[:space:]]*=.*/pm.max_children = ${pm_max_children}/" \
  -e "s/^[[:space:]]*pm\\.start_servers[[:space:]]*=.*/pm.start_servers = ${pm_start}/" \
  -e "s/^[[:space:]]*pm\\.min_spare_servers[[:space:]]*=.*/pm.min_spare_servers = ${pm_min}/" \
  -e "s/^[[:space:]]*pm\\.max_spare_servers[[:space:]]*=.*/pm.max_spare_servers = ${pm_max}/" \
  "${fpm_pool}" >"${fpm_candidate}"
chmod --reference="${php_ini}" "${php_candidate}"; chown --reference="${php_ini}" "${php_candidate}"
chmod --reference="${fpm_pool}" "${fpm_candidate}"; chown --reference="${fpm_pool}" "${fpm_candidate}"
test_main="$(mktemp "${state_dir}/php-fpm-test.XXXXXX")"
cat >"${test_main}" <<EOF
[global]
pid = /run/php-fpm.pid
error_log = /var/log/php-fpm.log
include=${fpm_candidate}
EOF

emit_progress 35 config_validate "正在校验 PHP-FPM 候选配置"
if ! PHPRC="${php_candidate}" "${install_dir}/sbin/php-fpm" --test --fpm-config "${test_main}"; then
  rm -f -- "${php_candidate}" "${fpm_candidate}" "${test_main}"
  printf 'PHP-FPM candidate configuration is invalid.\n' >&2
  exit 65
fi
rm -f -- "${test_main}"
was_active=false
systemctl is-active --quiet php-fpm.service && was_active=true
committed=false
rollback() {
  local code="${1:-$?}"
  set +e
  if [[ "${committed}" == "true" ]]; then
    php_restore="$(mktemp "$(dirname "${php_ini}")/.oneinstack-phpini-restore.XXXXXX")"
    fpm_restore="$(mktemp "$(dirname "${fpm_pool}")/.oneinstack-fpm-restore.XXXXXX")"
    cp -p -- "${backup_dir}/php.ini" "${php_restore}"; mv -f -- "${php_restore}" "${php_ini}"
    cp -p -- "${backup_dir}/www.conf" "${fpm_restore}"; mv -f -- "${fpm_restore}" "${fpm_pool}"
    [[ "${was_active}" == "true" ]] && systemctl reload php-fpm.service
  fi
  rm -f -- "${php_candidate:-}" "${fpm_candidate:-}" "${test_main:-}"
  exit "${code}"
}
trap 'rollback $?' ERR
trap 'rollback 130' INT
trap 'rollback 143' TERM

emit_progress 62 config_publish "正在发布 PHP-FPM 配置"
committed=true
mv -f -- "${php_candidate}" "${php_ini}"
mv -f -- "${fpm_candidate}" "${fpm_pool}"
if [[ "${was_active}" == "true" ]]; then
  emit_progress 82 config_reload "正在平滑重载 PHP-FPM"
  systemctl reload php-fpm.service
  systemctl is-active --quiet php-fpm.service
fi
trap - ERR INT TERM
prune_backups
emit_progress 100 config_applied "PHP-FPM 配置已生效"
printf 'Configuration backup: %s\n' "$(basename "${backup_dir}")"
