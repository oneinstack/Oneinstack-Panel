<h1 align="center">Oneinstack Server Management Panel</h1>

[![GitHub forks](https://img.shields.io/github/forks/oneinstack/Oneinstack-Panel)](https://github.com/oneinstack/Oneinstack-Panel/network)
[![GitHub stars](https://img.shields.io/github/stars/oneinstack/Oneinstack-Panel)](https://github.com/oneinstack/Oneinstack-Panel/stargazers)
[![GitHub license](https://img.shields.io/github/license/oneinstack/Oneinstack-Panel)](https://github.com/oneinstack/Oneinstack-Panel/blob/main/LICENSE)
![GitHub release](https://img.shields.io/github/v/release/oneinstack/Oneinstack-Panel)

> An open-source Linux server operation and maintenance management panel, making server management simpler, safer, and more efficient

## Language

- [English](README.md)
- [简体中文](README-zh.md)

Latest tagged build: `v0.3.0-build.11` (the main branch contains follow-up fixes)

## 🚀 Features

- 🛡️ Visual server status monitoring (CPU/Memory/Disk/Network)
- 🔧 Software store and component-package management (Nginx/MySQL/Redis/PHP, etc.)
- 🐳 Docker container, image, network, volume, Compose, and controlled terminal management
- 🔐 Firewall, port forwarding, auto-blocking, Fail2ban, and SSH management
- 🧾 HMAC-protected operation audit with filtering, integrity checks, and CSV export
- 🌐 Website, Nginx configuration, HTTPS certificates, and ACME renewal
- 📁 File management, trash, file sharing, database, and backup/restore workflows
- 🔄 Scheduled task management (Crontab)
- 📈 Metric history, service health checks, monitoring rules, alert events, and notification channels
- 🖥️ Bastion-host server management, connection tests, and metric collection
- 🧩 Configuration snapshots, diffs, preview execution, approvals, and audit
- [x] 📊 Real-time runtime log viewing and analysis
- [x] 📡 Multi-language API responses and user interface

## 📦 Quick Installation

### System requirements

- OS: Linux; verified on Ubuntu, Debian, CentOS, RHEL, Rocky, AlmaLinux,
  OpenCloudOS, and Anolis
- Architecture: linux/amd64 and linux/arm64
- Memory: Recommended 1GB+
- Disk Space: At least 20GB free space
- Root privileges, systemd, and `prlimit` required

Unverified Linux distributions can be explicitly installed with
`--allow-unsupported`; verified distributions are recommended for production.

Download the matching release archive and its `.sha256` file, verify it, and
extract it. Create the administrator password file without exposing the
password in shell history:

```bash
sha256sum -c one-linux-amd64-v0.3.0-build.11.tar.gz.sha256
tar -xzf one-linux-amd64-v0.3.0-build.11.tar.gz
cd one-linux-amd64-v0.3.0-build.11
read -r -s -p "Initial administrator password: " PANEL_PASSWORD
printf '\n'
sudo install -m 0600 /dev/null /run/one-admin-password
printf '%s\n' "$PANEL_PASSWORD" | sudo tee /run/one-admin-password >/dev/null
unset PANEL_PASSWORD
sudo ./install.sh --admin-user admin \
  --admin-password-file /run/one-admin-password
sudo rm -f /run/one-admin-password
```

After installation, visit: `http://your-server-ip:8089`

HTTP is the default and permanently available panel entry point. It listens
on `0.0.0.0` by default, so no domain is required for server-IP access. An
administrator may enable a separate HTTPS listener and certificate under
**Settings → Panel Access**; enabling HTTPS neither disables nor redirects
HTTP. The trusted-proxy list is empty by default and should contain only
proxy IP addresses or CIDR ranges under your control.

The installer uses only the binary and configuration bundled in the verified
release. It does not replace package repositories, disable the firewall,
change kernel parameters, or perform a system-wide upgrade.

To update from another verified, extracted release while preserving the
current configuration:

```bash
sudo ./install.sh --force
```

After configuring the Center URL and a trusted update public key, an
administrator can install the signed release assigned by Center from
**Settings → Panel Update**, or use:

```bash
sudo one update check
sudo one update apply --yes
sudo one update status
sudo one update rollback --yes
```

Center controls release channels, percentage rollout, instance targeting, and
version revocation. Panel does not trust Center's network response directly:
it verifies the pinned Ed25519 key, manifest, artifact size, and SHA-256 before
running a database migration preflight, atomically switching releases, and
checking readiness. A failed update restores the previous binary, database,
configuration, and bundled component scripts. See [BUILD.md](BUILD.md) for key
configuration, manual recovery, and release operations.

### Center-controlled software store and panel updates

The software store and component-package registry use the same trusted Center
connection. Configure `scriptCenter.enabled`, `scriptCenter.url`, and the
pinned Ed25519 public key in `scriptCenter.trustedKeys`. Panel downloads
`/v1/software/catalog` at startup and synchronizes it every 15 minutes by
default.

Center controls the applications and versions shown in the store, recommended
versions, whether new installations are allowed, ordering, tags, release
notes, and the component package used for each application. Panel applies a
new catalog in one database transaction only after verifying its signature and
revision. If Center is temporarily unavailable, the last verified snapshot
remains usable. An application removed from Center is no longer available for
new installations, while an already-installed instance remains visible for
service management and uninstallation.

```yaml
scriptCenter:
  enabled: true
  url: 'https://center.example.com'
  channel: 'stable'
  catalogSyncIntervalMinutes: 15
  catalogStaleAfterHours: 24
  trustedKeys:
    center-key-id: 'BASE64_ED25519_PUBLIC_KEY'

updateCenter:
  enabled: true
  centerUrl: 'https://center.example.com'
  channel: 'stable'
  healthTimeoutSeconds: 60
  backupRetention: 5
  trustedKeys: {}
```

For a loopback-only development Center using HTTP, set
`allowInsecureHTTP: true`. Production Center connections must use HTTPS.
Administrators can see the active data source on the software-store page and
manually refresh the catalog.

Normal uninstall preserves configuration, database, logs, and backups:

```bash
sudo ./install.sh uninstall
```

Permanent removal requires both destructive flags:

```bash
sudo ./install.sh uninstall --purge --yes
```

## 🖥️ Management Features

### Server Management

- Real-time resource monitoring

![alt text](img/1.png)

- Firewall rule configuration

![alt text](img/2.png)

- SSH port management
- System service management
- Scheduled task management
- Monitoring rules, service health checks, and alert notifications
- Configuration snapshots, preview execution, approvals, and audit
- Bastion-host servers and session metrics

![alt text](img/3.png)

- System update notifications

### Application Management

- Software store and signed component packages
- Software installation, upgrades, uninstallation, and service configuration
- Docker containers, images, networks, volumes, and Compose
- Database connections, backups, and restores

### Website Management

- Website lifecycle, configuration preview, and snapshot restore
- Static hosting and reverse proxy
- HTTPS certificates, ACME issuance, renewal, and disabling
- Website backups, restores, and task logs

## 🛠️ Technology Stack

- Core Language: Go
- Frontend Framework: Vue.js
- Database: SQLite
- Process Management: Systemd
- Production Targets: Linux amd64/arm64

## 🤝 Contributions

We welcome contributions of all kinds!

## 📄 License

This project is licensed under the [Apache License 2.0](LICENSE).

---

> 🌍 Official Website: [https://oneinstack.com](https://oneinstack.com)  
> 🐛 Bug Report: [GitHub Issues](https://github.com/oneinstack/Oneinstack-Panel/issues)
