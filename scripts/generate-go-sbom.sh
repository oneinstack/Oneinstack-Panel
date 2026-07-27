#!/usr/bin/env bash

set -euo pipefail

if [[ $# -ne 1 ]]; then
  echo "Usage: $0 OUTPUT_DIR" >&2
  exit 2
fi

command -v cyclonedx-gomod >/dev/null 2>&1 || {
  echo "cyclonedx-gomod is required" >&2
  exit 1
}
command -v jq >/dev/null 2>&1 || {
  echo "jq is required" >&2
  exit 1
}

output_dir="$1"
script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
project_dir="$(cd "${script_dir}/.." && pwd)"

case "$output_dir" in
  /*) ;;
  *) output_dir="${project_dir}/${output_dir}" ;;
esac
mkdir -p "$output_dir"

sbom="${output_dir}/one-go.cdx.json"
license_report="${output_dir}/one-go-licenses.tsv"
module_inventory="${output_dir}/one-go-modules.txt"

cyclonedx-gomod mod \
  -licenses \
  -json \
  -output "$sbom" \
  "$project_dir"

(
  cd "$project_dir"
  go list -mod=readonly -m all >"$module_inventory"
)

jq -r '
  ["MODULE", "VERSION", "LICENSE_EVIDENCE"],
  (
    .components[] |
    [
      (((.group // "") + "/" + .name) | sub("^/"; "")),
      (.version // ""),
      (
        [
          .licenses[]?.license.id,
          .licenses[]?.license.name,
          .evidence.licenses[]?.license.id,
          .evidence.licenses[]?.license.name
        ]
        | map(select(. != null and . != ""))
        | unique
        | if length == 0 then "UNKNOWN" else join(",") end
      )
    ]
  )
  | @tsv
' "$sbom" >"$license_report"

echo "Generated ${sbom}"
echo "Generated ${license_report}"
echo "Generated ${module_inventory}"
