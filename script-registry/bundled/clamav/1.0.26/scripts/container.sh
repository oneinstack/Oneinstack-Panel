#!/usr/bin/env bash
# CentOS Linux 7 runs the pinned upstream LTS image in an isolated Docker
# container. Its archived EPEL 7 ClamAV 0.103 must never be the managed engine.
# This file is sourced by the existing action entrypoints after common.sh.
# shellcheck disable=SC2034,SC2154 # Shared state is supplied by common.sh and consumed across sourced action functions.

container_image_id=sha256:76cff2172141d860ad6485c86dc6d9bdc4a6b6e1128ed9d86aea5434ae55bf5f
container_runtime_version=1.4.6
container_name=oneinstack-clamav
container_uid=100
container_gid=101
container_docker=''

container_require_runtime() {
  [[ "${system_id}" == centos && "${system_version%%.*}" == 7 && "${host_arch}" == amd64 ]] ||
    die 'HOST_PROFILE_UNAVAILABLE: the verified CentOS 7 container profile is limited to amd64.'
  validate_inputs
  [[ "${data_dir}" =~ ^/[A-Za-z0-9_./-]+$ ]] ||
    die 'DATA_DIR is unsafe for the CentOS 7 container mount; use letters, digits, slash, dot, underscore, or hyphen.'
  [[ "${data_dir}" != "${state_root}" && "${data_dir}" != "${state_root}"/* && "${state_root}" != "${data_dir}"/* &&
     "${data_dir}" != "${log_dir}" && "${data_dir}" != "${log_dir}"/* && "${log_dir}" != "${data_dir}"/* ]] ||
    die 'DATA_DIR overlaps Oneinstack state or logs.'
  [[ ! -L "${data_dir}" && ! -L "${config_dir}" && ! -L "${log_dir}" ]] ||
    die 'MANAGED_PATH_UNSAFE: ClamAV data, configuration, and log directories cannot be symlinks.'
  container_docker="$(command -v docker 2>/dev/null || true)"
  [[ -n "${container_docker}" ]] || die 'HOST_DEPENDENCY_UNSUPPORTED: CentOS 7 requires an already installed Docker engine.'
  "${container_docker}" info --format '{{.ServerVersion}}' >/dev/null 2>&1 ||
    die 'HOST_DEPENDENCY_UNSUPPORTED: Docker daemon is unavailable.'
  local docker_run_help
  docker_run_help="$("${container_docker}" run --help 2>/dev/null || true)"
  [[ "${docker_run_help}" == *'--pull'* && "${docker_run_help}" == *'--mount'* && "${docker_run_help}" == *'--no-healthcheck'* ]] ||
    die 'HOST_DEPENDENCY_UNSUPPORTED: Docker run must support --pull, --mount, and --no-healthcheck for the pinned ClamAV profile.'
  command -v systemctl >/dev/null 2>&1 || die 'HOST_DEPENDENCY_UNSUPPORTED: systemd is required.'
  local memory_kib free_kib
  memory_kib="$(awk '/^MemTotal:/ {print $2}' /proc/meminfo)"
  [[ "${memory_kib}" =~ ^[0-9]+$ && "${memory_kib}" -ge 3145728 ]] ||
    die 'HOST_RESOURCE_UNAVAILABLE: the CentOS 7 ClamAV container needs at least 3 GiB RAM.'
  free_kib="$(df -Pk "$(dirname -- "${data_dir}")" | awk 'NR == 2 {print $4}')"
  [[ "${free_kib}" =~ ^[0-9]+$ && "${free_kib}" -ge 1048576 ]] ||
    die 'DISK_SPACE_INSUFFICIENT: at least 1 GiB must be free for ClamAV image and databases.'
}

container_image_present() {
  [[ "$("${container_docker}" image inspect "${container_image_id}" --format '{{.Id}}' 2>/dev/null || true)" == "${container_image_id}" ]]
}

container_owned() {
  [[ "$("${container_docker}" inspect "${container_name}" --format '{{index .Config.Labels "oneinstack.component"}}' 2>/dev/null || true)" == clamav &&
     "$("${container_docker}" inspect "${container_name}" --format '{{index .Config.Labels "oneinstack.package"}}' 2>/dev/null || true)" == "${package_version}" &&
     "$("${container_docker}" inspect "${container_name}" --format '{{.Image}}' 2>/dev/null || true)" == "${container_image_id}" ]]
}

container_validate_database() {
  local directory="$1" stem file
  for stem in main daily bytecode; do
    file="$(find "${directory}" -maxdepth 1 -type f \( -name "${stem}.cvd" -o -name "${stem}.cld" \) -print -quit)"
    [[ -n "${file}" ]] || die "DATABASE_MISSING: ${stem} signature database is missing."
    "${container_docker}" run --rm --pull never --network none --read-only --cap-drop ALL --user "${container_uid}:${container_gid}" \
      --mount "type=bind,source=${directory},target=/oneinstack-database,readonly" \
      --entrypoint /usr/bin/sigtool "${container_image_id}" --info "/oneinstack-database/$(basename -- "${file}")" >/dev/null 2>&1 ||
      die "DATABASE_INVALID: ${stem} signature database failed ClamAV 1.4.6 verification."
  done
  "${container_docker}" run --rm --pull never --network none --read-only --cap-drop ALL --user "${container_uid}:${container_gid}" \
    --memory 2g --memory-swap 2g \
    --mount "type=bind,source=${directory},target=/oneinstack-database,readonly" \
    --entrypoint /usr/bin/clamscan "${container_image_id}" \
    --database=/oneinstack-database --no-summary /etc/hosts >/dev/null 2>&1 ||
    die 'DATABASE_INVALID: ClamAV 1.4.6 could not load and scan with the complete database.'
}

container_database_build_timestamp() {
  local directory="$1" daily_file build_time
  daily_file="$(find "${directory}" -maxdepth 1 -type f \( -name daily.cvd -o -name daily.cld \) -print -quit)"
  [[ -n "${daily_file}" ]] || die 'DATABASE_MISSING: daily signature database is missing.'
  build_time="$("${container_docker}" run --rm --pull never --network none --read-only --cap-drop ALL --user "${container_uid}:${container_gid}" \
    --mount "type=bind,source=${directory},target=/oneinstack-database,readonly" \
    --entrypoint /usr/bin/sigtool "${container_image_id}" --info "/oneinstack-database/$(basename -- "${daily_file}")" |
    awk -F': ' '/^Build time:/ && !seen {print $2; seen=1}')" || die 'DATABASE_INVALID: daily signature build time cannot be read.'
  date -u -d "${build_time}" +%s 2>/dev/null || die 'DATABASE_INVALID: daily signature build time is invalid.'
}

container_validate_bundle() (
  [[ -d "${offline_package_path}" && ! -L "${offline_package_path}" && -f "${offline_package_path}/manifest.yaml" &&
     -f "${offline_package_path}/bundle-info" && -f "${offline_package_path}/files.sha256" &&
     -f "${offline_package_path}/image.tar.gz" ]] || die 'OFFLINE_BUNDLE_INVALID: CentOS 7 image Bundle is incomplete.'
  validate_bundle_inventory
  (cd -- "${offline_package_path}" && sha256sum --check --strict files.sha256) >/dev/null ||
    die 'OFFLINE_BUNDLE_CHECKSUM_MISMATCH: Bundle content verification failed.'
  grep -Fxq "component=clamav" "${offline_package_path}/bundle-info" &&
    grep -Fxq "packageVersion=${package_version}" "${offline_package_path}/bundle-info" &&
    grep -Fxq 'softwareVersion=latest' "${offline_package_path}/bundle-info" &&
    grep -Fxq 'osId=centos' "${offline_package_path}/bundle-info" &&
    grep -Fxq "osVersion=${system_version}" "${offline_package_path}/bundle-info" &&
    grep -Fxq 'architecture=amd64' "${offline_package_path}/bundle-info" &&
    grep -Fxq "imageId=${container_image_id}" "${offline_package_path}/bundle-info" &&
    grep -Eq '^[[:space:]]+id:[[:space:]]+clamav[[:space:]]*$' "${offline_package_path}/manifest.yaml" &&
    grep -Eq "^[[:space:]]+version:[[:space:]]+${package_version//./\\.}[[:space:]]*$" "${offline_package_path}/manifest.yaml" ||
    die 'OFFLINE_BUNDLE_PLATFORM_MISMATCH: Bundle component, version, OS, arch, or image differs.'
  # Docker load is local-only. It is needed during precheck so the bundled
  # sigtool can validate the CVD signatures before any service is touched.
  "${container_docker}" load --input "${offline_package_path}/image.tar.gz" >/dev/null ||
    die 'OFFLINE_IMAGE_INVALID: Docker could not load the bundled image.'
  container_image_present || die 'OFFLINE_IMAGE_IDENTITY_MISMATCH: loaded image does not match the locked digest.'
  # Panel deliberately extracts package files as root:root 0640/0750. Stage
  # database copies for the unprivileged validation container without changing
  # the immutable cached Bundle or weakening the Panel cache permissions.
  local validation_dir stem file declared_timestamp actual_timestamp
  validation_dir="$(mktemp -d /var/tmp/oneinstack-clamav-database.XXXXXX)"
  trap 'rm -rf -- "${validation_dir}"' EXIT
  chown "${container_uid}:${container_gid}" "${validation_dir}"
  chmod 0750 "${validation_dir}"
  for stem in main daily bytecode; do
    file="$(find "${offline_package_path}/database" -maxdepth 1 -type f \( -name "${stem}.cvd" -o -name "${stem}.cld" \) -print -quit)"
    [[ -n "${file}" ]] || die "OFFLINE_DATABASE_MISSING: ${stem} database is missing."
    install -o "${container_uid}" -g "${container_gid}" -m 0640 -- "${file}" "${validation_dir}/$(basename -- "${file}")"
  done
  container_validate_database "${validation_dir}"
  declared_timestamp="$(awk -F= '$1 == "databaseGeneratedAtUnix" {print $2; exit}' "${offline_package_path}/bundle-info")"
  actual_timestamp="$(container_database_build_timestamp "${validation_dir}")"
  [[ "${declared_timestamp}" == "${actual_timestamp}" ]] ||
    die 'OFFLINE_DATABASE_IDENTITY_MISMATCH: Bundle timestamp differs from the signed daily database.'
  validate_offline_database_age
)

container_prepare_image() {
  if [[ "${install_mode}" == offline ]]; then
    container_validate_bundle
  else
    "${container_docker}" pull --platform linux/amd64 "clamav/clamav@${container_image_id}" >/dev/null ||
      die 'RUNTIME_IMAGE_UNAVAILABLE: the pinned upstream ClamAV image cannot be pulled.'
    container_image_present || die 'RUNTIME_IMAGE_IDENTITY_MISMATCH: pulled image does not match the pinned digest.'
  fi
}

container_load_state() {
  persisted_data_dir
  local saved_mode
  saved_mode="$(awk -F= '$1 == "INSTALL_MODE" {print $2; exit}' "${install_parameters_file}" 2>/dev/null || true)"
  [[ -z "${saved_mode}" ]] || install_mode="${saved_mode}"
  database_generated_at_unix="$(awk -F= '$1 == "DATABASE_GENERATED_AT_UNIX" {print $2; exit}' "${install_parameters_file}" 2>/dev/null || true)"
}

container_healthy() {
  systemctl is-active --quiet "${service_name}.service" 2>/dev/null &&
    container_owned &&
    [[ -S "${socket_path}" ]] &&
    [[ "$(stat -c '%a' "${socket_path}" 2>/dev/null || true)" == 660 ]] &&
    [[ "$("${container_docker}" inspect "${container_name}" --format '{{.State.Running}}' 2>/dev/null || true)" == true ]] &&
    "${container_docker}" exec "${container_name}" clamdscan --config-file="${clamd_config}" --ping 1 >/dev/null 2>&1
}

container_prepare_paths() {
  if "${container_docker}" container inspect "${container_name}" >/dev/null 2>&1 && ! container_owned; then
    die 'CONTAINER_OWNERSHIP_CONFLICT: the reserved ClamAV container name belongs to another runtime.'
  fi
  [[ ! -e "/etc/systemd/system/${service_name}.service" || -f "${state_dir}/installed" ]] ||
    die 'MANAGED_UNIT_CONFLICT: a non-managed Oneinstack ClamAV unit already exists.'
  [[ ! -e "${config_dir}" || -f "${state_dir}/installed" ]] ||
    die 'MANAGED_CONFIG_CONFLICT: an unmanaged Oneinstack ClamAV configuration already exists.'
  : >"${state_dir}/container-transaction"
  if [[ ! -d "${data_dir}" ]]; then
    install -d -o "${container_uid}" -g "${container_gid}" -m 0750 -- "${data_dir}"
    : >"${state_dir}/data-created"
  fi
  if [[ ! -d "${log_dir}" ]]; then
    install -d -o "${container_uid}" -g "${container_gid}" -m 0750 -- "${log_dir}"
    : >"${state_dir}/log-created"
  fi
  install -d -o root -g "${container_gid}" -m 0750 -- "${config_dir}"
  for path in "${data_dir}" "${log_dir}"; do
    "${container_docker}" run --rm --pull never --network none --user "${container_uid}:${container_gid}" \
      --mount "type=bind,source=${path},target=/oneinstack-write-check" \
      --entrypoint /bin/sh "${container_image_id}" -c 'test -w /oneinstack-write-check && test -x /oneinstack-write-check' >/dev/null ||
      die "DATA_PERMISSION_DENIED: container user cannot write ${path}; existing ownership was preserved."
  done
}

container_write_units() {
  local update_mode="$1" checks="$2" interval
  interval=$((86400 / checks))
  install -d -m 0755 /etc/systemd/system
  cat >"/etc/systemd/system/${service_name}.service" <<EOF
[Unit]
Description=Oneinstack ClamAV 1.4 LTS (CentOS 7 container)
Requires=docker.service
After=docker.service

[Service]
Type=simple
RuntimeDirectory=oneinstack-clamav
RuntimeDirectoryMode=0750
ExecStartPre=/usr/bin/chown ${container_uid}:${container_gid} ${runtime_dir}
ExecStart=${container_docker} run --rm --pull=never --no-healthcheck --name ${container_name} --label oneinstack.component=clamav --label oneinstack.package=${package_version} --network none --memory 3g --memory-swap 3g --user ${container_uid}:${container_gid} --cap-drop ALL --security-opt no-new-privileges --read-only --tmpfs /tmp:rw,nosuid,size=64m --mount type=bind,source=${data_dir},target=${data_dir} --mount type=bind,source=${config_dir},target=${config_dir},readonly --mount type=bind,source=${log_dir},target=${log_dir} --mount type=bind,source=${runtime_dir},target=${runtime_dir} --entrypoint /usr/sbin/clamd ${container_image_id} --foreground --config-file=${clamd_config}
ExecStop=${container_docker} stop -t 15 ${container_name}
Restart=on-failure
RestartSec=5
TimeoutStartSec=180

[Install]
WantedBy=multi-user.target
EOF
  if [[ "${install_mode}" == center && "${update_mode}" == auto ]]; then
    cat >"/etc/systemd/system/${update_service_name}.service" <<EOF
[Unit]
Description=Oneinstack ClamAV 1.4 LTS signature update
Requires=docker.service
After=docker.service network-online.target
Wants=network-online.target

[Service]
Type=oneshot
TimeoutStartSec=900
ExecStart=${container_docker} run --rm --pull=never --network bridge --user ${container_uid}:${container_gid} --cap-drop ALL --security-opt no-new-privileges --read-only --tmpfs /tmp:rw,nosuid,size=64m --mount type=bind,source=${data_dir},target=${data_dir} --mount type=bind,source=${config_dir},target=${config_dir},readonly --mount type=bind,source=${log_dir},target=${log_dir} --entrypoint /usr/bin/freshclam ${container_image_id} --stdout --config-file=${freshclam_config}
EOF
    cat >"/etc/systemd/system/${update_timer_name}" <<EOF
[Unit]
Description=Oneinstack ClamAV signature update schedule

[Timer]
OnBootSec=5m
OnUnitActiveSec=${interval}s
Persistent=true
Unit=${update_service_name}.service

[Install]
WantedBy=timers.target
EOF
  else
    systemctl disable --now "${update_timer_name}" 2>/dev/null || true
    rm -f -- "/etc/systemd/system/${update_service_name}.service" "/etc/systemd/system/${update_timer_name}"
  fi
  systemctl daemon-reload
}

container_precheck() {
  require_root
  container_require_runtime
  validate_mandatory_access_prerequisites
  printf 'install_profile=clamav-centos7-container\ncomponent_package_version=%s\nsoftware_version=latest\npackage_manager=docker\nruntime_image=%s\ndata_dir=%s\nservice=%s\ninstall_mode=%s\n' \
    "${package_version}" "${container_image_id}" "${data_dir}" "${service_name}" "${install_mode}"
  [[ "${install_mode}" != offline ]] || container_validate_bundle
  if ! is_managed && find_existing >/dev/null; then
    snapshot_existing
    emit_progress 30 existing_installation '已创建原生 ClamAV 迁移快照'
  fi
  emit_progress 100 precheck_completed 'CentOS 7 amd64 ClamAV 1.4 LTS container prerequisites passed'
}

container_install() {
  require_root
  container_require_runtime
  if is_managed; then
    container_load_state
    if container_healthy; then emit_progress 100 already_installed 'ClamAV 已安装且健康'; return 0; fi
    die 'MANAGED_INSTALLATION_UNHEALTHY: refusing to overwrite an existing ClamAV container installation without a recoverable repair snapshot.'
  fi
  [[ -d "${migration_dir}" || -d "${external_dir}" ]] || snapshot_existing
  container_prepare_image
  install -d -m 0750 -- "${state_dir}"
  container_install_complete=false
  container_install_exit() {
    local code="$1"
    trap - EXIT INT TERM
    set +e
    [[ "${container_install_complete}" == true ]] || container_rollback
    exit "${code}"
  }
  trap 'container_install_exit $?' EXIT
  trap 'container_install_exit 130' INT
  trap 'container_install_exit 143' TERM
  container_prepare_paths
  disable_native_units
  runtime_user=clamav runtime_group=clamav cvd_certs_dir=''
  local update_mode=auto
  [[ "${install_mode}" == center ]] || update_mode=manual
  write_settings "${settings_file}" "${update_mode}" 12 database.clamav.net 10 100 400 100 16 10000 no
  write_clamd_config "${clamd_config}" "${socket_path}" 10 100 400 100 16 10000 no
  write_freshclam_config "${freshclam_config}" 12 database.clamav.net
  chown root:"${container_gid}" "${clamd_config}" "${freshclam_config}" "${config_dir}"
  chmod 0640 "${clamd_config}" "${freshclam_config}"
  container_write_units "${update_mode}" 12
  if [[ "${install_mode}" == offline ]]; then
    local stem file
    for stem in main daily bytecode; do
      file="$(find "${offline_package_path}/database" -maxdepth 1 -type f \( -name "${stem}.cvd" -o -name "${stem}.cld" \) -print -quit)"
      [[ -n "${file}" ]] || die "OFFLINE_DATABASE_MISSING: ${stem} database is missing."
      install -o "${container_uid}" -g "${container_gid}" -m 0640 -- "${file}" "${data_dir}/$(basename -- "${file}")"
    done
  else
    systemctl start "${update_service_name}.service" || die 'DATABASE_UPDATE_FAILED: ClamAV 1.4.6 FreshClam container failed.'
  fi
  container_validate_database "${data_dir}"
  systemctl enable --now "${service_name}.service" || die 'SERVICE_NOT_READY: ClamAV container could not start.'
  if [[ "${install_mode}" == center ]]; then systemctl enable --now "${update_timer_name}"; fi
  local attempts=60
  until container_healthy; do
    ((attempts--)) || die 'SERVICE_NOT_READY: ClamAV container did not create a healthy Unix Socket.'
    sleep 2
  done
  database_generated_at_unix=''
  [[ "${install_mode}" != offline ]] || database_generated_at_unix="$(awk -F= '$1 == "databaseGeneratedAtUnix" {print $2; exit}' "${offline_package_path}/bundle-info")"
  printf '%s\n' "${package_version}" >"${state_dir}/version"
  printf '%s\n' latest >"${state_dir}/software-version"
  printf '%s\n' "${system_id}" >"${state_dir}/system-id"
  printf '%s\n' "${system_version}" >"${state_dir}/system-version"
  printf '%s\n' "${host_arch}" >"${state_dir}/architecture"
  printf '%s\n' "${service_name}" >"${state_dir}/service"
  write_install_parameters
  printf 'runtime_type=container\nimage_id=%s\nruntime_user=%s\nruntime_group=%s\nclamav_runtime_version=%s\n' \
    "${container_image_id}" "${container_uid}" "${container_gid}" "${container_runtime_version}" >"${state_dir}/runtime-profile"
  chmod 0640 "${state_dir}/runtime-profile"
  [[ ! -d "${migration_dir}" ]] || { rm -rf -- "${external_dir}"; mv -- "${migration_dir}" "${external_dir}"; }
  : >"${state_dir}/installed"
  mv -- "${state_dir}/container-transaction" "${state_dir}/container-managed"
  container_install_complete=true
  trap - EXIT INT TERM
  emit_progress 100 install_completed 'CentOS 7 ClamAV 1.4.6 container, signatures, and Unix Socket verified'
}

container_verify() {
  require_root
  container_require_runtime
  is_managed || die 'NOT_MANAGED: ClamAV is not managed by Oneinstack.'
  container_load_state
  container_image_present || die 'RUNTIME_IMAGE_UNAVAILABLE: managed image is missing.'
  [[ -f "${clamd_config}" && -f "${freshclam_config}" && -f "${settings_file}" ]] ||
    die 'RUNTIME_PROFILE_INVALID: managed configuration is incomplete.'
  container_validate_database "${data_dir}"
  validate_database_age
  container_healthy || die 'SERVICE_NOT_READY: CentOS 7 ClamAV container or Unix Socket PING failed.'
  local actual_version
  actual_version="$("${container_docker}" exec "${container_name}" clamscan --version 2>/dev/null | sed -nE 's/^ClamAV ([0-9]+(\.[0-9]+){1,3}).*/\1/p' | head -n1)"
  [[ "${actual_version}" == "${container_runtime_version}" ]] ||
    die 'RUNTIME_VERSION_MISMATCH: running ClamAV engine differs from the pinned image profile.'
  if [[ "${install_mode}" == center && "$(current_update_mode)" == auto ]]; then
    systemctl is-enabled --quiet "${update_timer_name}" && systemctl is-active --quiet "${update_timer_name}" ||
      die 'UPDATE_SCHEDULE_UNAVAILABLE: FreshClam timer is not enabled and active.'
  fi
  printf 'ClamAV verification passed; service=%s socket=%s runtime=%s database_age_hours=%s\n' \
    "${service_name}" "${socket_path}" "${container_runtime_version}" "$(database_age_hours_from_data)"
}

container_status() {
  container_docker="$(command -v docker 2>/dev/null || true)"
  if ! is_managed; then
    printf 'component=clamav\nservice=%s\nload_state=not-found\nactive_state=inactive\nsub_state=dead\nunit_file_state=disabled\nruntime_version=\ncan_reload=false\n' "${service_name}"
    return 0
  fi
  local load active sub unit
  load="$(systemctl_property "${service_name}.service" LoadState)"
  active="$(systemctl_property "${service_name}.service" ActiveState)"
  sub="$(systemctl_property "${service_name}.service" SubState)"
  unit="$(systemctl_property "${service_name}.service" UnitFileState)"
  if [[ "${active}" == active ]]; then
    if [[ -z "${container_docker}" ]]; then
      active=failed sub=probe-unavailable
    elif ! container_owned; then
      active=failed sub=container-unowned
    elif [[ "$("${container_docker}" inspect "${container_name}" --format '{{.State.Running}}' 2>/dev/null || true)" != true ]]; then
      active=failed sub=container-stopped
    elif [[ ! -S "${socket_path}" ]]; then
      active=failed sub=socket-unavailable
    elif [[ "$(stat -c '%a' "${socket_path}" 2>/dev/null || true)" != 660 ]]; then
      active=failed sub=socket-permission
    elif ! "${container_docker}" exec "${container_name}" clamdscan --config-file="${clamd_config}" --ping 1 >/dev/null 2>&1; then
      active=failed sub=ping-failed
    fi
  fi
  printf 'component=clamav\nservice=%s\nload_state=%s\nactive_state=%s\nsub_state=%s\nunit_file_state=%s\nruntime_version=%s\ncan_reload=false\n' \
    "${service_name}" "${load:-not-found}" "${active:-inactive}" "${sub:-dead}" "${unit:-disabled}" "${container_runtime_version}"
}

container_service_action() {
  local action="$1"
  require_root
  container_require_runtime
  is_managed || die 'NOT_MANAGED: ClamAV is not managed by Oneinstack.'
  container_load_state
  case "${action}" in
    start|restart)
      systemctl "${action}" "${service_name}.service"
      local attempts=60
      until container_healthy; do ((attempts--)) || die 'SERVICE_NOT_READY: ClamAV Unix Socket PING failed.'; sleep 2; done
      ;;
    stop) systemctl stop "${service_name}.service" ;;
    *) die 'Unsupported ClamAV service action.' ;;
  esac
}

