package i18n

import (
	"golang.org/x/text/language"
	"golang.org/x/text/message"
	"golang.org/x/text/message/catalog"
)

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

// softwareDescriptionTranslations is the extensible part of the local
// language pack. Add a software key here when a new catalog application needs
// an English description.
var softwareDescriptionTranslations = map[string]string{
	"webserver": "High-performance web server and reverse proxy",
	"tengine":   "Alibaba's open-source high-performance Nginx fork",
	"openresty": "High-performance web platform with integrated Lua support",
	"caddy":     "Modern web server with automatic HTTPS",
	"apache":    "Mature and stable Apache HTTP web server",
	"php":       "OneinStack PHP-FPM runtime environment",
	"db":        "MySQL database, using port 3306 by default; the Panel generates a random root password",
	"mariadb":   "Open-source MariaDB relational database",
	"mongodb":   "MongoDB document database",
	"mysql":     "MySQL database",
	"redis":     "Redis cache",
	"nginx":     "Nginx reverse proxy service",
	"firewall":  "Linux dynamic firewall management service",
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
