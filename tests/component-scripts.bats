#!/usr/bin/env bats

setup() {
  PROJECT_DIR="$(cd "${BATS_TEST_DIRNAME}/.." && pwd)"
  REGISTRY_ROOT="${PROJECT_DIR}/script-registry/bundled"
  TEST_WORK="$(mktemp -d "${BATS_TEST_TMPDIR}/one-components.XXXXXX")"
  COMPONENTS=(nginx mysql php redis)
  REQUIRED_BIN="${TEST_WORK}/required-bin"
  mkdir -p "${REQUIRED_BIN}"
  if ! command -v curl >/dev/null 2>&1; then
    printf '%s\n' '#!/usr/bin/env bash' 'exit 64' >"${REQUIRED_BIN}/curl"
    chmod 0755 "${REQUIRED_BIN}/curl"
    PATH="${REQUIRED_BIN}:${PATH}"
    export PATH
  fi
}

teardown() {
  rm -rf -- "${TEST_WORK}"
}

component_version() {
  case "$1" in
    nginx) printf '%s' "1.28.2" ;;
    mysql) printf '%s' "8.0" ;;
    php) printf '%s' "8.3" ;;
    redis) printf '%s' "7.4.8" ;;
    *) return 1 ;;
  esac
}

component_root() {
  printf '%s/%s/1.0.0' "${REGISTRY_ROOT}" "$1"
}

run_validated_common() {
  local component="$1"
  local common
  common="$(component_root "${component}")/scripts/common.sh"
  run env \
    SOFTWARE_VERSION="$(component_version "${component}")" \
    MYSQL_PASSWORD="ComponentCi!2026" \
    REDIS_PASSWORD="RedisCi!2026" \
    bash -c 'source "$1"; validate_inputs' _ "${common}"
}

@test "component packages are checksum-complete and actions are executable shell" {
  local component root action
  for component in "${COMPONENTS[@]}"; do
    root="$(component_root "${component}")"
    run bash -c 'cd "$1" && sha256sum --check files.sha256' _ "${root}"
    if [ "$status" -ne 0 ]; then
      echo "${component}: ${output}" >&3
    fi
    [ "$status" -eq 0 ]
    for action in precheck install configure verify rollback uninstall; do
      [ -f "${root}/scripts/${action}.sh" ]
      run bash -n "${root}/scripts/${action}.sh"
      [ "$status" -eq 0 ]
    done
  done
}

@test "valid component parameters pass package validation" {
  local component
  for component in "${COMPONENTS[@]}"; do
    run_validated_common "${component}"
    if [ "$status" -ne 0 ]; then
      echo "${component}: ${output}" >&3
    fi
    [ "$status" -eq 0 ]
  done
}

@test "unsupported versions and broad install paths fail closed" {
  local component common
  for component in "${COMPONENTS[@]}"; do
    common="$(component_root "${component}")/scripts/common.sh"
    run env SOFTWARE_VERSION="0.0-invalid" bash -c 'source "$1"; validate_inputs' _ "${common}"
    [ "$status" -ne 0 ]
    [[ "$output" == *"Unsupported"* ]]

    run env \
      SOFTWARE_VERSION="$(component_version "${component}")" \
      INSTALL_DIR="/usr/local" \
      MYSQL_PASSWORD="ComponentCi!2026" \
      bash -c 'source "$1"; validate_inputs' _ "${common}"
    [ "$status" -ne 0 ]
    [[ "$output" == *"too broad"* ]]
  done
}

@test "database ports and secrets reject unsafe values" {
  local mysql_common redis_common
  mysql_common="$(component_root mysql)/scripts/common.sh"
  redis_common="$(component_root redis)/scripts/common.sh"

  run env SOFTWARE_VERSION="8.0" MYSQL_PORT="70000" MYSQL_PASSWORD="ComponentCi!2026" \
    bash -c 'source "$1"; validate_inputs' _ "${mysql_common}"
  [ "$status" -ne 0 ]
  [[ "$output" == *"Invalid MYSQL_PORT"* ]]

  run env SOFTWARE_VERSION="8.0" MYSQL_PASSWORD='bad password;rm' \
    bash -c 'source "$1"; validate_inputs' _ "${mysql_common}"
  [ "$status" -ne 0 ]
  [[ "$output" == *"MYSQL_PASSWORD"* ]]

  run env SOFTWARE_VERSION="7.4.8" REDIS_PORT="0" \
    bash -c 'source "$1"; validate_inputs' _ "${redis_common}"
  [ "$status" -ne 0 ]
  [[ "$output" == *"Invalid REDIS_PORT"* ]]

  run env SOFTWARE_VERSION="7.4.8" REDIS_PASSWORD='bad password;rm' \
    bash -c 'source "$1"; validate_inputs' _ "${redis_common}"
  [ "$status" -ne 0 ]
  [[ "$output" == *"REDIS_PASSWORD"* ]]
}

