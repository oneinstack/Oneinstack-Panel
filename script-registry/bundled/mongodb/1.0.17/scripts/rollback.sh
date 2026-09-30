#!/usr/bin/env bash
set -Eeuo pipefail
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"
require_root; validate_inputs
restore_rollback
emit_progress 100 rollback_completed "MongoDB 事务回滚完成"