container_remove_managed() {
  [[ -f "${state_dir}/container-transaction" || -f "${state_dir}/container-managed" ]] || return 0
  systemctl stop "${service_name}.service" 2>/dev/null || true
  if [[ "$("${container_docker}" inspect "${container_name}" --format '{{.State.Running}}' 2>/dev/null || true)" == true ]]; then
    container_owned || die 'CONTAINER_OWNERSHIP_CONFLICT: refusing to stop an unowned ClamAV container.'
    "${container_docker}" stop -t 15 "${container_name}" >/dev/null ||
      die 'SERVICE_STOP_FAILED: managed ClamAV container did not stop.'
  fi
  [[ "$("${container_docker}" inspect "${container_name}" --format '{{.State.Running}}' 2>/dev/null || true)" != true ]] ||
    die 'SERVICE_STOP_FAILED: managed ClamAV container remains active.'
  systemctl disable "${service_name}.service" 2>/dev/null || true
  systemctl disable --now "${update_timer_name}" 2>/dev/null || true
  rm -f -- "/etc/systemd/system/${service_name}.service" "/etc/systemd/system/${update_service_name}.service" "/etc/systemd/system/${update_timer_name}"
  systemctl daemon-reload
  [[ ! -d "${config_dir}" ]] || rm -rf -- "${config_dir}"
  [[ ! -f "${state_dir}/log-created" ]] || rm -rf -- "${log_dir}"
}

