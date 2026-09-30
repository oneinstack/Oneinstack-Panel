#!/usr/bin/env bash
set -Eeuo pipefail
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"
require_root
validate_inputs
phpmyadmin_require_php_runtime
phpmyadmin_require_fpm_runtime
phpmyadmin_route_ready ||
  die "PHPMA_ROUTE_NOT_CONFIGURED: the managed Web Server does not expose /phpMyAdmin/."
verify_installed ||
  die "PHPMA_HTTP_PROBE_FAILED: /phpMyAdmin/index.php did not return a phpMyAdmin page."
write_state
emit_progress 100 verify_completed "${component_name} verification completed"