@test "precheck emits valid monotonic FD3 progress on supported Ubuntu hosts" {
  command -v jq >/dev/null
  local component precheck progress previous percent event_count
  for component in "${COMPONENTS[@]}"; do
    precheck="$(component_root "${component}")/scripts/precheck.sh"
    progress="${TEST_WORK}/${component}-progress.jsonl"
    run env \
      SOFTWARE_VERSION="$(component_version "${component}")" \
      MYSQL_PASSWORD="ComponentCi!2026" \
      REDIS_PASSWORD="RedisCi!2026" \
      bash -c 'exec 3>"$2"; export ONEINSTACK_PROGRESS_FD=3; exec bash "$1"' \
      _ "${precheck}" "${progress}"
    if [ "$status" -ne 0 ]; then
      echo "${component}: ${output}" >&3
    fi
    [ "$status" -eq 0 ]
    [ -s "${progress}" ]

    previous=-1
    event_count=0
    while IFS= read -r line; do
      run jq -e '.type == "progress" and (.percent | type == "number") and (.code | type == "string") and (.message | type == "string")' <<<"${line}"
      [ "$status" -eq 0 ]
      percent="$(jq -r '.percent' <<<"${line}")"
      [ "${percent}" -ge "${previous}" ]
      [ "${percent}" -ge 0 ]
      [ "${percent}" -le 100 ]
      previous="${percent}"
      event_count=$((event_count + 1))
    done <"${progress}"
    [ "${event_count}" -ge 3 ]
    [ "${previous}" -eq 100 ]
  done
}

@test "network and checksum failures stop before archive extraction" {
  local fake_bin component common
  fake_bin="${TEST_WORK}/fake-download-bin"
  mkdir -p "${fake_bin}"
  cat >"${fake_bin}/curl" <<'SCRIPT'
#!/usr/bin/env bash
set -euo pipefail
destination=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --output) destination="$2"; shift 2 ;;
    *) shift ;;
  esac
done
[[ -n "${destination}" ]]
printf '%s\n' "corrupted component archive" >"${destination}"
SCRIPT
  chmod 0755 "${fake_bin}/curl"

  for component in "${COMPONENTS[@]}"; do
    common="$(component_root "${component}")/scripts/common.sh"
    run env \
      PATH="${fake_bin}:${PATH}" \
      SOFTWARE_VERSION="$(component_version "${component}")" \
      bash -c 'source "$1"; download_verified "$2"' \
      _ "${common}" "${TEST_WORK}/${component}.archive"
    [ "$status" -ne 0 ]
    [[ "$output" == *"checksum"* ]]
  done

  cat >"${fake_bin}/curl" <<'SCRIPT'
#!/usr/bin/env bash
exit 7
SCRIPT
  chmod 0755 "${fake_bin}/curl"
  common="$(component_root nginx)/scripts/common.sh"
  run env PATH="${fake_bin}:${PATH}" SOFTWARE_VERSION="1.28.2" \
    bash -c 'source "$1"; download_verified "$2"' \
    _ "${common}" "${TEST_WORK}/network-failure.archive"
  [ "$status" -ne 0 ]
}

@test "rollback restores previous component files without touching other paths" {
  local fake_bin component common root
  fake_bin="${TEST_WORK}/fake-system-bin"
  mkdir -p "${fake_bin}"
  cat >"${fake_bin}/systemctl" <<'SCRIPT'
#!/usr/bin/env bash
if [[ "${1:-}" == "is-active" ]]; then
  exit 1
fi
exit 0
SCRIPT
  chmod 0755 "${fake_bin}/systemctl"

  for component in "${COMPONENTS[@]}"; do
    common="$(component_root "${component}")/scripts/common.sh"
    root="${TEST_WORK}/rollback-${component}"
    run env \
      PATH="${fake_bin}:${PATH}" \
      SOFTWARE_VERSION="$(component_version "${component}")" \
      bash -c '
        source "$1"
        component="$2"
        root="$3"
        install_dir="${root}/install"
        state_root="${root}/state"
        state_dir="${state_root}/${component}"
        rollback_dir="${state_dir}/rollback"
        unit_file="${root}/${component}.service"
        data_dir="${root}/data"
        config_file="${root}/my.cnf"
        mkdir -p "${install_dir}"
        printf old >"${install_dir}/marker"
        printf old-unit >"${unit_file}"
        [[ "${component}" != mysql ]] || printf old-config >"${config_file}"
        prepare_rollback
        mkdir -p "${install_dir}"
        printf new >"${install_dir}/marker"
        printf new-unit >"${unit_file}"
        restore_rollback
        [[ "$(cat "${install_dir}/marker")" == old ]]
        [[ "$(cat "${unit_file}")" == old-unit ]]
        [[ "${component}" != mysql ]] || [[ "$(cat "${config_file}")" == old-config ]]
      ' _ "${common}" "${component}" "${root}"
    if [ "$status" -ne 0 ]; then
      echo "${component}: ${output}" >&3
    fi
    [ "$status" -eq 0 ]
  done
}
