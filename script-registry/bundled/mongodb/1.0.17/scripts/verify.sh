#!/usr/bin/env bash
set -Eeuo pipefail
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"

validate_inputs
[[ -x "${install_dir}/bin/mongod" && -x "${install_dir}/bin/mongosh" && -f "${config_file}" && -f "${unit_file}" ]] || die "MongoDB runtime files are incomplete."
grep -Fqx '# Managed by Oneinstack MongoDB component' "${config_file}" || die "MongoDB configuration is not managed by Oneinstack."
grep -Eq '^[[:space:]]+authorization:[[:space:]]+enabled[[:space:]]*$' "${config_file}" || die "MongoDB authentication is not enabled."
validate_native_config "${config_file}"
emit_progress 20 service_status "正在检查 MongoDB systemd 状态"
service_is_active || die "MongoDB service is not active."
effective_port="$(config_scalar net port "${mongodb_port}")"
effective_bind="$(config_scalar net bindIp "${mongodb_bind_ip}")"
emit_progress 45 health_check "正在执行 MongoDB ping 与端口探针"
wait_for_mongodb "${effective_port}" "${effective_bind}"
[[ "$(actual_version)" == "${software_version}" ]] || die "MongoDB runtime version does not match ${software_version}."
limit_nofile="$(systemctl show mongod.service -p LimitNOFILE --value)"
limit_nproc="$(systemctl show mongod.service -p LimitNPROC --value)"
[[ "${limit_nofile}" =~ ^[0-9]+$ && "${limit_nofile}" -ge 64000 ]] || die "MongoDB LimitNOFILE is below 64000."
[[ "${limit_nproc}" =~ ^[0-9]+$ && "${limit_nproc}" -ge 64000 ]] || die "MongoDB LimitNPROC is below 64000."
emit_progress 82 finalize_state "正在提交 MongoDB 受管状态"
if [[ -f "${state_dir}/pending-version" ]]; then
  mv -f -- "${state_dir}/pending-version" "${state_dir}/version"
  mv -f -- "${state_dir}/pending-source-sha256" "${state_dir}/source-sha256"
fi
[[ -f "${state_dir}/version" && "$(<"${state_dir}/version")" == "${software_version}" ]] || die "MongoDB managed version state is invalid."
commit_transaction
emit_progress 100 verify_completed "MongoDB ${software_version} 运行验证通过"
