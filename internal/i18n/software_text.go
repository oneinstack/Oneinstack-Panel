package i18n

import (
	"golang.org/x/text/language"
	"golang.org/x/text/message"
	"golang.org/x/text/message/catalog"
)

// LocalizeSoftwareCategory translates built-in software-store category labels
// while keeping Category.Value unchanged for subsequent list filtering.
func LocalizeSoftwareCategory(locale, value string) string {
	if Canonical(locale) != LocaleEnUS {
		return value
	}
	if translated, ok := englishSoftwareCategories[value]; ok {
		return translated
	}
	return value
}

var englishSoftwareCategories = map[string]string{
	"全部":       "All",
	"建站":       "Websites",
	"数据库":      "Databases",
	"Web服务器":   "Web Servers",
	"运行环境":     "Runtime Environments",
	"缓存":       "Cache",
	"实用工具":     "Utilities",
	"容器":       "Containers",
	"安全":       "Security",
	"云存储":      "Cloud Storage",
	"中间件":      "Middleware",
	"研发协作":     "Development & Collaboration",
	"协作/效率":    "Collaboration & Productivity",
	"AI / 大模型": "AI / LLM",
	"其他":       "Other",
}

// LocalizeSoftwareDescription renders a software description from the local
// language pack. Descriptions without a registered translation keep the source
// value so extending the catalog does not break the response.
func LocalizeSoftwareDescription(locale, softwareKey, value string) string {
	if Canonical(locale) != LocaleEnUS {
		return value
	}
	translation := SoftwareDescriptionTranslation(softwareKey, value)
	if translation == "" {
		return value
	}
	printer := NewSoftwareDescriptionPrinter(locale, map[string]string{
		softwareKey: translation,
	})
	return RenderSoftwareDescription(printer, softwareKey, value)
}

func SoftwareDescriptionKey(softwareKey string) string {
	return "software." + softwareKey + ".describe"
}

func NewSoftwareDescriptionPrinter(locale string, translations map[string]string) *message.Printer {
	tag := language.Make(Canonical(locale))
	builder := catalog.NewBuilder()
	for key, value := range translations {
		_ = builder.SetString(tag, SoftwareDescriptionKey(key), value)
	}
	return message.NewPrinter(tag, message.Catalog(builder))
}

func RenderSoftwareDescription(printer *message.Printer, softwareKey, fallback string) string {
	if printer == nil {
		return fallback
	}
	return printer.Sprintf(message.Key(SoftwareDescriptionKey(softwareKey), fallback))
}

func SoftwareDescriptionTranslation(softwareKey, value string) string {
	if translation := softwareDescriptionTranslations[softwareKey]; translation != "" {
		return translation
	}
	return englishSoftwareDescriptions[value]
}

