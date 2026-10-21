#!/usr/bin/env bash
set -Eeuo pipefail
# shellcheck source=common.sh
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"

require_root
validate_inputs
check_host
validate_managed_upgrade_path
if [[ "${install_mode}" == offline ]]; then
  validate_offline_bundle
fi

if is_centos7_rpm_runtime; then
  dependency_message="正在安装 MariaDB CentOS 7 运行依赖"
else
  dependency_message="正在安装 MariaDB 源码构建依赖"
fi
emit_progress 5 install_dependencies "${dependency_message}"
install_dependencies

install -d -m 0755 -- /usr/local/src
work_dir="$(mktemp -d /usr/local/src/oneinstack-mariadb.XXXXXX)"
trap 'rm -rf -- "${work_dir}"' EXIT
stage_root="${work_dir}/stage-root"
stage_install="${stage_root}${install_dir}"
if is_centos7_rpm_runtime; then
  rpm_dir="${work_dir}/rpms"
  extraction_root="${work_dir}/rpm-root"
  emit_progress 18 rpm_resolve "正在获取并校验 MariaDB 官方 RHEL 7 RPM"
  download_centos7_rpm_set "${rpm_dir}"
  emit_progress 55 rpm_stage "正在生成 MariaDB CentOS 7 受管运行目录"
  stage_centos7_rpm_runtime "${rpm_dir}" "${stage_install}" "${extraction_root}"
else
  archive="${work_dir}/${source_archive}"
  signature="${archive}.asc"
  emit_progress 18 source_resolve "正在获取并校验 MariaDB 固定源码"
  download_verified "${archive}" "${signature}"
  source_dir="${work_dir}/source"
  build_dir="${work_dir}/build"
  install -d -m 0755 -- "${source_dir}" "${build_dir}" "${stage_root}"
  tar -xzf "${archive}" --strip-components=1 -C "${source_dir}"
  [[ -f "${source_dir}/CMakeLists.txt" ]] || die "MariaDB source archive is invalid."
  prepare_bundled_build_dependencies "${source_dir}"

  emit_progress 30 source_configure "正在配置 MariaDB 源码构建"
  cmake -S "${source_dir}" -B "${build_dir}" \
    -DCMAKE_BUILD_TYPE=Release \
    -DCMAKE_INSTALL_PREFIX="${install_dir}" \
    -DINSTALL_LAYOUT=STANDALONE \
    -DMYSQL_DATADIR="${data_dir}" \
    -DMYSQL_UNIX_ADDR=/run/mariadb/mariadb.sock \
    -DSYSCONFDIR="$(dirname -- "${config_file}")" \
    -DWITH_SSL=system \
    -DWITH_ZLIB=bundled \
    -DWITH_SYSTEMD=yes \
    -DWITH_UNIT_TESTS=OFF \
    -DPLUGIN_ROCKSDB=NO \
    -DPLUGIN_S3=NO \
    -DPLUGIN_OQGRAPH=NO \
    -DPLUGIN_MROONGA=NO \
    -DPLUGIN_TOKUDB=NO \
    -DPLUGIN_CONNECT=NO \
    -DPLUGIN_DUCKDB=NO \
    -DPLUGIN_COLUMNSTORE=NO

  cpu_jobs="$(nproc)"
  memory_kb="$(awk '/MemAvailable:/ {print $2; exit}' /proc/meminfo)"
  [[ "${memory_kb}" =~ ^[0-9]+$ ]] || memory_kb=2097152
  memory_jobs=$((memory_kb / 2097152))
  ((memory_jobs >= 1)) || memory_jobs=1
  build_jobs="${cpu_jobs}"
  ((build_jobs <= memory_jobs)) || build_jobs="${memory_jobs}"
  ((build_jobs <= 8)) || build_jobs=8

  emit_progress 42 source_build "正在编译 MariaDB，构建并发 ${build_jobs}"
  cmake --build "${build_dir}" --parallel "${build_jobs}"
  emit_progress 70 stage_install "正在生成 MariaDB staging 安装目录"
  DESTDIR="${stage_root}" cmake --install "${build_dir}"
fi
[[ -x "${stage_install}/bin/mariadbd" ]] || die "Staged mariadbd binary is missing."
[[ -x "${stage_install}/bin/mariadb" ]] || die "Staged mariadb client is missing."
[[ -x "${stage_install}/scripts/mariadb-install-db" || -x "${stage_install}/bin/mariadb-install-db" ]] ||
  die "Staged mariadb-install-db is missing."

emit_progress 78 prepare_account "正在准备 MariaDB 运行账户"
ensure_account
emit_progress 82 prepare_rollback "正在创建 MariaDB 事务回滚点"
prepare_rollback
emit_progress 90 install_files "正在原子部署 MariaDB 安装文件"
install -d -m 0755 -- "$(dirname -- "${install_dir}")"
mv -- "${stage_install}" "${install_dir}"
chown -R root:root "${install_dir}"
install -d -o "${run_user}" -g "${run_group}" -m 0750 -- "${data_dir}" "${log_dir}"
printf '%s\n' "${software_version}" >"${state_dir}/pending-version"
printf '%s\n' "${patch_version}" >"${state_dir}/pending-patch-version"
emit_progress 100 install_completed "MariaDB 安装文件部署完成"
printf 'MariaDB %s binaries verified and staged.\n' "${software_version}"
