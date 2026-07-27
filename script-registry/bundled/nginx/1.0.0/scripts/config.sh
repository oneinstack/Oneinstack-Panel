#!/usr/bin/env bash
set -Eeuo pipefail
source "$(dirname "$0")/common.sh"

config_file="${install_dir}/conf/nginx.conf"
operation="${ONEINSTACK_CONFIG_OPERATION:-get}"
expected_revision="${ONEINSTACK_CONFIG_REVISION:-}"
backup_root="${state_dir}/config-backups"

revision() { sha256sum "${config_file}" | awk '{print $1}'; }
read_value() {
  local expression="$1" fallback="$2" value
  value="$(sed -nE "${expression}" "${config_file}" | head -n1)"
  printf '%s' "${value:-${fallback}}"
}
prune_backups() {
  local -a backups=()
  mapfile -t backups < <(find "${backup_root}" -mindepth 1 -maxdepth 1 -type d -printf '%T@ %p\n' 2>/dev/null | sort -rn | cut -d' ' -f2-)
  local index
  for ((index=20; index<${#backups[@]}; index++)); do rm -rf -- "${backups[index]}"; done
}

validate_inputs
[[ -f "${config_file}" && -x "${install_dir}/sbin/nginx" ]] || die "Nginx configuration is unavailable."

if [[ "${operation}" == "get" ]]; then
  worker_processes="$(read_value 's/^[[:space:]]*worker_processes[[:space:]]+([^;]+);/\1/p' auto)"
  worker_connections="$(grep -Eo 'worker_connections[[:space:]]+[0-9]+' "${config_file}" | head -n1 | grep -Eo '[0-9]+' || true)"
  keepalive_timeout="$(read_value 's/^[[:space:]]*keepalive_timeout[[:space:]]+([0-9]+);/\1/p' 65)"
  client_max_body_size="$(read_value 's/^[[:space:]]*client_max_body_size[[:space:]]+([0-9]+)[mM];/\1/p' 1)"
  printf 'component=nginx\nrevision=%s\napply_mode=reload\n' "$(revision)"
  printf 'workerProcesses=%s\nworkerConnections=%s\nkeepaliveTimeout=%s\nclientMaxBodySize=%s\n' \
    "${worker_processes}" "${worker_connections:-4096}" "${keepalive_timeout}" "${client_max_body_size}"
  exit 0
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
[[ "${worker_processes}" == "auto" || "${worker_processes}" =~ ^[1-9][0-9]?$ ]] || die "Invalid workerProcesses."
[[ "${worker_connections}" =~ ^[0-9]+$ && "${worker_connections}" -ge 512 && "${worker_connections}" -le 65535 ]] || die "Invalid workerConnections."
[[ "${keepalive_timeout}" =~ ^[0-9]+$ && "${keepalive_timeout}" -ge 5 && "${keepalive_timeout}" -le 300 ]] || die "Invalid keepaliveTimeout."
[[ "${client_max_body_size}" =~ ^[0-9]+$ && "${client_max_body_size}" -ge 1 && "${client_max_body_size}" -le 10240 ]] || die "Invalid clientMaxBodySize."

emit_progress 8 config_snapshot "正在创建 Nginx 配置快照"
install -d -m 0750 -- "${backup_root}"
backup_dir="$(mktemp -d "${backup_root}/config-$(date -u +%Y%m%dT%H%M%SZ)-XXXXXX")"
chmod 0700 "${backup_dir}"
cp -a -- "${config_file}" "${backup_dir}/nginx.conf"
printf '%s\n' "${expected_revision}" >"${backup_dir}/revision"

candidate="$(mktemp "$(dirname "${config_file}")/.oneinstack-nginx.XXXXXX")"
cp -p -- "${config_file}" "${candidate}"
sed -Ei \
  -e "s/^[[:space:]]*worker_processes[[:space:]]+[^;]+;/worker_processes ${worker_processes};/" \
  -e "s/worker_connections[[:space:]]+[0-9]+;/worker_connections ${worker_connections};/" \
  -e "s/^[[:space:]]*keepalive_timeout[[:space:]]+[0-9]+;/    keepalive_timeout ${keepalive_timeout};/" \
  "${candidate}"
if grep -Eq '^[[:space:]]*client_max_body_size[[:space:]]+' "${candidate}"; then
  sed -Ei "s/^[[:space:]]*client_max_body_size[[:space:]]+[^;]+;/    client_max_body_size ${client_max_body_size}m;/" "${candidate}"
else
  sed -i "/^[[:space:]]*server_tokens[[:space:]]/i\\    client_max_body_size ${client_max_body_size}m;" "${candidate}"
fi

emit_progress 35 config_validate "正在校验 Nginx 候选配置"
if ! "${install_dir}/sbin/nginx" -t -p "${install_dir}/" -c "${candidate}"; then
  rm -f -- "${candidate}"
  printf 'Nginx candidate configuration is invalid.\n' >&2
  exit 65
fi
was_active=false
systemctl is-active --quiet nginx.service && was_active=true
committed=false
rollback() {
  local code="${1:-$?}"
  set +e
  if [[ "${committed}" == "true" ]]; then
    restore="$(mktemp "$(dirname "${config_file}")/.oneinstack-nginx-restore.XXXXXX")"
    cp -p -- "${backup_dir}/nginx.conf" "${restore}"
    mv -f -- "${restore}" "${config_file}"
    [[ "${was_active}" == "true" ]] && systemctl reload nginx.service
  fi
  rm -f -- "${candidate:-}"
  exit "${code}"
}
trap 'rollback $?' ERR
trap 'rollback 130' INT
trap 'rollback 143' TERM

emit_progress 62 config_publish "正在原子发布 Nginx 配置"
mv -f -- "${candidate}" "${config_file}"
committed=true
if [[ "${was_active}" == "true" ]]; then
  emit_progress 82 config_reload "正在平滑重载 Nginx"
  systemctl reload nginx.service
  systemctl is-active --quiet nginx.service
fi
trap - ERR INT TERM
prune_backups
emit_progress 100 config_applied "Nginx 配置已生效"
printf 'Configuration backup: %s\n' "$(basename "${backup_dir}")"
