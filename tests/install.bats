#!/usr/bin/env bats

setup() {
  PROJECT_DIR="$(cd "${BATS_TEST_DIRNAME}/.." && pwd)"
  INSTALLER="${PROJECT_DIR}/install.sh"
  TEST_TMPDIR="${BATS_TEST_TMPDIR:-${TMPDIR:-/tmp}}"
  TEST_ROOT="$(mktemp -d "${TEST_TMPDIR}/one-install-root.XXXXXX")"
  FIXTURE_DIR="$(mktemp -d "${TEST_TMPDIR}/one-install-fixture.XXXXXX")"
  FAKE_BINARY="${FIXTURE_DIR}/one"
  CONFIG_SOURCE="${FIXTURE_DIR}/config.yaml"

  printf '#!/usr/bin/env bash\nexit 0\n' >"${FAKE_BINARY}"
  chmod 0755 "${FAKE_BINARY}"
  printf 'system:\n  port: 8089\n' >"${CONFIG_SOURCE}"
}

teardown() {
  rm -rf -- "${TEST_ROOT}" "${FIXTURE_DIR}"
}

install_staged() {
  "${INSTALLER}" \
    --root "${TEST_ROOT}" \
    --binary "${FAKE_BINARY}" \
    --config "${CONFIG_SOURCE}" \
    --yes \
    "$@"
}

@test "staged install writes managed program, config, link, and service" {
  run install_staged
  if [ "$status" -ne 0 ]; then
    echo "$output" >&3
  fi
  [ "$status" -eq 0 ]

  [ -x "${TEST_ROOT}/usr/local/one/one" ]
  [ -f "${TEST_ROOT}/usr/local/one/config.yaml" ]
  [ -L "${TEST_ROOT}/usr/local/bin/one" ]
  [ "$(readlink "${TEST_ROOT}/usr/local/bin/one")" = "/usr/local/one/one" ]
  [ -f "${TEST_ROOT}/etc/systemd/system/one.service" ]
  [ -f "${TEST_ROOT}/etc/systemd/system/one-update.service" ]
  grep -Fq "# Managed by OneinStack Panel installer" \
    "${TEST_ROOT}/etc/systemd/system/one.service"
  grep -Fq "ExecStart=/usr/local/one/one server start" \
    "${TEST_ROOT}/etc/systemd/system/one.service"
  grep -Fq "ExecStart=/usr/local/one/one update apply --yes" \
    "${TEST_ROOT}/etc/systemd/system/one-update.service"
}

@test "repeat install needs force and force preserves config" {
  install_staged
  printf 'local-change: true\n' >"${TEST_ROOT}/usr/local/one/config.yaml"

  run install_staged
  [ "$status" -ne 0 ]
  [[ "$output" == *"--force"* ]]

  run install_staged --force
  [ "$status" -eq 0 ]
  grep -Fq "local-change: true" "${TEST_ROOT}/usr/local/one/config.yaml"
  [ -d "${TEST_ROOT}/usr/local/one/backups" ]
}

@test "replace config requires force and creates a backup" {
  install_staged
  printf 'local-change: true\n' >"${TEST_ROOT}/usr/local/one/config.yaml"

  run install_staged --replace-config
  [ "$status" -ne 0 ]

  run install_staged --force --replace-config
  [ "$status" -eq 0 ]
  grep -Fq "port: 8089" "${TEST_ROOT}/usr/local/one/config.yaml"
  ! grep -Fq "local-change: true" "${TEST_ROOT}/usr/local/one/config.yaml"
  find "${TEST_ROOT}/usr/local/one/backups" -name config.yaml -type f | grep -q .
}

@test "normal uninstall preserves config and data" {
  install_staged
  printf 'keep\n' >"${TEST_ROOT}/usr/local/one/operator-data"

  run "${INSTALLER}" uninstall --root "${TEST_ROOT}"
  [ "$status" -eq 0 ]
  [ ! -e "${TEST_ROOT}/usr/local/one/one" ]
  [ ! -e "${TEST_ROOT}/etc/systemd/system/one.service" ]
  [ ! -e "${TEST_ROOT}/etc/systemd/system/one-update.service" ]
  [ ! -e "${TEST_ROOT}/usr/local/bin/one" ]
  [ -f "${TEST_ROOT}/usr/local/one/config.yaml" ]
  [ -f "${TEST_ROOT}/usr/local/one/operator-data" ]
}

@test "purge requires explicit confirmation and removes only install directory" {
  install_staged
  printf 'sentinel\n' >"${TEST_ROOT}/sentinel"

  run "${INSTALLER}" uninstall --root "${TEST_ROOT}" --purge
  [ "$status" -ne 0 ]
  [ -d "${TEST_ROOT}/usr/local/one" ]

  run "${INSTALLER}" uninstall --root "${TEST_ROOT}" --purge --yes
  [ "$status" -eq 0 ]
  [ ! -e "${TEST_ROOT}/usr/local/one" ]
  [ -f "${TEST_ROOT}/sentinel" ]
}

@test "unsafe install directories are rejected" {
  run install_staged --install-dir /usr/local
  [ "$status" -ne 0 ]
  [[ "$output" == *"过宽"* ]]
}

@test "installer does not mutate repositories, firewall, or kernel settings" {
  run grep -En \
    'apt(-get)? (update|upgrade)|yum (update|upgrade)|dnf (update|upgrade)|ufw disable|firewall-cmd|sysctl -w|sources\.list|yum\.repos\.d' \
    "${PROJECT_DIR}/install.sh" \
    "${PROJECT_DIR}/install-ubuntu.sh" \
    "${PROJECT_DIR}/install-cent.sh"
  [ "$status" -eq 1 ]
}
