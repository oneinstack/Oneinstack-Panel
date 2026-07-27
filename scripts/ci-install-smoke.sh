#!/usr/bin/env bash

set -euo pipefail

if [[ $# -ne 1 ]]; then
  echo "Usage: $0 RELEASE_ARCHIVE" >&2
  exit 2
fi

archive="$1"
[[ -f "$archive" ]] || {
  echo "Release archive not found: $archive" >&2
  exit 1
}
command -v curl >/dev/null 2>&1 || {
  echo "curl is required" >&2
  exit 1
}

temporary_base="${TMPDIR:-/tmp}"
temporary_base="${temporary_base%/}"
work_dir="$(mktemp -d "${temporary_base}/one-install-smoke.XXXXXX")"
server_pid=""

cleanup() {
  if [[ -n "$server_pid" ]] && kill -0 "$server_pid" >/dev/null 2>&1; then
    kill -TERM "$server_pid" >/dev/null 2>&1 || true
    wait "$server_pid" >/dev/null 2>&1 || true
  fi
  case "$work_dir" in
    "${temporary_base}"/one-install-smoke.*) rm -rf -- "$work_dir" ;;
  esac
}
trap cleanup EXIT

extract_dir="${work_dir}/package"
stage_root="${work_dir}/stage"
runtime_dir="${work_dir}/runtime"
mkdir -p "$extract_dir" "$stage_root" \
  "${runtime_dir}/data/wwwroot" \
  "${runtime_dir}/data/wwwlogs" \
  "${runtime_dir}/data/db"
tar -xzf "$archive" -C "$extract_dir"
package_root="$(find "$extract_dir" -mindepth 1 -maxdepth 1 -type d -print -quit)"
[[ -n "$package_root" ]] || {
  echo "Release archive has no package root" >&2
  exit 1
}

"${package_root}/install.sh" --root "$stage_root"
installed_binary="${stage_root}/usr/local/one/one"
[[ -x "$installed_binary" ]]

port="${ONEINSTACK_SMOKE_PORT:-18089}"
runtime_config="${runtime_dir}/config.yaml"
sed \
  -e "s|port: 8089|port: ${port}|" \
  -e "s|defaultPath: '/data/'|defaultPath: '${runtime_dir}/data/'|" \
  -e "s|webPath: '/data/wwwroot/'|webPath: '${runtime_dir}/data/wwwroot/'|" \
  -e "s|logPath: '/data/wwwlogs/'|logPath: '${runtime_dir}/data/wwwlogs/'|" \
  -e "s|dataPath: '/data/db/'|dataPath: '${runtime_dir}/data/db/'|" \
  -e "s|certificatePath: '/usr/local/one/certificates'|certificatePath: '${runtime_dir}/certificates'|" \
  -e "s|acmeChallengePath: '/usr/local/one/acme-webroot'|acmeChallengePath: '${runtime_dir}/acme-webroot'|" \
  "${package_root}/config.yaml" >"$runtime_config"

password_file="${runtime_dir}/admin-password"
(umask 077 && printf '%s\n' 'P0laris!2026' >"$password_file")

ONEINSTACK_BASE_PATH="$runtime_dir" \
  ONEINSTACK_CONFIG_PATH="$runtime_config" \
  "$installed_binary" init \
  --user smoke_operator \
  --password-file "$password_file"
rm -f -- "$password_file"

ONEINSTACK_BASE_PATH="$runtime_dir" \
  ONEINSTACK_CONFIG_PATH="$runtime_config" \
  "$installed_binary" server start >"${runtime_dir}/server.log" 2>&1 &
server_pid=$!

ready=false
for _ in $(seq 1 30); do
  if curl --fail --silent --show-error --max-time 2 \
    "http://127.0.0.1:${port}/health/live" >/dev/null &&
    curl --fail --silent --show-error --max-time 2 \
      "http://127.0.0.1:${port}/health/ready" >/dev/null; then
    ready=true
    break
  fi
  if ! kill -0 "$server_pid" >/dev/null 2>&1; then
    break
  fi
  sleep 1
done

if [[ "$ready" != true ]]; then
  cat "${runtime_dir}/server.log" >&2
  echo "Panel did not become healthy" >&2
  exit 1
fi

kill -TERM "$server_pid"
wait "$server_pid"
server_pid=""

"${package_root}/install.sh" uninstall --root "$stage_root" --purge --yes
[[ ! -e "${stage_root}/usr/local/one" ]]
echo "Release install, initialization, health, shutdown, and uninstall smoke test passed"
