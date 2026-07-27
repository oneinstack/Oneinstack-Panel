#!/usr/bin/env bash

set -Eeuo pipefail

usage() {
  echo "Usage: $0 CENTER_URL VERSION CHANNEL AMD64_ARTIFACT ARM64_ARTIFACT [ROLLOUT_PERCENTAGE]" >&2
}

if [[ $# -lt 5 || $# -gt 6 ]]; then
  usage
  exit 2
fi

center_url="${1%/}"
version="$2"
channel="$3"
amd64_artifact="$4"
arm64_artifact="$5"
rollout_percentage="${6:-0}"

if [[ -z "${CENTER_RELEASE_TOKEN:-}" ]]; then
  echo "CENTER_RELEASE_TOKEN is required" >&2
  exit 2
fi
if [[ ! "$center_url" =~ ^https:// ]] &&
  [[ ! "$center_url" =~ ^http://(127\.0\.0\.1|\[::1\]|localhost)(:[0-9]+)?$ ]]; then
  echo "Center URL must use HTTPS (loopback HTTP is allowed for development)" >&2
  exit 2
fi
if [[ ! "$version" =~ ^v?[0-9]+\.[0-9]+(\.[0-9]+)?([-+][0-9A-Za-z.-]+)?$ ]]; then
  echo "Invalid Panel version: $version" >&2
  exit 2
fi
case "$channel" in
  stable|beta|development) ;;
  *)
    echo "Invalid release channel: $channel" >&2
    exit 2
    ;;
esac
if [[ ! "$rollout_percentage" =~ ^[0-9]+$ ]] ||
  ((rollout_percentage < 0 || rollout_percentage > 100)); then
  echo "Rollout percentage must be between 0 and 100" >&2
  exit 2
fi
for artifact in "$amd64_artifact" "$arm64_artifact"; do
  if [[ ! -f "$artifact" || ! -s "$artifact" ]]; then
    echo "Panel release artifact is missing or empty: $artifact" >&2
    exit 2
  fi
done
for command_name in curl jq; do
  if ! command -v "$command_name" >/dev/null 2>&1; then
    echo "Required command not found: $command_name" >&2
    exit 2
  fi
done

temporary_base="${TMPDIR:-/tmp}"
temporary_base="${temporary_base%/}"
curl_config="$(mktemp "${temporary_base}/oneinstack-center-curl.XXXXXX")"
cleanup() {
  case "$curl_config" in
    "${temporary_base}"/oneinstack-center-curl.*) rm -f -- "$curl_config" ;;
  esac
}
trap cleanup EXIT
chmod 0600 "$curl_config"
{
  printf 'silent\n'
  printf 'show-error\n'
  printf 'fail-with-body\n'
  printf 'header = "Authorization: Bearer %s"\n' "$CENTER_RELEASE_TOKEN"
} >"$curl_config"

release_notes="${CENTER_RELEASE_NOTES:-Panel release ${version}}"
case "$version" in
  v*) canonical_version="$version" ;;
  *) canonical_version="v${version}" ;;
esac
create_payload="$(
  jq -cn \
    --arg version "$canonical_version" \
    --arg channel "$channel" \
    --arg release_notes "$release_notes" \
    --argjson percentage "$rollout_percentage" \
    '{
      version: $version,
      channel: $channel,
      releaseNotes: $release_notes,
      rollout: {percentage: $percentage}
    }'
)"

release_list="$(
  curl --config "$curl_config" \
    "${center_url}/v1/admin/panel/releases"
)"
release_json="$(
  jq -c --arg version "$canonical_version" \
    '.releases[]? | select(.version == $version)' <<<"$release_list"
)"
if [[ -z "$release_json" ]]; then
  curl --config "$curl_config" \
    --request POST \
    --header "Content-Type: application/json" \
    --data "$create_payload" \
    "${center_url}/v1/admin/panel/releases" >/dev/null
  release_json="$(
    curl --config "$curl_config" \
      "${center_url}/v1/admin/panel/releases" |
      jq -c --arg version "$canonical_version" \
        '.releases[]? | select(.version == $version)'
  )"
fi

release_status="$(jq -r '.status' <<<"$release_json")"
existing_channel="$(jq -r '.channel' <<<"$release_json")"
if [[ "$existing_channel" != "$channel" ]]; then
  echo "Existing ${canonical_version} uses channel ${existing_channel}, expected ${channel}" >&2
  exit 1
fi
if [[ "$release_status" == "revoked" ]]; then
  echo "Center release ${canonical_version} is revoked and cannot be reused" >&2
  exit 1
fi

file_sha256() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  else
    shasum -a 256 "$1" | awk '{print $1}'
  fi
}

upload_artifact() {
  local arch="$1"
  local artifact="$2"
  local expected_sha existing_sha
  expected_sha="$(file_sha256 "$artifact")"
  existing_sha="$(
    jq -r --arg arch "$arch" \
      '.artifacts[]? | select(.os == "linux" and .arch == $arch) | .sha256' \
      <<<"$release_json"
  )"
  if [[ -n "$existing_sha" ]]; then
    if [[ "$existing_sha" != "$expected_sha" ]]; then
      echo "Existing ${canonical_version} linux/${arch} artifact has different bytes" >&2
      exit 1
    fi
    return
  fi
  if [[ "$release_status" != "draft" ]]; then
    echo "Published ${canonical_version} is missing linux/${arch}; immutable release cannot be repaired" >&2
    exit 1
  fi
  curl --config "$curl_config" \
    --request POST \
    --form "artifact=@${artifact};type=application/gzip" \
    "${center_url}/v1/admin/panel/releases/${canonical_version}/artifacts/linux/${arch}" >/dev/null
}

upload_artifact amd64 "$amd64_artifact"
upload_artifact arm64 "$arm64_artifact"

if [[ "$release_status" == "published" ]]; then
  echo "Center release ${canonical_version} is already published with matching artifacts"
  exit 0
fi

curl --config "$curl_config" \
  --request POST \
  "${center_url}/v1/admin/panel/releases/${canonical_version}/publish" >/dev/null

echo "Published ${canonical_version} to Center with rollout percentage ${rollout_percentage}"
