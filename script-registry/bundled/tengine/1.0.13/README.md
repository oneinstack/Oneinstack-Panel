# Tengine stable package

Package version: 1.0.9. Software version: 3.1.0.

This package builds Tengine from the fixed source archive below and installs
the native Tengine executable, configuration, and PID paths:

- binary: /usr/local/tengine/sbin/tengine
- main configuration: /usr/local/tengine/conf/tengine.conf
- PID: /usr/local/tengine/logs/tengine.pid

Source URL: https://mirrors.oneinstack.com/oneinstack/src/tengine-3.1.0.tar.gz
Source SHA-256: 64ed7155c0c904ce0fe7199c21b8eb6c2abfc267278fa8af832c0cb781e864dc

## Installation modes

- Center/online mode downloads the fixed source archive and installs declared
  build dependencies through apt, dnf, yum, or zypper.
- Fully offline mode requires an exact Bundle for the host OS, version, and
  architecture. It verifies files.sha256 and installs only local packages;
  repository refresh, upstream download, and network fallback are disabled.
- If a managed web or log root has a restricted parent directory, installation
  grants only the Tengine worker user traverse access through a POSIX ACL. It
  does not recursively relax parent or managed-directory permissions.

## Parameters

Install parameters include SOFTWARE_VERSION=3.1.0, TENGINE_PORT=80,
PHP_FPM_SOCKET=/dev/shm/php-cgi.sock, INSTALL_DIR, WEB_ROOT, LOG_DIR,
WEB_VHOST_ROOT, RUN_USER, and RUN_GROUP. Runtime configuration exposes worker
processes/connections, keepalive timeout, request body size, listener port,
paths, and runtime ownership through configGet/configApply.

If phpMyAdmin is detected without an active PHP-FPM service and socket, the
installation remains successful and emits TENGINE_PHPMYADMIN_INTEGRATION_PENDING;
phpMyAdmin access is not claimed as verified.

The matrix is a compatibility declaration. Each listed OS and architecture
still requires real-host online and offline acceptance before it is certified.
