package utils

import (
	"fmt"
	"net"
	"oneinstack/core"
	"regexp"
	"strings"
	"unicode"
)

// ValidateUsername 验证用户名
func ValidateUsername(username string) *core.AppError {
	if username == "" {
		return core.NewFieldError(core.ErrBadRequest, "用户名不能为空", "username")
	}

	if len(username) < 3 {
		return core.NewFieldError(core.ErrBadRequest, "用户名长度不能少于3个字符", "username")
	}

	if len(username) > 32 {
		return core.NewFieldError(core.ErrBadRequest, "用户名长度不能超过32个字符", "username")
	}

	// 只允许字母、数字、下划线、连字符
	usernameRegex := regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)
	if !usernameRegex.MatchString(username) {
		return core.NewFieldError(core.ErrBadRequest, "用户名只能包含字母、数字、下划线和连字符", "username")
	}

	// 不能以数字开头
	if unicode.IsDigit(rune(username[0])) {
		return core.NewFieldError(core.ErrBadRequest, "用户名不能以数字开头", "username")
	}

	return nil
}

// ValidatePassword 验证密码（与系统服务中的密码验证保持一致）
func ValidatePassword(password string) *core.AppError {
	if len(password) < 8 {
		return core.NewFieldError(core.ErrWeakPassword, "密码长度不能少于8个字符", "password")
	}

	if len(password) > 128 {
		return core.NewFieldError(core.ErrWeakPassword, "密码长度不能超过128个字符", "password")
	}

	var (
		hasUpper   = false
		hasLower   = false
		hasNumber  = false
		hasSpecial = false
	)

	for _, char := range password {
		switch {
		case unicode.IsUpper(char):
			hasUpper = true
		case unicode.IsLower(char):
			hasLower = true
		case unicode.IsNumber(char):
			hasNumber = true
		case unicode.IsPunct(char) || unicode.IsSymbol(char):
			hasSpecial = true
		}
	}

	if !hasUpper {
		return core.NewFieldError(core.ErrWeakPassword, "密码必须包含至少一个大写字母", "password")
	}
	if !hasLower {
		return core.NewFieldError(core.ErrWeakPassword, "密码必须包含至少一个小写字母", "password")
	}
	if !hasNumber {
		return core.NewFieldError(core.ErrWeakPassword, "密码必须包含至少一个数字", "password")
	}
	if !hasSpecial {
		return core.NewFieldError(core.ErrWeakPassword, "密码必须包含至少一个特殊字符", "password")
	}

	// 检查常见弱密码
	commonPasswords := []string{
		"password", "123456", "123456789", "12345678", "12345", "1234567",
		"admin", "administrator", "root", "user", "test", "guest",
	}

	for _, common := range commonPasswords {
		if matched, _ := regexp.MatchString("(?i)"+common, password); matched {
			return core.NewFieldError(core.ErrWeakPassword, "密码包含常见弱密码模式", "password")
		}
	}

	return nil
}

// ValidateEmail 验证邮箱格式
func ValidateEmail(email string) *core.AppError {
	if email == "" {
		return core.NewFieldError(core.ErrBadRequest, "邮箱不能为空", "email")
	}

	emailRegex := regexp.MustCompile(`^[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}$`)
	if !emailRegex.MatchString(email) {
		return core.NewFieldError(core.ErrBadRequest, "邮箱格式不正确", "email")
	}

	if len(email) > 254 {
		return core.NewFieldError(core.ErrBadRequest, "邮箱长度不能超过254个字符", "email")
	}

	return nil
}

// ValidatePort 验证端口号
func ValidatePort(port string) *core.AppError {
	if port == "" {
		return core.NewFieldError(core.ErrBadRequest, "端口号不能为空", "port")
	}

	portRegex := regexp.MustCompile(`^[0-9]+$`)
	if !portRegex.MatchString(port) {
		return core.NewFieldError(core.ErrBadRequest, "端口号必须是数字", "port")
	}

	portNum := 0
	if _, err := fmt.Sscanf(port, "%d", &portNum); err != nil {
		return core.NewFieldError(core.ErrBadRequest, "端口号格式不正确", "port")
	}

	if portNum < 1 || portNum > 65535 {
		return core.NewFieldError(core.ErrBadRequest, "端口号必须在1-65535之间", "port")
	}

	// 检查系统保留端口
	reservedPorts := []int{22, 25, 53, 80, 110, 143, 443, 993, 995}
	for _, reserved := range reservedPorts {
		if portNum == reserved {
			return core.NewFieldError(core.ErrBadRequest, fmt.Sprintf("端口%d是系统保留端口", portNum), "port")
		}
	}

	return nil
}

