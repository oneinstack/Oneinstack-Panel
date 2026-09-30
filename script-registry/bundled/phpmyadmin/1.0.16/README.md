# phpMyAdmin stable package

Package version: 1.0.16. Supported software versions: 4.4.15.10, 5.2.3.
The compatibility baseline is pinned to OneinStack commit 42d59b33765ad57c455b83bc3d4eb09ed367754a, and its archive
is verified with SHA-256 65a9164f7d9b6037e0771b28cbd04baec0207c28f556dd78b773853898b53dfa.

| Software version | Official artifact | SHA-256 |
| --- | --- | --- |
| 4.4.15.10 | https://files.phpmyadmin.net/phpMyAdmin/4.4.15.10/phpMyAdmin-4.4.15.10-all-languages.tar.gz | c28ba15b3b95b9d179b312f5c9fcd59a0593a315ddc6f7906f98a74508ffd32d |
| 5.2.3 | https://files.phpmyadmin.net/phpMyAdmin/5.2.3/phpMyAdmin-5.2.3-all-languages.tar.gz | 12ba1c425fa4071abbd4e7668c9ebdeac0b0755a467a6d6d5026122bb47c102b |

## Compatibility matrix

The manifest declares Ubuntu 20.04/22.04/24.04/26.04, Debian 11/12/13,
RHEL 8/9/10, Rocky Linux 8/9/10, AlmaLinux 8/9/10, CentOS 7/8/9/10,
Oracle Linux 8/9/10, Fedora, SUSE Linux Enterprise 15/16, openSUSE Leap,
openSUSE Tumbleweed, openSUSE, and Amazon Linux 2023.

Declared architectures: amd64 and arm64.

The installer discovers PHP-FPM and the active web server. It configures the
phpMyAdmin route only when it finds exactly one OneinStack-managed Nginx,
OpenResty, Tengine, Apache, or Caddy instance. Package-manager installations
and web servers whose configuration cannot be located are detected but never
rewritten automatically.

## Installation modes and parameters

- Center/online mode is the default. It downloads the selected
  SOFTWARE_VERSION from its pinned official URL, verifies SHA-256, and caches
  the verified archive locally.
- Fully offline mode requires ONEINSTACK_INSTALL_MODE=offline and an absolute
  ONEINSTACK_OFFLINE_PACKAGE_PATH. The bundle must contain manifest.yaml,
  files.sha256, and artifacts/phpmyadmin/<version>/*.tar.gz or *.tgz.
  Every artifact is checked against files.sha256 before extraction, and the
  install path does not access the network.
- Common inputs are SOFTWARE_VERSION and ONEINSTACK_COMPONENT_STATE.

## Managed installation

The managed directory is /data/wwwroot/phpMyAdmin. Compatible route aliases
include /data/wwwroot/phpmyadmin and /data/wwwroot/default/phpMyAdmin.
The scripts derive the document root, listen address, and port from the active
web-server configuration. They persist the PHP-FPM socket and service, web
server type and configuration path, and final URL in
<ONEINSTACK_COMPONENT_STATE>/phpmyadmin/installed.json.

## Validation boundary

The manifest matrix is a compatibility declaration, not proof that every OS,
version, and architecture has passed real-host acceptance. Each target host
must still run precheck, install, and verify, then validate web-server
syntax, the effective listener, and the actual HTTP response.
