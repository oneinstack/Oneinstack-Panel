package panelupdate

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var instanceIDPattern = regexp.MustCompile(`^panel-[a-f0-9]{32}$`)

func LoadOrCreateInstanceID(installDir string) (string, error) {
	installDir = filepath.Clean(strings.TrimSpace(installDir))
	if installDir == "" || installDir == "." {
		return "", fmt.Errorf("install directory is required")
	}
	fileName := filepath.Join(installDir, "panel-instance-id")
	if instanceID, exists, err := readInstanceID(fileName); err != nil {
		return "", fmt.Errorf("read panel instance ID: %w", err)
	} else if exists {
		return instanceID, nil
	}

	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return "", fmt.Errorf("generate panel instance ID: %w", err)
	}
	instanceID := "panel-" + hex.EncodeToString(random)
	if err := os.MkdirAll(installDir, 0750); err != nil {
		return "", fmt.Errorf("create panel instance directory: %w", err)
	}
	temporary, err := os.CreateTemp(installDir, ".panel-instance-id-*.tmp")
	if err != nil {
		return "", fmt.Errorf("create panel instance ID: %w", err)
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(0600); err != nil {
		temporary.Close()
		return "", fmt.Errorf("secure panel instance ID: %w", err)
	}
	if _, err := temporary.WriteString(instanceID + "\n"); err != nil {
		temporary.Close()
		return "", fmt.Errorf("write panel instance ID: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return "", fmt.Errorf("sync panel instance ID: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return "", fmt.Errorf("close panel instance ID: %w", err)
	}
	if err := os.Link(temporaryName, fileName); err != nil {
		if os.IsExist(err) {
			instanceID, exists, readErr := readInstanceID(fileName)
			if readErr != nil {
				return "", fmt.Errorf("read concurrently created panel instance ID: %w", readErr)
			}
			if exists {
				return instanceID, nil
			}
		}
		return "", fmt.Errorf("install panel instance ID: %w", err)
	}
	return instanceID, nil
}

func readInstanceID(fileName string) (string, bool, error) {
	info, err := os.Lstat(fileName)
	if os.IsNotExist(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return "", false, fmt.Errorf("stored panel instance ID must be a private regular file")
	}
	contents, err := os.ReadFile(fileName)
	if err != nil {
		return "", false, err
	}
	instanceID := strings.TrimSpace(string(contents))
	if !instanceIDPattern.MatchString(instanceID) {
		return "", false, fmt.Errorf("stored panel instance ID is invalid")
	}
	return instanceID, true, nil
}
