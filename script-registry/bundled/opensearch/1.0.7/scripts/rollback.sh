#!/usr/bin/env bash
set -Eeuo pipefail
# shellcheck source=common.sh
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"

require_root
load_install_parameters
validate_path "${state_root}" ONEINSTACK_COMPONENT_STATE
validate_path "${install_dir}" INSTALL_DIR
restore_snapshot
rm -f -- "${state_dir}/pending-version"
emit_progress 100 rollback_completed "OpenSearch 程序和 systemd 快照已恢复；数据目录未被清理"