container_rollback() {
  require_root
  container_require_runtime
  if [[ ! -f "${state_dir}/container-transaction" ]]; then
    printf 'ClamAV rollback found no active container transaction; existing installation preserved\n'
    return 0
  fi
  container_load_state
  container_remove_managed
  restore_external_state
  if [[ -f "${state_dir}/data-created" && ! -d "${external_dir}" && ! -d "${migration_dir}" ]]; then
    rm -rf -- "${data_dir}"
  fi
  rm -rf -- "${state_dir}"
  printf 'ClamAV CentOS 7 container rollback completed\n'
}

container_uninstall() {
  require_root
  container_require_runtime
  is_managed || die 'NOT_MANAGED: refusing to remove an external ClamAV installation.'
  container_load_state
  local policy="${UNINSTALL_DATA_POLICY:-preserve}" external=false
  [[ "${policy}" == preserve || "${policy}" == delete ]] || die 'UNINSTALL_DATA_POLICY must be preserve or delete.'
  [[ "${policy}" != delete || "${UNINSTALL_CONFIRM_DATA_DELETION:-false}" == true ]] ||
    die 'UNINSTALL_CONFIRM_DATA_DELETION=true is required to delete data.'
  [[ ! -d "${external_dir}" ]] || external=true
  container_remove_managed
  restore_external_state
  if [[ "${policy}" == delete && "${external}" == false ]]; then rm -rf -- "${data_dir}"; fi
  rm -rf -- "${state_dir}"
  printf 'ClamAV CentOS 7 container uninstalled; data_policy=%s\n' "${policy}"
}

