#!/usr/bin/env bash
set -Eeuo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
panel_root="$(cd -- "${script_dir}/.." && pwd)"
center_root="${1:-$(cd -- "${panel_root}/../Oneinstack-Center" 2>/dev/null && pwd)}"
package_version="${2:-1.0.0}"

[[ -f "${center_root}/go.mod" && -d "${center_root}/components/production" ]] || {
  echo "Invalid Oneinstack-Center path: ${center_root}" >&2
  exit 2
}
[[ "${package_version}" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || {
  echo "Invalid package version: ${package_version}" >&2
  exit 2
}

temporary_dir="$(mktemp -d "${TMPDIR:-/tmp}/oneinstack-component-sync.XXXXXX")"
cleanup() {
  case "${temporary_dir}" in
    "${TMPDIR:-/tmp}"/oneinstack-component-sync.*) rm -rf -- "${temporary_dir}" ;;
  esac
}
trap cleanup EXIT

for component in nginx mysql php redis; do
  archive="${temporary_dir}/${component}.tar.gz"
  extracted="${temporary_dir}/${component}"
  mkdir -p -- "${extracted}"
  (
    cd -- "${center_root}"
    go run ./cmd/package \
      -source "./components/production/${component}" \
      -output "${archive}"
  )
  tar -xzf "${archive}" -C "${extracted}"

  destination="${panel_root}/script-registry/bundled/${component}/${package_version}"
  staged="${destination}.new"
  case "${staged}" in
    "${panel_root}"/script-registry/bundled/*/"${package_version}.new") ;;
    *) echo "Refusing unsafe destination: ${staged}" >&2; exit 1 ;;
  esac
  rm -rf -- "${staged}"
  mkdir -p -- "$(dirname -- "${destination}")"
  mv -- "${extracted}" "${staged}"
  rm -rf -- "${destination}"
  mv -- "${staged}" "${destination}"
done

echo "Synchronized production Center packages into ${panel_root}/script-registry/bundled"
