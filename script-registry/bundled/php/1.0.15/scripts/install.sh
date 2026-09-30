#!/usr/bin/env bash
set -Eeuo pipefail
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"
require_root; validate_inputs; check_host; validate_external_policy
[[ "${install_mode}" != "offline" ]] || validate_offline_bundle
case "${lifecycle_action}" in
  install)
    if managed_installation_present && [[ ! -f "${state_dir}/pending-version" ]]; then
      die_code COMPONENT_ALREADY_MANAGED "PHP is already managed by Oneinstack; use upgrade for a version change"
    fi
    ;;
  upgrade)
    managed_installation_present || die_code COMPONENT_NOT_MANAGED "Upgrade requires an existing Oneinstack-managed PHP installation"
    ;;
  *)
    die_code INVALID_ACTION "PHP install script received an invalid lifecycle action"
    ;;
esac
emit_progress 5 install_dependencies "正在安装 PHP 编译依赖"
install_dependencies
prepare_openssl_compatibility
emit_progress 15 prepare_account "正在准备 PHP-FPM 运行账户"
ensure_account
work_dir="$(mktemp -d /usr/local/src/oneinstack-php.XXXXXX)"
trap 'rm -rf -- "${work_dir}"' EXIT
if is_legacy_php; then
  emit_progress 16 prepare_openssl "正在准备 PHP legacy OpenSSL 兼容工具链"
  prepare_legacy_openssl
fi
archive="${work_dir}/${source_archive}"
if is_legacy_php; then
  emit_progress 18 prepare_build_profile "正在加载 PHP legacy 编译参数"
else
  emit_progress 18 prepare_libzip "正在检查 PHP ZIP 扩展依赖"
  prepare_libzip_dependency
fi
if [[ -n "${libzip_build_prefix}" ]]; then
  export PKG_CONFIG_PATH="${libzip_build_prefix}/lib/pkgconfig${PKG_CONFIG_PATH:+:${PKG_CONFIG_PATH}}"
  export CPPFLAGS="-I${libzip_build_prefix}/include ${CPPFLAGS:-}"
  export LDFLAGS="-L${libzip_build_prefix}/lib -Wl,-rpath,${install_dir}/lib ${LDFLAGS:-}"
fi
emit_progress 22 download "正在下载 PHP 源码"
download_verified "${archive}"
emit_progress 35 verify_checksum "PHP 源码校验完成"
emit_progress 40 extract "正在解压 PHP 源码"
case "${source_archive}" in
  *.tar.gz) tar -xzf "${archive}" -C "${work_dir}" ;;
  *.tar.xz) tar -xJf "${archive}" -C "${work_dir}" ;;
  *) die_code PACKAGE_INVALID "Unsupported PHP source archive format" ;;
esac
source_dir="${work_dir}/php-${patch_version}"; [[ -d "${source_dir}" ]] || die "Unexpected PHP archive layout."
patch_openssl_legacy_sources
cd "${source_dir}"
emit_progress 48 prepare_build "正在生成 PHP 编译配置"
configure_php_build
emit_progress 58 compile "正在编译 PHP"
if ! make -j"$(nproc)"; then
  die_code BUILD_FAILED "PHP 编译失败，请查看实时任务日志中的最后一个编译器错误"
fi
emit_progress 82 install_files "正在暂存 PHP 安装文件"
stage="${work_dir}/stage"; make INSTALL_ROOT="${stage}" install
install -D -m 0644 -- "${source_dir}/php.ini-production" "${stage}${install_dir}/lib/php.ini"
if [[ -n "${libzip_build_prefix}" ]]; then
  install -d -m 0755 -- "${stage}${install_dir}/lib"
  cp -a -- "${libzip_build_prefix}/lib"/libzip.so* "${stage}${install_dir}/lib/"
fi
[[ -x "${stage}${install_dir}/sbin/php-fpm" ]] || die "Staged php-fpm binary is missing."
emit_progress 90 prepare_rollback "正在创建 PHP 回滚点"
prepare_rollback
install -d -m 0755 -- "$(dirname -- "${install_dir}")"
mv -- "${stage}${install_dir}" "${install_dir}"
printf '%s\n' "${software_version}" >"${state_dir}/pending-version"
printf '%s\n' "${patch_version}" >"${state_dir}/pending-patch-version"
write_runtime_parameters "${state_dir}/pending-runtime-params"
emit_progress 100 install_completed "PHP 安装文件部署完成"
echo "component=php"
echo "version=${software_version}"
echo "version_line=${software_version%.*}.x"
echo "action=${lifecycle_action}"
echo "install=files_ready"
