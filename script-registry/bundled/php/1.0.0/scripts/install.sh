#!/usr/bin/env bash
set -Eeuo pipefail
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"
require_root; validate_inputs; check_host
emit_progress 5 install_dependencies "正在安装 PHP 编译依赖"
install_dependencies
emit_progress 15 prepare_account "正在准备 PHP-FPM 运行账户"
ensure_account
work_dir="$(mktemp -d /usr/local/src/oneinstack-php.XXXXXX)"
trap 'rm -rf -- "${work_dir}"' EXIT
archive="${work_dir}/php.tar.xz"
emit_progress 22 download "正在下载 PHP 源码"
download_verified "${archive}"
emit_progress 35 verify_checksum "PHP 源码校验完成"
emit_progress 40 extract "正在解压 PHP 源码"
tar -xJf "${archive}" -C "${work_dir}"
source_dir="${work_dir}/php-${patch_version}"; [[ -d "${source_dir}" ]] || die "Unexpected PHP archive layout."
cd "${source_dir}"
emit_progress 48 prepare_build "正在生成 PHP 编译配置"
./configure --prefix="${install_dir}" --with-config-file-path="${install_dir}/lib" \
  --with-config-file-scan-dir="${install_dir}/etc/php.d" \
  --enable-fpm --with-fpm-user="${run_user}" --with-fpm-group="${run_group}" \
  --with-openssl --with-zlib --with-curl --with-zip --with-sodium \
  --with-mysqli=mysqlnd --with-pdo-mysql=mysqlnd --with-pdo-sqlite --with-sqlite3 \
  --enable-bcmath --enable-calendar --enable-exif --enable-ftp --enable-gd \
  --with-freetype --with-jpeg --with-webp --enable-intl --enable-mbstring \
  --enable-opcache --enable-pcntl --enable-soap --enable-sockets \
  --with-gettext --with-iconv --with-xsl --with-password-argon2
emit_progress 58 compile "正在编译 PHP"
make -j"$(nproc)"
emit_progress 82 install_files "正在暂存 PHP 安装文件"
stage="${work_dir}/stage"; make INSTALL_ROOT="${stage}" install
install -D -m 0644 -- "${source_dir}/php.ini-production" "${stage}${install_dir}/lib/php.ini"
[[ -x "${stage}${install_dir}/sbin/php-fpm" ]] || die "Staged php-fpm binary is missing."
emit_progress 90 prepare_rollback "正在创建 PHP 回滚点"
prepare_rollback
install -d -m 0755 -- "$(dirname -- "${install_dir}")"
mv -- "${stage}${install_dir}" "${install_dir}"
printf '%s\n' "${software_version}" >"${state_dir}/pending-version"
printf '%s\n' "${patch_version}" >"${state_dir}/pending-patch-version"
emit_progress 100 install_completed "PHP 安装文件部署完成"
echo "PHP ${patch_version} binaries installed."
