#!/usr/bin/env bash
set -Eeuo pipefail
# shellcheck source=common.sh
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"

require_root
validate_path "${state_root}" ONEINSTACK_COMPONENT_STATE
if [[ -d "${rollback_dir}" ]]; then
  restore_transaction
  rm -f -- "${state_dir}/pending-version" "${state_dir}/pending-jar-sha256"
fi
emit_progress 100 rollback_completed "Halo 程序、配置、unit、文件数据与本事务 ACL 已恢复"
