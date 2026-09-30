#!/usr/bin/env bash
set -Eeuo pipefail
# shellcheck source=common.sh
source "$(dirname -- "${BASH_SOURCE[0]}")/common.sh"
require_root
managed || die 'MANAGED_STATE_MISSING: WebDAV installation is not managed.'
read_config
systemctl start webdav.service
verify_runtime
