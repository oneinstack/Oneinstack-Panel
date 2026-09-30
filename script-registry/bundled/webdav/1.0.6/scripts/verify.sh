#!/usr/bin/env bash
set -Eeuo pipefail
# shellcheck source=common.sh
source "$(dirname -- "${BASH_SOURCE[0]}")/common.sh"
require_root
validate_common
managed || die 'MANAGED_STATE_MISSING: WebDAV installation is not managed.'
read_config
verify_runtime
if [[ -f "$rollback_dir/added-acl" ]]; then
  cat "$rollback_dir/added-acl" >>"$state_dir/added-acl"
fi
rm -rf -- "$rollback_dir"
emit_progress 100 verify_completed 'WebDAV service and authentication probes passed'
