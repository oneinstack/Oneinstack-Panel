#!/usr/bin/env bash
set -Eeuo pipefail
# shellcheck source=common.sh
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"

require_root
validate_inputs
select_artifacts
[[ -f "${state_dir}/pending-version" ]] || die "TRANSACTION_STATE_MISSING: no pending Halo installation exists."
[[ "$(cat "${state_dir}/pending-version")" == "${software_version}" ]] || die "TRANSACTION_VERSION_MISMATCH: pending Halo version changed."
restored=false
restore_on_error() {
  local code="${1:-$?}"
  if [[ "${restored}" != true ]]; then restored=true; restore_transaction; fi
  exit "${code}"
}
trap 'restore_on_error $?' ERR
trap 'restore_on_error 130' INT
trap 'restore_on_error 143' TERM
verify_runtime
trap - ERR INT TERM
commit_installation
rm -f -- "${state_dir}/pending-version" "${state_dir}/pending-jar-sha256"
emit_progress 100 verify_completed "Halo systemd、运行用户、JAR/JRE 摘要、端口与 readiness 验证通过"
printf 'Halo %s verification passed with private Temurin %s\n' "${software_version}" "${jre_version}"
