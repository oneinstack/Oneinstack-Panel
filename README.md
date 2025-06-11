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
- 🌐 Website/FTP management
- 🔄 Scheduled task management (Crontab)
- [x] 📊 Real-time log viewing and analysis
- [x] Database visual management
- [x] ⚡ Built-in BBR network acceleration optimization
- [x] 📡 Multi-language interface support

## 📦 Quick Installation

### System Requirements

- OS: CentOS 7+/Ubuntu 20.04+
- Memory: Recommended 1GB+
- Disk Space: At least 20GB free space
- Root privileges required

### Installation Commands

```bash
 wget -O install.sh https://.cos.ap-guangzhou.myqcloud.com/oneinstack/install.sh && chmod +x install.sh && ./install.sh

```

### Update

```bash
wget -O update.sh https://.cos.ap-guangzhou.myqcloud.com/oneinstack/update.sh && chmod +x update.sh && ./update.sh
```

After installation, visit: `http://your-server-ip:8089`

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
