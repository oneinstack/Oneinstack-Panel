#!/usr/bin/env bash

set -euo pipefail

base_path="${ONEINSTACK_BASE_PATH:-/var/lib/oneinstack-panel}"
admin_user="${ONEINSTACK_ADMIN_USER:-admin}"
admin_secret="${ONEINSTACK_ADMIN_PASSWORD_FILE:-/run/secrets/one_admin_password}"
temporary_password_file=""

cleanup() {
  if [[ -n "$temporary_password_file" && -f "$temporary_password_file" ]]; then
    rm -f -- "$temporary_password_file"
  fi
}
trap cleanup EXIT

mkdir -p -- "$base_path" /data

if [[ "${1:-}" == "version" ]]; then
  exec /usr/local/bin/one "$@"
fi

if [[ -r "$admin_secret" ]]; then
  umask 077
  temporary_password_file="$(mktemp /tmp/one-admin-password.XXXXXX)"
  cp -- "$admin_secret" "$temporary_password_file"
  chmod 0600 "$temporary_password_file"
  /usr/local/bin/one init \
    --user "$admin_user" \
    --password-file "$temporary_password_file"
  cleanup
  temporary_password_file=""
elif [[ ! -s "${base_path%/}/myadmin.db" ]]; then
  echo "首次启动需要 Docker Secret: ${admin_secret}" >&2
  exit 1
fi

exec /usr/local/bin/one "$@"
