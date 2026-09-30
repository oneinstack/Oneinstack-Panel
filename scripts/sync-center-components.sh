#!/usr/bin/env bash
set -Eeuo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
panel_root="$(cd -- "${script_dir}/.." && pwd)"
center_root="${1:-$(cd -- "${panel_root}/../Oneinstack-Center" && pwd)}"
center_ref="${2:-HEAD}"
bundled_root="${panel_root}/script-registry/bundled"
components=(adminer apache caddy clamav docker docker-compose fail2ban firewalld halo mariadb mongodb mysql nginx nodejs openresty opensearch php phpmyadmin redis tengine tomcat webdav)

[[ -f "${center_root}/go.mod" && -d "${center_root}/components/production" ]] || {
  echo "Invalid Oneinstack-Center path: ${center_root}" >&2
  exit 2
}
center_commit="$(git -C "${center_root}" rev-parse --verify "${center_ref}^{commit}")" || {
  echo "Center commit does not exist: ${center_ref}" >&2
  exit 2
}

temporary_dir="$(mktemp -d "${TMPDIR:-/tmp}/oneinstack-component-sync.XXXXXX")"
cleanup() {
  case "${temporary_dir}" in
    "${TMPDIR:-/tmp}"/oneinstack-component-sync.*) rm -rf -- "${temporary_dir}" ;;
  esac
}
trap cleanup EXIT

mkdir -p -- "${temporary_dir}/source" "${temporary_dir}/packages"
git -C "${center_root}" archive "${center_commit}" |
  tar -xf - -C "${temporary_dir}/source"

records=()
for component in "${components[@]}"; do
  source_group=development
  case "${component}" in
    docker|docker-compose|fail2ban|firewalld|mariadb|mongodb|mysql|nginx|php|redis)
      source_group=production ;;
  esac
  source="${temporary_dir}/source/components/${source_group}/${component}"
  archive="${temporary_dir}/packages/${component}.tar.gz"
  extracted="${temporary_dir}/packages/${component}"
  [[ -f "${source}/manifest.yaml" ]] || {
    echo "Missing committed component: ${component}" >&2
    exit 1
  }
  (
    cd -- "${temporary_dir}/source"
    go run ./cmd/package -source "${source}" -output "${archive}"
  )
  mkdir -p -- "${extracted}"
  tar -xzf "${archive}" -C "${extracted}"
  identity="$(cd -- "${panel_root}" && go run ./cmd/bundled-package inspect "${extracted}")"
  IFS=$'\t' read -r package_id package_version package_digest <<<"${identity}"
  [[ "${package_id}" == "${component}" && "${package_version}" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ &&
     "${package_digest}" =~ ^[0-9a-f]{64}$ ]] || {
    echo "Invalid built package identity: ${component}" >&2
    exit 1
  }
  destination="${bundled_root}/${component}/${package_version}"
  if [[ -e "${destination}" ]]; then
    existing="$(cd -- "${panel_root}" && go run ./cmd/bundled-package inspect "${destination}")" || exit 1
    [[ "${existing}" == "${identity}" ]] || {
      echo "Refusing to replace immutable bundled package: ${destination}" >&2
      exit 1
    }
  fi
  records+=("${component}|${package_version}|${package_digest}")
done

lock_staged="${temporary_dir}/production-lock.json"
{
  printf '{"schemaVersion":1,"centerCommit":"%s","packages":[\n' "${center_commit}"
  for ((i=0; i<${#records[@]}; i++)); do
    IFS='|' read -r component package_version package_digest <<<"${records[i]}"
    ((i == 0)) || printf ',\n'
    printf '  {"component":"%s","version":"%s","sha256":"%s"}' \
      "${component}" "${package_version}" "${package_digest}"
  done
  printf '\n]}\n'
} >"${lock_staged}"

# Keep the store metadata from the same committed Center snapshot as the
# packages. The runtime verifies this catalog against the production lock.
python3 - "${temporary_dir}/source/internal/softwarecatalog" "${temporary_dir}/production-catalog.json" "${lock_staged}" <<'PY'
import base64
import json
import pathlib
import sys

source = pathlib.Path(sys.argv[1])
products = json.loads((source / "default_catalog.json").read_text())
components = {record["component"] for record in json.loads(pathlib.Path(sys.argv[3]).read_text())["packages"]}
selected = [product for product in products if product.get("component") in components]
if len(selected) != len(components) or {product["component"] for product in selected} != components:
    raise SystemExit("Center catalog does not contain all locked components")
for product in selected:
    icon_name = "docker" if product["component"] == "docker-compose" else product["component"]
    icon_png = source / "icons" / (icon_name + ".png")
    icon_svg = source / "icons" / (icon_name + ".svg")
    icon_path = icon_png if icon_png.is_file() else icon_svg
    icon = icon_path.read_bytes()
    mime = "image/png" if icon_path == icon_png else "image/svg+xml"
    product["icon"] = "data:" + mime + ";base64," + base64.b64encode(icon).decode("ascii")
pathlib.Path(sys.argv[2]).write_text(json.dumps(selected, ensure_ascii=False, separators=(",", ":")) + "\n")
PY

for record in "${records[@]}"; do
  IFS='|' read -r component package_version package_digest <<<"${record}"
  destination="${bundled_root}/${component}/${package_version}"
  [[ -d "${destination}" ]] && continue
  staged="${destination}.new"
  [[ ! -e "${staged}" ]] || {
    echo "Refusing to replace existing staged package: ${staged}" >&2
    exit 1
  }
  mkdir -p -- "$(dirname -- "${destination}")"
  cp -R -- "${temporary_dir}/packages/${component}" "${staged}"
  mv -- "${staged}" "${destination}"
done

lock_destination="${bundled_root}/production-lock.json"
lock_candidate="${bundled_root}/production-lock.json.new"
[[ ! -e "${lock_candidate}" ]] || {
  echo "Refusing to replace staged lock: ${lock_candidate}" >&2
  exit 1
}
cp -- "${lock_staged}" "${lock_candidate}"
mv -f -- "${lock_candidate}" "${lock_destination}"
catalog_candidate="${bundled_root}/production-catalog.json.new"
[[ ! -e "${catalog_candidate}" ]] || {
  echo "Refusing to replace staged catalog: ${catalog_candidate}" >&2
  exit 1
}
cp -- "${temporary_dir}/production-catalog.json" "${catalog_candidate}"
mv -f -- "${catalog_candidate}" "${bundled_root}/production-catalog.json"
(cd -- "${panel_root}" && go run ./cmd/bundled-package verify "${bundled_root}")
echo "Synchronized ${#components[@]} committed Center packages from ${center_commit}"
