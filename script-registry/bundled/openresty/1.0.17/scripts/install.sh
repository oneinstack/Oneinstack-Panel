#!/usr/bin/env bash
# shellcheck source=common.sh
set -Eeuo pipefail
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"
require_root; validate_inputs; validate_upgrade_path; check_host
if [[ "${install_mode}" == "offline" ]]; then validate_offline_bundle; fi
emit_progress 5 install_dependencies "正在安装 OpenResty 编译依赖"
install_dependencies
emit_progress 10 check_port "正在复核 OpenResty 监听端口"
ensure_install_port_available
emit_progress 15 prepare_account "正在准备 OpenResty 运行账户"
ensure_account
install -d -m 0755 -- /usr/local/src
work_dir="$(mktemp -d /usr/local/src/oneinstack-openresty.XXXXXX)"
trap 'rm -rf -- "${work_dir}"' EXIT
archive="${work_dir}/${source_archive}"
emit_progress 22 download "正在下载 OpenResty 源码"
download_verified "${archive}"
emit_progress 35 verify_checksum "OpenResty 源码校验完成"
emit_progress 40 extract "正在解压 OpenResty 源码"
tar -xzf "${archive}" -C "${work_dir}"
source_dir="${work_dir}/openresty-${software_version}"; [[ -d "${source_dir}" ]] || die "Unexpected OpenResty archive layout."
cd "${source_dir}"
emit_progress 48 prepare_build "正在生成 OpenResty 编译配置"
configure_ssl_args=()
if [[ "${os_id}" == "centos" && "${os_major}" == "7" ]]; then
  openssl11_include="/usr/include/openssl11"
  openssl11_libdir="/usr/lib64/openssl11"
  [[ -f "${openssl11_include}/openssl/ssl.h" && -e "${openssl11_libdir}/libssl.so" &&
    -e "${openssl11_libdir}/libcrypto.so" ]] ||
    die "CentOS 7 requires openssl11-devel from EPEL; OpenSSL 1.0.2 cannot build this OpenResty release."
  configure_ssl_args=(
    "--with-cc-opt=-I${openssl11_include}"
    "--with-ld-opt=-L${openssl11_libdir} -Wl,-rpath,${openssl11_libdir}"
  )
fi
./configure "${configure_ssl_args[@]}" --prefix="${install_dir}" --user="${run_user}" --group="${run_group}" \
  --with-http_ssl_module --with-http_v2_module \
  --with-http_stub_status_module --with-http_sub_module --with-http_gzip_static_module \
  --with-http_realip_module --with-http_flv_module --with-http_mp4_module \
  --with-stream --with-stream_ssl_module --with-stream_ssl_preread_module --with-pcre-jit
if [[ "${os_id}" == "centos" && "${os_major}" == "7" ]]; then
  sed -Ei 's/(CJSON_CFLAGS="[^"]*)"/\1 -std=gnu99"/g' Makefile
  grep -Eq 'CJSON_CFLAGS="[^"]* -std=gnu99"' Makefile ||
    die "Unable to enable GNU C99 for bundled lua-cjson on CentOS 7."
fi
emit_progress 60 compile "正在编译 OpenResty"
build_jobs="$(getconf _NPROCESSORS_ONLN 2>/dev/null || echo 1)"
[[ "${build_jobs}" =~ ^[1-9][0-9]*$ ]] || build_jobs=1
make -j"${build_jobs}"
emit_progress 82 install_files "正在暂存 OpenResty 安装文件"
stage="${work_dir}/stage"; make install DESTDIR="${stage}"
[[ -x "${stage}${install_dir}/nginx/sbin/nginx" ]] || die "Staged OpenResty binary is missing."
emit_progress 90 prepare_rollback "正在创建 OpenResty 回滚点"
prepare_rollback
install -d -m 0755 -- "$(dirname -- "${install_dir}")"
mv -- "${stage}${install_dir}" "${install_dir}"
printf '%s\n' "${software_version}" >"${state_dir}/pending-version"
emit_progress 100 install_completed "OpenResty 安装文件部署完成"
echo "OpenResty ${software_version} binaries installed."
