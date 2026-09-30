#!/usr/bin/env bash
set -Eeuo pipefail
# shellcheck disable=SC1091,SC2154,SC2034
# shellcheck source=components/development/nodejs/scripts/common.sh
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"

require_root
check_common_commands
validate_inputs
detect_host
select_artifact
install -d -m 0755 -- "$(dirname -- "${install_dir}")"

if [[ -f "${state_file}" ]]; then
  load_managed_install_dir
  validate_managed_path "${install_dir}" INSTALL_DIR
  if verify_installed; then
    emit_progress 100 already_installed "${component_name} ${software_version} is already installed"
    exit 0
  fi
fi

if [[ "${route}" == source ]]; then
  check_build_resources
  emit_progress 15 dependencies "Preparing local or online source-build dependencies"
  install_build_dependencies
  select_compiler
else
  emit_progress 15 dependencies "Preparing local or online Node.js runtime dependencies"
  install_runtime_dependencies
fi

trap cleanup_transaction EXIT
prepare_transaction
resolve_artifact

build_root="$(mktemp -d "$(dirname -- "${install_dir}")/.nodejs-build.XXXXXX")"
stage_dir="${build_root}/install"
install -d -m 0755 -- "${stage_dir}"

if [[ "${route}" == binary ]]; then
  emit_progress 35 extract_binary "Extracting verified Node.js binary artifact"
  tar -xJf "${artifact}" --strip-components=1 -C "${stage_dir}"
else
  emit_progress 35 extract_source "Extracting verified Node.js source artifact"
  source_dir="${build_root}/source"
  install -d -m 0755 -- "${source_dir}"
  tar -xJf "${artifact}" --strip-components=1 -C "${source_dir}"
  jobs="$(getconf _NPROCESSORS_ONLN 2>/dev/null || printf '1')"
  [[ "${jobs}" =~ ^[0-9]+$ && "${jobs}" -gt 0 ]] || jobs=1
  ((jobs > 4)) && jobs=4
  emit_progress 50 compile_source "Compiling Node.js ${software_version} with ${jobs} job(s)"
  (
    cd -- "${source_dir}"
    ./configure --prefix="${stage_dir}"
    make -j"${jobs}"
    make install
  )
fi

verify_runtime_dir "${stage_dir}" || die "Staged Node.js ${software_version} runtime verification failed."
emit_progress 85 activate_runtime "Activating managed Node.js runtime"
mv -- "${stage_dir}" "${install_dir}"
stage_dir=""
configure_entrypoints
verify_installed || die "Activated Node.js ${software_version} runtime verification failed."
write_state
transaction_committed=true
emit_progress 100 install_completed "${component_name} ${software_version} installed via ${route} route"
