#!/usr/bin/env bash
set -Eeuo pipefail
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"
require_root; validate_inputs; validate_install_version; check_host
emit_progress 5 install_dependencies "正在安装 Nginx 编译依赖"
install_dependencies
emit_progress 15 prepare_account "正在准备 Nginx 运行账户"
ensure_account
work_dir="$(mktemp -d /usr/local/src/oneinstack-nginx.XXXXXX)"
trap 'rm -rf -- "${work_dir}"' EXIT
archive="${work_dir}/nginx.tar.gz"
emit_progress 22 download "正在下载 Nginx 源码"
download_verified "${archive}"
emit_progress 35 verify_checksum "Nginx 源码校验完成"
emit_progress 40 extract "正在解压 Nginx 源码"
tar -xzf "${archive}" -C "${work_dir}"
source_dir="${work_dir}/nginx-${software_version}"; [[ -d "${source_dir}" ]] || die "Unexpected Nginx archive layout."
cd "${source_dir}"
emit_progress 48 prepare_build "正在生成 Nginx 编译配置"
./configure --prefix="${install_dir}" --user="${run_user}" --group="${run_group}" \
  --with-http_ssl_module --with-http_v2_module --with-http_v3_module \
  --with-http_stub_status_module --with-http_sub_module --with-http_gzip_static_module \
  --with-http_realip_module --with-http_flv_module --with-http_mp4_module \
  --with-stream --with-stream_ssl_module --with-stream_ssl_preread_module --with-pcre-jit
emit_progress 60 compile "正在编译 Nginx"
make -j"$(nproc)"
emit_progress 82 install_files "正在暂存 Nginx 安装文件"
stage="${work_dir}/stage"; make install DESTDIR="${stage}"
[[ -x "${stage}${install_dir}/sbin/nginx" ]] || die "Staged Nginx binary is missing."
emit_progress 90 prepare_rollback "正在创建 Nginx 回滚点"
prepare_rollback
install -d -m 0755 -- "$(dirname -- "${install_dir}")"
mv -- "${stage}${install_dir}" "${install_dir}"
printf '%s\n' "${software_version}" >"${state_dir}/pending-version"
emit_progress 100 install_completed "Nginx 安装文件部署完成"
echo "Nginx ${software_version} binaries installed."
