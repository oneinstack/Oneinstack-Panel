#!/usr/bin/env bash
set -Eeuo pipefail
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"

require_root; validate_inputs; ensure_account
[[ -x "${install_dir}/bin/mongod" && -x "${install_dir}/bin/mongosh" ]] || die "MongoDB runtime binaries are missing."
emit_progress 10 prepare_directories "正在准备 MongoDB 数据与日志目录"
new_database=false
restored_database=false
retained_archive="$(retained_data_archive || true)"
if [[ -n "${retained_archive}" && -d "${data_dir}" && -n "$(find "${data_dir}" -mindepth 1 -maxdepth 1 -print -quit)" ]]; then
  restored_database=true
elif [[ ! -r "${state_dir}/version" && ! -f "${config_file}" ]]; then
  new_database=true
fi
if [[ ! -d "${data_dir}" ]]; then
  install -d -o "${run_user}" -g "${run_group}" -m 0750 -- "${data_dir}"
  [[ ! -d "${rollback_dir}" ]] || : >"${rollback_dir}/new-data"
elif [[ "${new_database}" == true && -n "$(find "${data_dir}" -mindepth 1 -maxdepth 1 -print -quit)" ]]; then
  die "EXTERNAL_DATA_DETECTED: ${data_dir} is non-empty and is not managed by Oneinstack."
fi
install -d -o "${run_user}" -g "${run_group}" -m 0750 -- "${log_dir}"
chown -R "${run_user}:${run_group}" "${data_dir}" "${log_dir}"
ensure_runtime_path_traversal "${run_user}" "${install_dir}" INSTALL_DIR
ensure_runtime_path_traversal "${run_user}" "${data_dir}" DATA_DIR
ensure_runtime_path_traversal "${run_user}" "${log_dir}" LOG_DIR
verify_runtime_path_access
emit_progress 20 runtime_permissions "MongoDB 运行目录权限已校验"
write_unit

if [[ "${restored_database}" == true ]]; then
  emit_progress 30 restore_configuration "正在恢复 Oneinstack 保留的 MongoDB 配置"
  install -m 0640 -- "${retained_archive}/mongod.conf" "${config_file}"
  chown root:"${run_group}" "${config_file}"
  grep -Eq '^[[:space:]]+authorization:[[:space:]]+enabled[[:space:]]*$' "${config_file}" ||
    die "AUTHENTICATION_REQUIRED: preserved MongoDB configuration does not enable authentication."
  validate_native_config "${config_file}"
  mongodb_port="$(config_scalar net port "${mongodb_port}")"
  mongodb_bind_ip="$(config_scalar net bindIp "${mongodb_bind_ip}")"
  emit_progress 52 restore_service "正在启动保留数据的 MongoDB 实例"
  service_start
  wait_for_mongodb "${mongodb_port}" "${mongodb_bind_ip}"
  emit_progress 68 verify_preserved_credentials "正在验证原 MongoDB 管理员凭据"
  restore_host="${mongodb_bind_ip%%,*}"
  case "${restore_host}" in 0.0.0.0) restore_host=127.0.0.1 ;; ::) restore_host=::1 ;; esac
  printf '%s\n' 'const u=process.env.ONEINSTACK_MONGODB_RESTORE_USERNAME; const p=process.env.ONEINSTACK_MONGODB_RESTORE_PASSWORD; quit(db.auth(u,p) ? 0 : 1);' |
    env ONEINSTACK_MONGODB_RESTORE_USERNAME="${admin_username}" ONEINSTACK_MONGODB_RESTORE_PASSWORD="${admin_password}" \
      "${install_dir}/bin/mongosh" --quiet --host "${restore_host}" --port "${mongodb_port}" --norc admin >/dev/null ||
    die "PRESERVED_DATA_AUTHENTICATION_FAILED: use the administrator username and password from the preserved MongoDB instance."
  : >"${state_dir}/password-configured"
elif [[ "${new_database}" == true ]]; then
  [[ -n "${admin_password}" ]] || die "MONGODB_ADMIN_PASSWORD is required for first initialization."
  emit_progress 30 bootstrap_config "正在发布仅回环、未启用认证的临时初始化配置"
  bootstrap="$(mktemp /etc/.oneinstack-mongod-bootstrap.XXXXXX)"
  write_mongod_config "${bootstrap}" "127.0.0.1" "${mongodb_port}" disabled 0 0 off 100
  validate_native_config "${bootstrap}"
  mv -f -- "${bootstrap}" "${config_file}"
  service_start
  wait_for_mongodb "${mongodb_port}" "127.0.0.1"
  emit_progress 52 create_admin "正在通过标准输入创建 MongoDB 管理员"
  printf '%s\n' 'const p=process.env.ONEINSTACK_MONGODB_BOOTSTRAP_PASSWORD; const u=process.env.ONEINSTACK_MONGODB_BOOTSTRAP_USERNAME; db.getSiblingDB("admin").createUser({user:u,pwd:p,roles:[{role:"root",db:"admin"}]});' |
    env ONEINSTACK_MONGODB_BOOTSTRAP_PASSWORD="${admin_password}" ONEINSTACK_MONGODB_BOOTSTRAP_USERNAME="${admin_username}" \
      "${install_dir}/bin/mongosh" --quiet --host 127.0.0.1 --port "${mongodb_port}" --norc admin >/dev/null
  : >"${state_dir}/password-configured"
  emit_progress 68 enable_authentication "正在原子发布强制认证配置"
  candidate="$(mktemp /etc/.oneinstack-mongod.XXXXXX)"
  write_mongod_config "${candidate}" "${mongodb_bind_ip}" "${mongodb_port}" enabled 0 0 off 100
  validate_native_config "${candidate}"
  mv -f -- "${candidate}" "${config_file}"
  service_restart
  wait_for_mongodb "${mongodb_port}" "${mongodb_bind_ip}"
else
  [[ -f "${config_file}" ]] || die "MANAGED_CONFIGURATION_MISSING: ${config_file} is unavailable."
  grep -Fqx '# Managed by Oneinstack MongoDB component' "${config_file}" || die "EXTERNAL_CONFIGURATION_DETECTED: refusing to use an unknown MongoDB configuration."
  grep -Eq '^[[:space:]]+authorization:[[:space:]]+enabled[[:space:]]*$' "${config_file}" || die "AUTHENTICATION_REQUIRED: managed MongoDB authentication is not enabled."
  validate_native_config "${config_file}"
  mongodb_port="$(config_scalar net port "${mongodb_port}")"
  mongodb_bind_ip="$(config_scalar net bindIp "${mongodb_bind_ip}")"
  service_restart
  wait_for_mongodb "${mongodb_port}" "${mongodb_bind_ip}"
fi
persist_install_parameters
emit_progress 100 configure_completed "MongoDB 认证配置与 systemd 服务已生效"
