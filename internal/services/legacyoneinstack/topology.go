package legacyoneinstack

import (
	"net/url"
	"regexp"

	"oneinstack/internal/models"

	"gorm.io/gorm"
)

// IsProxyPair recognizes the script's LNMPA topology from the actual front-end
// vhost and Apache Listen directives. Two unrelated servers remain exclusive.
func IsProxyPair(db *gorm.DB, first, second string) bool {
	front := first
	if first == "apache" {
		front = second
	} else if second != "apache" {
		return false
	}
	if front != "nginx" && front != "openresty" && front != "tengine" {
		return false
	}
	f, err := Installed(db, front)
	if err != nil || f == nil {
		return false
	}
	b, err := Installed(db, "apache")
	if err != nil || b == nil {
		return false
	}
	backend, err := DecodeComponent(b.LegacyMetadata)
	if err != nil {
		return false
	}
	config, err := ReadFile(backend.Config)
	if err != nil {
		return false
	}
	listen := regexp.MustCompile(`(?mi)^\s*Listen\s+(?:127\.0\.0\.1:|\*:)?([0-9]+)\s*(?:#.*)?$`)
	ports := map[string]bool{}
	for _, match := range listen.FindAllStringSubmatch(string(config), -1) {
		ports[match[1]] = true
	}
	var sites []models.Website
	if db.Where("origin = ? AND engine = ? AND enabled = ?", Origin, front, true).Find(&sites).Error != nil {
		return false
	}
	for _, site := range sites {
		s, err := DecodeSite(site.LegacyMetadata)
		if err != nil {
			continue
		}
		data, err := ReadFile(s.Config)
		if err != nil {
			continue
		}
		target, err := url.Parse(directive(string(data), "proxy_pass", true))
		if err == nil && target.Scheme == "http" && (target.Hostname() == "127.0.0.1" || target.Hostname() == "localhost") && ports[target.Port()] {
			return true
		}
	}
	return false
}

func IsProxyPeerService(db *gorm.DB, component, service string) bool {
	if db == nil {
		return false
	}
	var peer models.Software
	if db.Where("installed = ? AND origin = ? AND service_name = ?", true, Origin, service).First(&peer).Error != nil {
		return false
	}
	return IsProxyPair(db, component, peer.Component)
}
