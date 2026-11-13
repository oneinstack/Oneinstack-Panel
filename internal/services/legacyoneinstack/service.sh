#!/usr/bin/env bash
set -euo pipefail

# All values come from verified, server-owned discovery metadata. This adapter
# never invokes install.sh, upgrade.sh, vhost.sh or options.conf.
case "${LEGACY_ACTION:?}" in start|stop|restart|reload) ;; *) exit 64 ;; esac
[[ "${LEGACY_SERVICE:?}" =~ ^[a-zA-Z0-9][a-zA-Z0-9._@-]*$ ]] || exit 64
if [[ "$LEGACY_ACTION" != stop ]]; then
  case "${LEGACY_COMPONENT:?}" in
    nginx|openresty|tengine) "${LEGACY_BINARY:?}" -t -c "${LEGACY_CONFIG:?}" ;;
    apache) "${LEGACY_BINARY:?}" -t -f "${LEGACY_CONFIG:?}" ;;
    php|php-*) "${LEGACY_INSTALL_DIR:?}/sbin/php-fpm" -t -y "${LEGACY_CONFIG:?}" ;;
  esac
fi
systemctl "$LEGACY_ACTION" "${LEGACY_SERVICE}.service"
if [[ "$LEGACY_ACTION" == stop ]]; then
  ! systemctl is-active --quiet "${LEGACY_SERVICE}.service"
else
  systemctl is-active --quiet "${LEGACY_SERVICE}.service"
fi