// ValidateIP 验证IP地址
func ValidateIP(ip string) *core.AppError {
	if ip == "" {
		return core.NewFieldError(core.ErrBadRequest, "IP地址不能为空", "ip")
	}

	if net.ParseIP(ip) == nil {
		return core.NewFieldError(core.ErrBadRequest, "IP地址格式不正确", "ip")
	}

	return nil
}

// ValidateDomain 验证域名
func ValidateDomain(domain string) *core.AppError {
	if domain == "" {
		return core.NewFieldError(core.ErrBadRequest, "域名不能为空", "domain")
	}

	if len(domain) > 253 {
		return core.NewFieldError(core.ErrBadRequest, "域名长度不能超过253个字符", "domain")
	}

	// 基本域名格式验证
	domainRegex := regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?(\.[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?)*$`)
	if !domainRegex.MatchString(domain) {
		return core.NewFieldError(core.ErrBadRequest, "域名格式不正确", "domain")
	}

	return nil
}

// ValidateFilePath 验证文件路径
func ValidateFilePath(path string) *core.AppError {
	if path == "" {
		return core.NewFieldError(core.ErrBadRequest, "文件路径不能为空", "path")
	}

	// 检查路径遍历攻击
	if strings.Contains(path, "../") || strings.Contains(path, "..\\") {
		return core.NewFieldError(core.ErrBadRequest, "文件路径包含非法字符", "path")
	}

	// 检查危险路径
	dangerousPaths := []string{
		"/etc/passwd", "/etc/shadow", "/etc/hosts", "/etc/fstab",
		"/root/", "/home/", "/var/log/", "/proc/", "/sys/",
	}

	for _, dangerous := range dangerousPaths {
		if strings.HasPrefix(path, dangerous) {
			return core.NewFieldError(core.ErrBadRequest, "不允许访问系统敏感路径", "path")
		}
	}

	return nil
}

// ValidateServiceName 验证服务名称
func ValidateServiceName(serviceName string) *core.AppError {
	if serviceName == "" {
		return core.NewFieldError(core.ErrBadRequest, "服务名不能为空", "serviceName")
	}

	serviceRegex := regexp.MustCompile(`^[a-zA-Z0-9\-_\.]+$`)
	if !serviceRegex.MatchString(serviceName) {
		return core.NewFieldError(core.ErrBadRequest, "服务名格式不正确", "serviceName")
	}

	if len(serviceName) > 64 {
		return core.NewFieldError(core.ErrBadRequest, "服务名长度不能超过64个字符", "serviceName")
	}

	return nil
}

// ValidateWebsiteName 验证网站名称
func ValidateWebsiteName(name string) *core.AppError {
	if name == "" {
		return core.NewFieldError(core.ErrBadRequest, "网站名称不能为空", "name")
	}

	if len(name) > 64 {
		return core.NewFieldError(core.ErrBadRequest, "网站名称长度不能超过64个字符", "name")
	}

	nameRegex := regexp.MustCompile(`^[a-zA-Z0-9\-_\.]+$`)
	if !nameRegex.MatchString(name) {
		return core.NewFieldError(core.ErrBadRequest, "网站名称只能包含字母、数字、下划线、连字符和点", "name")
	}

	return nil
}

// ValidateRequired 验证必填字段
func ValidateRequired(value, fieldName string) *core.AppError {
	if strings.TrimSpace(value) == "" {
		return core.NewFieldError(core.ErrBadRequest, fmt.Sprintf("%s不能为空", fieldName), fieldName)
	}
	return nil
}

// ValidateLength 验证字符串长度
func ValidateLength(value, fieldName string, min, max int) *core.AppError {
	length := len(value)
	if length < min {
		return core.NewFieldError(core.ErrBadRequest, fmt.Sprintf("%s长度不能少于%d个字符", fieldName, min), fieldName)
	}
	if length > max {
		return core.NewFieldError(core.ErrBadRequest, fmt.Sprintf("%s长度不能超过%d个字符", fieldName, max), fieldName)
	}
	return nil
}

// SanitizeString 清理字符串，移除危险字符
func SanitizeString(input string) string {
	// 移除控制字符
	cleaned := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t' {
			return -1
		}
		return r
	}, input)

	// 移除多余的空白字符
	cleaned = strings.TrimSpace(cleaned)

	return cleaned
}

// ValidateAndSanitize 验证并清理输入
func ValidateAndSanitize(input, fieldName string, required bool, minLen, maxLen int) (string, *core.AppError) {
	cleaned := SanitizeString(input)

	if required {
		if err := ValidateRequired(cleaned, fieldName); err != nil {
			return "", err
		}
	}

	if cleaned != "" {
		if err := ValidateLength(cleaned, fieldName, minLen, maxLen); err != nil {
			return "", err
		}
	}

	return cleaned, nil
}
