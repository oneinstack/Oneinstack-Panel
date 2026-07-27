<h1 align="center">Oneinstack Server Management Panel</h1>

[![GitHub forks](https://img.shields.io/github/forks/oneinstack/Oneinstack-Panel)](https://github.com/oneinstack/Oneinstack-Panel/network)
[![GitHub stars](https://img.shields.io/github/stars/oneinstack/Oneinstack-Panel)](https://github.com/oneinstack/Oneinstack-Panel/stargazers)
[![GitHub license](https://img.shields.io/github/license/oneinstack/Oneinstack-Panel)](https://github.com/oneinstack/Oneinstack-Panel/blob/main/LICENSE)
![GitHub release](https://img.shields.io/github/v/release/oneinstack/Oneinstack-Panel)

> An open-source Linux server operation and maintenance management panel, making server management simpler, safer, and more efficient

## Language

- [English](README.md)
- [简体中文](README-zh.md)

## 🚀 Features

- 🛡️ Visual server status monitoring (CPU/Memory/Disk/Network)
- 🔧 One-click installation of common services/software (Nginx/MySQL/Redis etc.)
- 🔐 Automatic firewall configuration and management
- 🧾 HMAC-protected operation audit with filtering, integrity checks, and CSV export
- 🌐 Website/FTP management
- 🔄 Scheduled task management (Crontab)
- [x] 📊 Real-time log viewing and analysis
- [x] Database visual management
- [x] ⚡ Built-in BBR network acceleration optimization
- [x] 📡 Multi-language interface support

## 📦 Quick Installation

### Verified system requirements

- OS: Ubuntu 22.04 LTS or Ubuntu 24.04 LTS
- Architecture: amd64; arm64 packages are built but remain outside the first
  production compatibility matrix
- Memory: Recommended 1GB+
- Disk Space: At least 20GB free space
- Root privileges required

Download the matching release archive and its `.sha256` file, verify it, and
extract it. Create the administrator password file without exposing the
password in shell history:

```bash
sha256sum -c one-linux-amd64-v0.1.0.tar.gz.sha256
tar -xzf one-linux-amd64-v0.1.0.tar.gz
cd one-linux-amd64-v0.1.0
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

After configuring a trusted update public key and manifest URL, an
administrator can also install signed releases from **Settings → Panel
Update**, or use:

```bash
sudo one update check
sudo one update apply --yes
sudo one update status
```

The updater verifies the Ed25519 manifest, artifact size, and SHA-256 before
running a database migration preflight, atomically switching releases, and
checking readiness. A failed update restores the previous binary, database,
configuration, and bundled component scripts. See [BUILD.md](BUILD.md) for key
configuration, manual recovery, and release operations.

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

![alt text](img/3.png)

- System update notifications

### Application Management

- One-click installation:
  - Web Server: Nginx
  - Databases: MySQL/Redis
  - Runtimes: PHP/Java

### Website Management

- Static hosting
- Reverse Proxy

## 🛠️ Technology Stack

- Core Language: Go
- Frontend Framework: Vue.js
- Database: SQLite
- Process Management: Systemd

## 🤝 Contributions

We welcome contributions of all kinds!

## 📄 License

This project is licensed under the [Apache License 2.0](LICENSE).

---

> 🌍 Official Website: [https://oneinstack.com](https://oneinstack.com)  
> 🐛 Bug Report: [GitHub Issues](https://github.com/oneinstack/Oneinstack-Panel/issues)