// softwareDescriptionTranslations mirrors the current Center software
// catalog. Keep these translations keyed by the stable product key rather
// than the persisted Chinese description: Center descriptions can change and
// the same product may have multiple cached versions in the Panel database.
var softwareDescriptionTranslations = map[string]string{
	"webserver":      "High-performance web server and reverse proxy",
	"tengine":        "Alibaba's open-source high-performance Nginx fork",
	"openresty":      "High-performance web platform with integrated Lua support",
	"caddy":          "Modern web server with automatic HTTPS",
	"apache":         "Mature and stable Apache HTTP web server",
	"php":            "OneinStack PHP-FPM runtime environment",
	"java":           "OpenJDK Java runtime and development environment",
	"tomcat":         "Java web application server",
	"db":             "MySQL database, using port 3306 by default; the Panel generates a random root password",
	"mariadb":        "Open-source MariaDB relational database",
	"percona":        "High-performance MySQL-compatible Percona Server database",
	"postgresql":     "Open-source PostgreSQL relational database",
	"mongodb":        "MongoDB document database",
	"nodejs":         "Node.js JavaScript runtime environment",
	"pureftpd":       "Lightweight and secure FTP service",
	"phpmyadmin":     "Manage MySQL and MariaDB through a web browser",
	"memcached":      "High-performance distributed in-memory cache",
	"redis":          "High-performance in-memory cache and data service",
	"firewalld":      "Linux dynamic firewall management service",
	"docker":         "Container runtime and Docker service management",
	"docker-compose": "Docker Compose CLI plugin for multi-container orchestration",
	"fail2ban":       "Controlled login and web intrusion detection and banning service",
	"halo":           "Modern open-source website building and content management platform",
	"typecho":        "Lightweight blog and content publishing system",
	"adminer":        "Lightweight web database management tool",
	"pgadmin":        "PostgreSQL web management tool",
	"mongo-express":  "MongoDB web management tool",
	"minio":          "S3-compatible object storage service",
	"rclone":         "Multi-cloud storage synchronization and mounting tool",
	"webdav":         "Standard WebDAV file service adapter",
	"backup-s3":      "S3-compatible object storage backup target adapter",
	"backup-oss":     "Alibaba Cloud OSS backup target adapter",
	"backup-cos":     "Tencent Cloud COS backup target adapter",
	"uptime-kuma":    "Service availability monitoring and alerting",
	"prometheus":     "Metrics collection and time-series data storage",
	"grafana":        "Metrics and log visualization platform",
	"loki":           "Lightweight log aggregation system",
	"waf":            "ModSecurity-based web application firewall adapter",
	"website-tamper": "Website file integrity monitoring and tamper protection adapter",
	"clamav":         "Open-source virus scanning service",
	"host-hardening": "Host account, SSH, permission, and audit baseline hardening adapter",
	"rabbitmq":       "Reliable message queue and management service",
	"opensearch":     "Full-text search and log analytics service",
	"gitea":          "Lightweight Git hosting platform",
	"jenkins":        "Continuous integration and automation platform",
	"woodpecker":     "Lightweight continuous integration platform",
	"harbor":         "Enterprise OCI container image registry",
	"ollama":         "Local large language model runtime service",
	"open-webui":     "Large language model web interface",
	"dify":           "Large language model application development and orchestration platform",
	"n8n":            "Visual workflow automation platform",
	"nextcloud":      "Self-hosted file collaboration and office platform",
	"vaultwarden":    "Lightweight self-hosted password management service",
	"nocobase":       "Extensible no-code/low-code platform",
	"umami":          "Privacy-friendly website analytics platform",
	"wordpress":      "Popular PHP content management and website building platform",

	// Legacy aliases kept for locally cached rows that predate the Center
	// catalog's webserver/db component mapping.
	"mysql":    "MySQL database",
	"nginx":    "Nginx reverse proxy service",
	"firewall": "Linux dynamic firewall management service",
}

var englishSoftwareDescriptions = map[string]string{
	"高性能 Web 与反向代理服务":                         "High-performance web server and reverse proxy",
	"阿里巴巴开源的高性能 Nginx 分支":                     "Alibaba's open-source high-performance Nginx fork",
	"集成 Lua 能力的高性能 Web 平台":                    "High-performance web platform with integrated Lua support",
	"现代化自动 HTTPS Web 服务器":                     "Modern web server with automatic HTTPS",
	"成熟稳定的 Apache HTTP Web 服务器":               "Mature and stable Apache HTTP web server",
	"OneinStack PHP-FPM 运行环境":                 "OneinStack PHP-FPM runtime environment",
	"MySQL 数据库，默认端口 3306，root 密码由 Panel 随机生成": "MySQL database, using port 3306 by default; the Panel generates a random root password",
	"MariaDB 开源关系型数据库":                        "Open-source MariaDB relational database",
	"MongoDB 文档型数据库":                          "MongoDB document database",
	"Mysql数据库":                                "MySQL database",
	"Redis缓存":                                 "Redis cache",
	"Nginx代理服务":                               "Nginx reverse proxy service",
	"Linux 动态防火墙管理服务":                         "Linux dynamic firewall management service",
}
