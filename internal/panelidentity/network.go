package panelidentity

import (
	"encoding/base64"
	"encoding/json"
	"net"
	"sort"
	"strings"
)

const (
	NetworkInfoHeader     = "X-Oneinstack-Panel-Network-Info"
	maxHeaderValueBytes   = 4096
	maxNetworkInterfaces  = 32
	maxInterfaceAddresses = 64
)

type Interface struct {
	Name      string   `json:"name"`
	MAC       string   `json:"mac,omitempty"`
	Addresses []string `json:"addresses,omitempty"`
}

type Snapshot struct {
	Interfaces []Interface `json:"interfaces,omitempty"`
}

func HeaderValue() string {
	snapshot := collect()
	if len(snapshot.Interfaces) == 0 {
		return ""
	}
	contents, err := json.Marshal(snapshot)
	if err != nil {
		return ""
	}
	encoded := base64.RawURLEncoding.EncodeToString(contents)
	if len(encoded) > maxHeaderValueBytes {
		return ""
	}
	return encoded
}

func collect() Snapshot {
	interfaces, err := net.Interfaces()
	if err != nil {
		return Snapshot{}
	}
	result := make([]Interface, 0, len(interfaces))
	for _, networkInterface := range interfaces {
		item := Interface{
			Name: strings.TrimSpace(networkInterface.Name),
			MAC:  normalizeMAC(networkInterface.HardwareAddr.String()),
		}
		if item.Name == "" {
			continue
		}
		addresses, addressErr := networkInterface.Addrs()
		if addressErr == nil {
			for _, address := range addresses {
				if parsed := parseAddress(address.String()); parsed != "" {
					item.Addresses = append(item.Addresses, parsed)
				}
			}
		}
		item.Addresses = uniqueSorted(item.Addresses)
		result = append(result, item)
	}
	sort.Slice(result, func(left, right int) bool {
		return result[left].Name < result[right].Name
	})
	if len(result) > maxNetworkInterfaces {
		result = result[:maxNetworkInterfaces]
	}
	return Snapshot{Interfaces: result}
}

func parseAddress(value string) string {
	value = strings.TrimSpace(value)
	if slash := strings.IndexByte(value, '/'); slash >= 0 {
		value = value[:slash]
	}
	if zone := strings.LastIndexByte(value, '%'); zone >= 0 {
		value = value[:zone]
	}
	if ip := net.ParseIP(strings.Trim(value, "[]")); ip != nil {
		return ip.String()
	}
	return ""
}

func normalizeMAC(value string) string {
	address, err := net.ParseMAC(strings.TrimSpace(value))
	if err != nil {
		return ""
	}
	return address.String()
}

func uniqueSorted(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	if len(result) > maxInterfaceAddresses {
		result = result[:maxInterfaceAddresses]
	}
	return result
}
