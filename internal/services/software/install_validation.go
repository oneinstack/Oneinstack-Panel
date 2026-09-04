package software

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"syscall"

	"oneinstack/internal/services/script"
	"oneinstack/router/input"
)

// InstallParameterError identifies a safe, client-actionable installation
// parameter error. It is returned before a software task is created.
type InstallParameterError struct {
	Field   string
	Message string
}

func (e *InstallParameterError) Error() string {
	if e == nil {
		return "software installation parameters are invalid"
	}
	if strings.TrimSpace(e.Field) == "" {
		return e.Message
	}
	return fmt.Sprintf("%s: %s", e.Field, e.Message)
}

// ValidateInstallParams resolves the component package and validates its
// manifest parameters without executing any component action. Version and
// port are common install inputs; all other inputs are validated according to
// the resolved package manifest, whose parameter set may vary by component.
func (installer *Installer) ValidateInstallParams(ctx context.Context, params *input.InstallParams) error {
	if params == nil {
		return &InstallParameterError{Field: "install", Message: "installation parameters are required"}
	}

	flatVersion := strings.TrimSpace(params.Version)
	parameterVersion := installParameterValue(params.Parameters, "software-version", "version")
	flatPort := strings.TrimSpace(params.Port)
	parameterPort := installParameterValue(params.Parameters, "port", "nginx-port", "nginxPort")

	NormalizeInstallParams(params)
	if flatVersion != "" && parameterVersion != "" && flatVersion != parameterVersion {
		return &InstallParameterError{
			Field:   "software-version",
			Message: "must match version when both fields are provided",
		}
	}
	if flatPort != "" && parameterPort != "" && flatPort != parameterPort {
		return &InstallParameterError{
			Field:   "port",
			Message: "must match the port parameter when both fields are provided",
		}
	}
	if strings.TrimSpace(params.Key) == "" {
		return &InstallParameterError{Field: "key", Message: "is required"}
	}
	if params.Version == "" {
		return &InstallParameterError{Field: "version", Message: "is required"}
	}
	if params.Port == "" {
		return &InstallParameterError{Field: "port", Message: "is required"}
	}
	port, err := strconv.Atoi(params.Port)
	if err != nil || port < 1 || port > 65535 {
		return &InstallParameterError{Field: "port", Message: "must be a valid port between 1 and 65535"}
	}
	if err := validatePortAvailable(ctx, port); err != nil {
		return err
	}

	if ctx == nil {
		ctx = context.Background()
	}
	scriptInfo, err := installer.getInstallScript(ctx, params, "install")
	if err != nil {
		return err
	}
	installer.setScriptParams(scriptInfo, params)
	if err := script.ValidateParameters(scriptInfo); err != nil {
		return &InstallParameterError{Field: "parameters", Message: err.Error()}
	}
	return nil
}

// ValidateInstallationParams validates an installation request using the
// same package and parameter rules as the production installer.
func ValidateInstallationParams(ctx context.Context, params *input.InstallParams) error {
	return NewInstaller().ValidateInstallParams(ctx, params)
}

// validatePortAvailable checks the requested TCP port before an installation
// task is persisted. The action scripts still need to check again at runtime,
// because another process can claim the port after this short probe closes its
// temporary listeners.
func validatePortAvailable(ctx context.Context, port int) error {
	if ctx == nil {
		ctx = context.Background()
	}

	address := net.JoinHostPort("", strconv.Itoa(port))
	for _, network := range []string{"tcp4", "tcp6"} {
		listener, err := (&net.ListenConfig{}).Listen(ctx, network, address)
		if err == nil {
			if closeErr := listener.Close(); closeErr != nil {
				return &InstallParameterError{
					Field:   "port",
					Message: fmt.Sprintf("port %d availability could not be confirmed", port),
				}
			}
			continue
		}
		if errors.Is(err, syscall.EADDRINUSE) {
			return &InstallParameterError{
				Field:   "port",
				Message: fmt.Sprintf("port %d is already in use", port),
			}
		}
		if network == "tcp6" && (errors.Is(err, syscall.EAFNOSUPPORT) ||
			errors.Is(err, syscall.EPROTONOSUPPORT) || errors.Is(err, syscall.EADDRNOTAVAIL)) {
			continue
		}
		return &InstallParameterError{
			Field:   "port",
			Message: fmt.Sprintf("port %d availability could not be confirmed", port),
		}
	}
	return nil
}
