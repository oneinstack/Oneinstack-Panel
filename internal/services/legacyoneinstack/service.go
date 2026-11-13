package legacyoneinstack

import (
	"context"
	_ "embed"
	"errors"
	"strings"
)

//go:embed service.sh
var ServiceScript string

// VerifyService binds controls to the exact service discovered during import.
// A replacement unit must be scanned again before Panel can control it.
func VerifyService(ctx context.Context, c Component) (map[string]string, error) {
	if c.Service == "" || c.UnitIdentity == "" || !safeName.MatchString(c.Service) {
		return nil, errors.New("EXTERNAL_SERVICE_CONFLICT: service identity has not been verified")
	}
	values, err := unit(ctx, c.Service)
	if err != nil {
		return nil, err
	}
	if unitIdentity(values) != c.UnitIdentity {
		return nil, errors.New("CONFIG_REVISION_CONFLICT: the legacy service changed; scan the environment again")
	}
	return values, nil
}

func RuntimeVersion(ctx context.Context, c Component) string {
	args := []string{"--version"}
	if c.ID == "memcached" {
		args = []string{"-h"}
	}
	if c.ID == "nginx" || c.ID == "openresty" || c.ID == "tengine" || c.ID == "apache" || strings.HasPrefix(c.ID, "php") {
		args = []string{"-v"}
	}
	data, err := command(ctx, c.Binary, args...)
	if err != nil {
		return ""
	}
	return runtimeVersion(c.ID, string(data))
}