container_config_revision() {
  sha256sum "${clamd_config}" "${freshclam_config}" "${settings_file}" | sha256sum | awk '{print $1}'
}

container_validate_candidate() {
  local candidate_dir="$1" candidate_socket="$2" candidate_name="oneinstack-clamav-config-$$" attempts=60 ready=false
  install -d -o "${container_uid}" -g "${container_gid}" -m 0750 -- "${runtime_dir}"
  "${container_docker}" run --rm --pull never --network none --user "${container_uid}:${container_gid}" \
    --mount "type=bind,source=${candidate_dir},target=${candidate_dir},readonly" \
    --entrypoint /usr/bin/freshclam "${container_image_id}" --debug --config-file="${candidate_dir}/freshclam.conf" --version >/dev/null ||
    die 'CONFIG_VALIDATE_FAILED: candidate FreshClam configuration is invalid.'
  "${container_docker}" run -d --name "${candidate_name}" --pull never --no-healthcheck --network none --memory 2g --memory-swap 2g \
    --user "${container_uid}:${container_gid}" --cap-drop ALL --security-opt no-new-privileges --read-only \
    --tmpfs /tmp:rw,nosuid,size=64m \
    --mount "type=bind,source=${data_dir},target=${data_dir},readonly" \
    --mount "type=bind,source=${candidate_dir},target=${candidate_dir},readonly" \
    --mount "type=bind,source=${log_dir},target=${log_dir}" \
    --mount "type=bind,source=${runtime_dir},target=${runtime_dir}" \
    --entrypoint /usr/sbin/clamd "${container_image_id}" --foreground --config-file="${candidate_dir}/clamd.conf" >/dev/null ||
    die 'CONFIG_VALIDATE_FAILED: candidate ClamAV container could not start.'
  while ((attempts > 0)); do
    if [[ -S "${candidate_socket}" ]] &&
      "${container_docker}" exec "${candidate_name}" clamdscan --config-file="${candidate_dir}/clamd.conf" --ping 1 >/dev/null 2>&1; then
      ready=true
      break
    fi
    [[ "$("${container_docker}" inspect "${candidate_name}" --format '{{.State.Running}}' 2>/dev/null || true)" == true ]] || break
    sleep 2
    ((attempts--))
  done
  if [[ "${ready}" != true ]]; then
    "${container_docker}" logs --tail 30 "${candidate_name}" >&2 || true
  fi
  "${container_docker}" stop "${candidate_name}" >/dev/null 2>&1 || true
  "${container_docker}" rm "${candidate_name}" >/dev/null 2>&1 || true
  rm -f -- "${candidate_socket}"
  [[ "${ready}" != true ]] || rm -f -- "${log_dir}/clamd-config-$$.log"
  [[ "${ready}" == true ]] || die 'CONFIG_VALIDATE_FAILED: candidate ClamAV Unix Socket did not answer PING.'
}

