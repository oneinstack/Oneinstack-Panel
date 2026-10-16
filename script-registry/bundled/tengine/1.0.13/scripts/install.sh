#!/usr/bin/env bash
set -Eeuo pipefail
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"
require_root; validate_inputs; check_host
if [[ "${install_mode}" == "offline" ]]; then validate_offline_bundle; fi
emit_progress 5 install_dependencies "正在安装 Tengine 编译依赖"
install_dependencies
emit_progress 15 prepare_account "正在准备 Tengine 运行账户"
ensure_account
install -d -m 0755 -- /usr/local/src
work_dir="$(mktemp -d /usr/local/src/oneinstack-tengine.XXXXXX)"
trap 'rm -rf -- "${work_dir}"' EXIT
archive="${work_dir}/${source_archive}"
emit_progress 22 download "正在下载 Tengine 源码"
download_verified "${archive}"
emit_progress 35 verify_checksum "Tengine 源码校验完成"
emit_progress 40 extract "正在解压 Tengine 源码"
tar -xzf "${archive}" -C "${work_dir}"
source_dir="${work_dir}/tengine-${software_version}"; [[ -d "${source_dir}" ]] || die "Unexpected Tengine archive layout."
cd "${source_dir}"
emit_progress 48 prepare_build "正在生成 Tengine 编译配置"
./configure --prefix="${install_dir}" --sbin-path="${tengine_binary}" --conf-path="${install_dir}/conf/tengine.conf" --pid-path="${tengine_pid_file}" \
	  --error-log-path="${log_dir}/tengine-error.log" --http-log-path="${log_dir}/tengine-access.log" --user="${run_user}" --group="${run_group}" \
  --with-http_ssl_module --with-http_v2_module \
  --with-http_stub_status_module --with-http_sub_module --with-http_gzip_static_module \
  --with-http_realip_module --with-http_flv_module --with-http_mp4_module \
  --with-stream --with-stream_ssl_module --with-stream_ssl_preread_module --with-pcre-jit
emit_progress 60 compile "正在编译 Tengine"
build_jobs="$(getconf _NPROCESSORS_ONLN 2>/dev/null || echo 1)"
[[ "${build_jobs}" =~ ^[1-9][0-9]*$ ]] || build_jobs=1
make -j"${build_jobs}"
emit_progress 82 install_files "正在暂存 Tengine 安装文件"
stage="${work_dir}/stage"; make install DESTDIR="${stage}"
[[ -x "${stage}${install_dir}/sbin/tengine" ]] || die "Staged Tengine binary is missing."
emit_progress 90 prepare_rollback "正在创建 Tengine 回滚点"
prepare_rollback
install -d -m 0755 -- "$(dirname -- "${install_dir}")"
mv -- "${stage}${install_dir}" "${install_dir}"
printf '%s\n' "${software_version}" >"${state_dir}/pending-version"
emit_progress 100 install_completed "Tengine 安装文件部署完成"
echo "Tengine ${software_version} binaries installed."
