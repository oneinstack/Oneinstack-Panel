#!/usr/bin/env bash
set -Eeuo pipefail
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"

operation="${ONEINSTACK_CONFIG_OPERATION:-get}"
expected_revision="${ONEINSTACK_CONFIG_REVISION:-}"
backup_root="${state_dir}/config-backups"
default_ignore_ips="127.0.0.1/8 ::1"

revision() {
  sha256sum "${defaults_path}" | awk '{print $1}'
}

last_value() {
  local key="$1" fallback="$2" value
  value="$(sed -nE "s/^[[:space:]]*${key}[[:space:]]*=[[:space:]]*(.*)$/\1/p" "${defaults_path}" | tail -n1)"
  printf '%s' "${value:-${fallback}}"
}

prune_backups() {
  local -a backups=()
  mapfile -t backups < <(find "${backup_root}" -mindepth 1 -maxdepth 1 -type d -printf '%T@ %p\n' 2>/dev/null | sort -rn | cut -d' ' -f2-)
  local index
  for ((index = 20; index < ${#backups[@]}; index++)); do
    rm -rf -- "${backups[index]}"
  done
}

validate_inputs
[[ -r "${defaults_path}" ]] || die "Fail2ban managed configuration is unavailable."

if [[ "${operation}" == "get" ]]; then
  printf 'component=fail2ban\nrevision=%s\napply_mode=reload\n' "$(revision)"
  printf 'maxRetry=%s\nfindTimeSeconds=%s\nbanTimeSeconds=%s\nignoreIps=%s\n' \
    "$(last_value maxretry 5)" \
    "$(last_value findtime 600)" \
    "$(last_value bantime 3600)" \
    "$(last_value ignoreip "${default_ignore_ips}")"
  exit 0
fi

[[ "${operation}" == "apply" ]] || die "Unsupported configuration operation."
require_root
[[ "${expected_revision}" =~ ^[0-9a-f]{64}$ ]] || die "Invalid configuration revision."
[[ "$(revision)" == "${expected_revision}" ]] || {
  printf 'Configuration changed since preview; refresh and try again.\n' >&2
  exit 75
}

max_retry="${ONEINSTACK_CONFIG_MAX_RETRY:-}"
find_time="${ONEINSTACK_CONFIG_FIND_TIME:-}"
ban_time="${ONEINSTACK_CONFIG_BAN_TIME:-}"
ignore_ips="${ONEINSTACK_CONFIG_IGNORE_IPS:-}"
[[ "${max_retry}" =~ ^[0-9]+$ && "${max_retry}" -ge 1 && "${max_retry}" -le 100 ]] || die "Invalid maxRetry."
[[ "${find_time}" =~ ^[0-9]+$ && "${find_time}" -ge 1 && "${find_time}" -le 604800 ]] || die "Invalid findTimeSeconds."
[[ "${ban_time}" =~ ^[0-9]+$ && "${ban_time}" -ge 60 && "${ban_time}" -le 31536000 ]] || die "Invalid banTimeSeconds."
[[ -z "${ignore_ips}" || "${ignore_ips}" =~ ^[0-9A-Fa-f:./[:space:]]+$ ]] || die "Invalid ignoreIps."

emit_progress 8 config_snapshot "正在创建 Fail2ban 配置快照"
install -d -m 0750 -- "${backup_root}"
backup_dir="$(mktemp -d "${backup_root}/config-$(date -u +%Y%m%dT%H%M%SZ)-XXXXXX")"
chmod 0700 "${backup_dir}"
cp -a -- "${defaults_path}" "${backup_dir}/defaults.local"
printf '%s\n' "${expected_revision}" >"${backup_dir}/revision"

banaction="$(last_value banaction "$(select_banaction)")"
candidate="$(mktemp "$(dirname -- "${defaults_path}")/.oneinstack-fail2ban.XXXXXX")"
{
  printf '[DEFAULT]\n'
  printf 'banaction = %s\n' "${banaction}"
  printf 'maxretry = %s\n' "${max_retry}"
  printf 'findtime = %s\n' "${find_time}"
  printf 'bantime = %s\n' "${ban_time}"
  if [[ -n "${ignore_ips}" ]]; then
    printf 'ignoreip = %s\n' "${ignore_ips}"
  fi
} >"${candidate}"
chmod --reference="${defaults_path}" "${candidate}"
chown --reference="${defaults_path}" "${candidate}" 2>/dev/null || true

was_active=false
if systemctl is-active --quiet "${service_name}.service"; then
  was_active=true
fi
committed=false
rollback() {
  local code="${1:-$?}" restore
  set +e
  if [[ "${committed}" == "true" ]]; then
    restore="$(mktemp "$(dirname -- "${defaults_path}")/.oneinstack-fail2ban-restore.XXXXXX")"
    cp -p -- "${backup_dir}/defaults.local" "${restore}"
    mv -f -- "${restore}" "${defaults_path}"
    if [[ "${was_active}" == "true" ]]; then
      fail2ban-client -t >/dev/null 2>&1 && fail2ban-client reload >/dev/null 2>&1 || true
    fi
  fi
  rm -f -- "${candidate:-}"
  exit "${code}"
}
trap 'rollback $?' ERR
trap 'rollback 130' INT
trap 'rollback 143' TERM

emit_progress 35 config_publish "正在原子发布 Fail2ban 配置"
mv -f -- "${candidate}" "${defaults_path}"
committed=true
emit_progress 65 config_validate "正在校验 Fail2ban 候选配置"
fail2ban-client -t
if [[ "${was_active}" == "true" ]]; then
  emit_progress 82 config_reload "正在重载 Fail2ban"
  fail2ban-client reload
fi
trap - ERR INT TERM
prune_backups
emit_progress 100 config_applied "Fail2ban 配置已生效"
printf 'Configuration backup: %s\n' "$(basename "${backup_dir}")"
