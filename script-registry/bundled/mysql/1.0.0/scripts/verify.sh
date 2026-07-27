#!/usr/bin/env bash
set -Eeuo pipefail
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"
validate_inputs
emit_progress 15 service_status "正在检查 MySQL 服务状态"
systemctl is-active --quiet mysql
emit_progress 40 health_check "正在执行 MySQL 连接健康检查"
"${install_dir}/bin/mysqladmin" --protocol=socket --socket=/run/mysqld/mysqld.sock ping >/dev/null 2>&1
emit_progress 65 verify_version "正在核对 MySQL 版本"
"${install_dir}/bin/mysqld" --version | grep -Fq "Ver ${patch_version}"
emit_progress 88 finalize_state "正在确认 MySQL 安装状态"
mv -f -- "${state_dir}/pending-version" "${state_dir}/version"
mv -f -- "${state_dir}/pending-patch-version" "${state_dir}/patch-version"
rm -f -- "${state_dir}/initialized-this-run"
rm -rf -- "${rollback_dir}"
emit_progress 100 verify_completed "MySQL 启动和健康检查通过"
echo "MySQL ${patch_version} verification passed."
