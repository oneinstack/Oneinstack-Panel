package input

import (
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
)

type QueryParam struct {
	Page
	ID       int64
	Addr     string
	Port     string
	Root     string
	Password string
	Remark   string
	Type     string
	RDB      int
}

type AddParam struct {
	ID       int64
	Addr     string
	Name     string
	Port     string
	Root     string
	Password string
	Remark   string
	Type     string
}

type LibParam struct {
	Page
	ID       int64
	Name     string
	Root     string
	Password string
	Remark   string
}

// Validate checks if the fields of AddParam are valid and returns an error if any field is invalid.
func (p *AddParam) Validate() error {
	if err := validateAddr(p.Addr); err != nil {
		return err
	}
	if err := validatePort(p.Port); err != nil {
		return err
	}
	if err := validateRoot(p.Root); err != nil {
		return err
	}
	if err := validatePassword(p.Password); err != nil {
		return err
	}
	if err := validateType(p.Type); err != nil {
		return err
	}
	return nil
}

// validateAddr checks if the Addr is a valid IP address or domain name.
func validateAddr(addr string) error {
	// Check if it's a valid IP address.
	if net.ParseIP(addr) != nil {
		return nil
	}
	// If not, check if it's a valid domain name.
	if matched, _ := regexp.MatchString(`^([a-zA-Z0-9]([a-zA-Z0-9\-]{0,61}[a-zA-Z0-9])?\.)+[a-zA-Z]{2,}$`, addr); matched {
		return nil
	}
	return fmt.Errorf("invalid Addr: %s. It should be a valid IP address or domain name", addr)
}

// validatePort checks if the Port is within the valid range for ports (1-65535).
func validatePort(port string) error {
	portNum, err := strconv.Atoi(port)
	if err != nil || portNum < 1 || portNum > 65535 {
		return fmt.Errorf("invalid Port: %s. It should be a number between 1 and 65535", port)
	}
	return nil
}

// validateRoot can be customized according to what constitutes a valid Root value in your context.
func validateRoot(root string) error {
	// Placeholder for root validation logic.
	// For example, you might want to check that it's not empty.
	if strings.TrimSpace(root) == "" {
		return fmt.Errorf("Root cannot be empty ")
	}
	return nil
}

// validatePassword can be customized according to your password policy.
func validatePassword(password string) error {
	// Placeholder for password validation logic.
	// For example, you might want to check that it's not empty or meets certain complexity requirements.
	if strings.TrimSpace(password) == "" {
		return fmt.Errorf("Password cannot be empty ")
	}
	return nil
}

func validateType(t string) error {
	switch t {
	case "mysql":
		return nil
	case "pg":
		return nil
	case "sqlserver":
		return nil
	case "redis":
		return nil
	case "mongo":
		return nil
	}
	return fmt.Errorf("未知的存储服务")
}

type IDParam struct {
	ID int64
}
