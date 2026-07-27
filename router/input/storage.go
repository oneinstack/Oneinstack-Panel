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
	ID       int64  `json:"id"`
	Addr     string `json:"addr"`
	Port     string `json:"port"`
	Root     string `json:"root"`
	Password string `json:"password"`
	Remark   string `json:"remark"`
	Type     string `json:"type"`
	RDB      int    `json:"r_db"`
	Name     string `json:"name"`
}

type AddParam struct {
	ID       int64  `json:"id"`
	Addr     string `json:"addr"`
	Name     string `json:"name"`
	Port     string `json:"port"`
	Root     string `json:"root"`
	Password string `json:"password"`
	Remark   string `json:"remark"`
	Type     string `json:"type"`
}

type LibParam struct {
	Page
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Root     string `json:"root"`
	Password string `json:"password"`
	Remark   string `json:"remark"`
	Encoding string `json:"encoding"`
}

type LibraryCredentialVerification struct {
	PanelPassword string `json:"panelPassword" binding:"required"`
}

type UpdateLibraryCredentialParam struct {
	PanelPassword string `json:"panelPassword" binding:"required"`
	Password      string `json:"password"`
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
	if err := validateConnectionPassword(p.Password, p.Type, p.ID > 0); err != nil {
		return err
	}
	if err := validateType(p.Type); err != nil {
		return err
	}
	return nil
}

// validateAddr checks if the Addr is a valid IP address or domain name.
func validateAddr(addr string) error {
	addr = strings.TrimSpace(addr)
	if strings.EqualFold(addr, "localhost") {
		return nil
	}
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
func validateConnectionPassword(password, storageType string, update bool) error {
	if strings.ContainsRune(password, 0) || len(password) > 4096 {
		return fmt.Errorf("password contains invalid data")
	}
	if strings.TrimSpace(password) == "" && storageType == "mysql" && !update {
		return fmt.Errorf("MySQL password cannot be empty")
	}
	return nil
}

func validateType(t string) error {
	switch t {
	case "mysql":
		return nil
	case "redis":
		return nil
	}
	return fmt.Errorf("unsupported storage service: %s", t)
}

type IDParam struct {
	ID int64 `json:"id"`
}

type DeleteLibraryParam struct {
	ID          int64  `json:"id" binding:"required"`
	ConfirmName string `json:"confirmName" binding:"required"`
}

type DatabaseBackupParam struct {
	LibraryID int64 `json:"libraryId" binding:"required"`
}

type DatabaseRestoreParam struct {
	LibraryID   int64  `json:"libraryId" binding:"required"`
	BackupID    string `json:"backupId" binding:"required"`
	ConfirmName string `json:"confirmName" binding:"required"`
}

type DeleteDatabaseBackupParam struct {
	ConfirmName string `json:"confirmName" binding:"required"`
}

var databaseIdentifierPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,63}$`)
var databaseUserPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,31}$`)

func (p *LibParam) Validate() error {
	p.Name = strings.TrimSpace(p.Name)
	// A managed database always receives a dedicated account with the same
	// identifier. This keeps ownership obvious and prevents accidental reuse
	// of a privileged or unrelated account.
	p.Root = p.Name
	p.Encoding = strings.ToLower(strings.TrimSpace(p.Encoding))
	if p.ID <= 0 {
		return fmt.Errorf("database connection is required")
	}
	if !databaseIdentifierPattern.MatchString(p.Name) {
		return fmt.Errorf("database name must start with a letter and contain only letters, numbers, or underscores")
	}
	if !databaseUserPattern.MatchString(p.Root) {
		return fmt.Errorf("database username must start with a letter and contain only letters, numbers, or underscores")
	}
	if strings.TrimSpace(p.Password) != "" {
		if err := validateDatabaseUserPassword(p.Password); err != nil {
			return err
		}
	}
	switch p.Encoding {
	case "", "utf8mb4", "utf8mb3", "utf8", "gbk", "big5":
	default:
		return fmt.Errorf("unsupported database encoding: %s", p.Encoding)
	}
	if p.Encoding == "" || p.Encoding == "utf8" {
		p.Encoding = "utf8mb4"
	}
	return nil
}

func ValidateDatabaseUserPassword(password string) error {
	return validateDatabaseUserPassword(password)
}

func validateDatabaseUserPassword(password string) error {
	if strings.ContainsRune(password, 0) {
		return fmt.Errorf("database password contains invalid data")
	}
	if length := len(password); length < 12 || length > 128 {
		return fmt.Errorf("database password must contain 12 to 128 characters")
	}
	if strings.TrimSpace(password) != password {
		return fmt.Errorf("database password cannot start or end with whitespace")
	}
	return nil
}