container_config() {
  container_require_runtime
  is_managed || die 'NOT_MANAGED: ClamAV managed configuration is unavailable.'
  container_load_state
  runtime_user=clamav runtime_group=clamav cvd_certs_dir=''
  [[ -f "${clamd_config}" && -f "${freshclam_config}" && -f "${settings_file}" ]] ||
    die 'CONFIG_UNAVAILABLE: managed configuration files are missing.'
  local operation="${ONEINSTACK_CONFIG_OPERATION:-get}" verbose
  if [[ "${operation}" == get ]]; then
    printf 'component=clamav\nrevision=%s\napply_mode=restart\n' "$(container_config_revision)"
    printf 'databaseUpdateMode=%s\nchecksPerDay=%s\ndatabaseMirror=%s\nmaxThreads=%s\nmaxQueue=%s\nmaxScanSizeMB=%s\nmaxFileSizeMB=%s\nmaxRecursion=%s\nmaxFiles=%s\n' \
      "$(current_update_mode)" "$(current_checks_per_day)" "$(current_database_mirror)" "$(current_max_threads)" \
      "$(current_max_queue)" "$(current_max_scan_size)" "$(current_max_file_size)" "$(current_max_recursion)" "$(current_max_files)"
    verbose=false; [[ "$(current_log_verbose)" == yes ]] && verbose=true
    printf 'logVerbose=%s\n' "${verbose}"
    printf 'runtime.socketPath=%s\nruntime.dataDir=%s\nruntime.configFile=%s\nruntime.serviceName=%s\nruntime.runUser=%s\nruntime.runGroup=%s\nruntime.installSource=%s\nruntime.version=%s\nruntime.systemdState=%s\nruntime.databaseAgeHours=%s\n' \
      "${socket_path}" "${data_dir}" "${clamd_config}" "${service_name}" "${container_uid}" "${container_gid}" "${install_mode}" "${container_runtime_version}" \
      "$(systemctl is-active "${service_name}.service" 2>/dev/null || true)" "$(database_age_hours_from_data)"
    return 0
  fi
  [[ "${operation}" == apply ]] || die 'Unsupported ClamAV configuration operation.'
  require_root
  local expected="${ONEINSTACK_CONFIG_REVISION:-}" update_mode="${ONEINSTACK_CONFIG_DATABASE_UPDATE_MODE:-}"
  local checks="${ONEINSTACK_CONFIG_CHECKS_PER_DAY:-}" mirror="${ONEINSTACK_CONFIG_DATABASE_MIRROR:-}"
  local threads="${ONEINSTACK_CONFIG_MAX_THREADS:-}" queue="${ONEINSTACK_CONFIG_MAX_QUEUE:-}"
  local scan_size="${ONEINSTACK_CONFIG_MAX_SCAN_SIZE_MB:-}" file_size="${ONEINSTACK_CONFIG_MAX_FILE_SIZE_MB:-}"
  local recursion="${ONEINSTACK_CONFIG_MAX_RECURSION:-}" files="${ONEINSTACK_CONFIG_MAX_FILES:-}"
  verbose="${ONEINSTACK_CONFIG_LOG_VERBOSE:-}"
  [[ "${expected}" =~ ^[0-9a-f]{64}$ && "$(container_config_revision)" == "${expected}" ]] ||
    die 'CONFIG_REVISION_CONFLICT: refresh the current ClamAV configuration before applying.'
  [[ "${update_mode}" == auto || "${update_mode}" == manual ]] || die 'Invalid databaseUpdateMode.'
  [[ "${checks}" =~ ^[0-9]+$ && "${checks}" -ge 1 && "${checks}" -le 24 ]] || die 'checksPerDay must be 1-24.'
  [[ -z "${mirror}" || "${mirror}" =~ ^[A-Za-z0-9][A-Za-z0-9.-]{0,252}$ ]] || die 'Invalid databaseMirror.'
  [[ -n "${mirror}" ]] || mirror=database.clamav.net
  [[ "${threads}" =~ ^[0-9]+$ && "${threads}" -ge 1 && "${threads}" -le 64 ]] || die 'maxThreads must be 1-64.'
  [[ "${queue}" =~ ^[0-9]+$ && "${queue}" -ge 1 && "${queue}" -le 512 ]] || die 'maxQueue must be 1-512.'
  [[ "${scan_size}" =~ ^[0-9]+$ && "${scan_size}" -ge 1 && "${scan_size}" -le 4096 ]] || die 'maxScanSizeMB must be 1-4096.'
  [[ "${file_size}" =~ ^[0-9]+$ && "${file_size}" -ge 1 && "${file_size}" -le "${scan_size}" ]] || die 'maxFileSizeMB must not exceed maxScanSizeMB.'
  [[ "${recursion}" =~ ^[0-9]+$ && "${recursion}" -ge 1 && "${recursion}" -le 100 ]] || die 'maxRecursion must be 1-100.'
  [[ "${files}" =~ ^[0-9]+$ && "${files}" -ge 1 && "${files}" -le 1000000 ]] || die 'maxFiles must be 1-1000000.'
  [[ "${verbose}" == true || "${verbose}" == false ]] || die 'Invalid logVerbose.'
  if [[ "${install_mode}" == offline ]]; then
    [[ "${update_mode}" == manual && "${mirror}" == "$(current_database_mirror)" ]] ||
      die 'OFFLINE_NETWORK_FORBIDDEN: offline ClamAV cannot enable updates or change databaseMirror.'
  fi
  [[ "${verbose}" == true ]] && verbose=yes || verbose=no
  local candidate_dir candidate_socket backup_root backup_dir was_active=false timer_was_active=false committed=false
  candidate_dir="$(mktemp -d "${config_dir}/.config-candidate.XXXXXX")"
  trap 'rm -rf -- "${candidate_dir}"' EXIT
  candidate_socket="${runtime_dir}/clamd-config-$$.sock"
  write_settings "${candidate_dir}/runtime-settings" "${update_mode}" "${checks}" "${mirror}" "${threads}" "${queue}" "${scan_size}" "${file_size}" "${recursion}" "${files}" "${verbose}"
  write_clamd_config "${candidate_dir}/clamd.conf" "${candidate_socket}" "${threads}" "${queue}" "${scan_size}" "${file_size}" "${recursion}" "${files}" "${verbose}"
  sed -i "s#^LogFile .*#LogFile ${log_dir}/clamd-config-$$.log#" "${candidate_dir}/clamd.conf"
  write_freshclam_config "${candidate_dir}/freshclam.conf" "${checks}" "${mirror}"
  chown -R root:"${container_gid}" "${candidate_dir}"
  chmod 0750 "${candidate_dir}"
  chmod 0640 "${candidate_dir}/clamd.conf" "${candidate_dir}/freshclam.conf"
  container_validate_candidate "${candidate_dir}" "${candidate_socket}"
  sed -i "s#^LogFile .*#LogFile ${log_dir}/clamd.log#" "${candidate_dir}/clamd.conf"
  sed -i "s#^LocalSocket .*#LocalSocket ${socket_path}#" "${candidate_dir}/clamd.conf"
  backup_root="${state_dir}/config-backups"
  install -d -m 0700 -- "${backup_root}"
  backup_dir="$(mktemp -d "${backup_root}/config-XXXXXX")"
  cp -a -- "${clamd_config}" "${freshclam_config}" "${settings_file}" "${backup_dir}/"
  systemctl is-active --quiet "${service_name}.service" && was_active=true
  systemctl is-active --quiet "${update_timer_name}" && timer_was_active=true
  container_config_rollback() {
    local code="$1"
    trap - EXIT INT TERM
    set +e
    if [[ "${committed}" == true ]]; then
      cp -a -- "${backup_dir}/clamd.conf" "${clamd_config}"
      cp -a -- "${backup_dir}/freshclam.conf" "${freshclam_config}"
      cp -a -- "${backup_dir}/runtime-settings" "${settings_file}"
      container_write_units "$(current_update_mode)" "$(current_checks_per_day)"
      [[ "${was_active}" != true ]] || systemctl restart "${service_name}.service"
      [[ "${timer_was_active}" != true ]] || systemctl enable --now "${update_timer_name}"
    fi
    rm -rf -- "${candidate_dir}"
    exit "${code}"
  }
  trap 'container_config_rollback $?' EXIT
  trap 'container_config_rollback 130' INT
  trap 'container_config_rollback 143' TERM
  committed=true
  mv -f -- "${candidate_dir}/clamd.conf" "${clamd_config}"
  mv -f -- "${candidate_dir}/freshclam.conf" "${freshclam_config}"
  mv -f -- "${candidate_dir}/runtime-settings" "${settings_file}"
  rm -rf -- "${candidate_dir}"
  trap - EXIT
  container_write_units "${update_mode}" "${checks}"
  systemctl restart "${service_name}.service"
  local attempts=60
  until container_healthy; do ((attempts--)) || die 'SERVICE_NOT_READY: ClamAV failed after configuration apply.'; sleep 2; done
  if [[ "${install_mode}" == center && "${update_mode}" == auto ]]; then systemctl enable --now "${update_timer_name}"; fi
  [[ "${was_active}" == true ]] || systemctl stop "${service_name}.service"
  trap - EXIT INT TERM
  emit_progress 100 config_applied 'ClamAV CentOS 7 container configuration validated and applied'
}
