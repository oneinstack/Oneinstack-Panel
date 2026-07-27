package utils

import (
	cryptorand "crypto/rand"
	"fmt"
	"math/big"
	mathrand "math/rand"
)

// GenerateRandomString 根据给定的最小长度和最大长度生成一个包含大小写字母、数字和特殊字符的随机字符串
func GenerateRandomString(minLen, maxLen int) string {
	// 定义字符集，包含大小写字母、数字和特殊字符
	charset := "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789!@#$%^&*"
	// 设置随机长度
	length := minLen
	if maxLen > minLen {
		length = minLen + mathrand.Intn(maxLen-minLen+1)
	}

	// 构造随机字符串
	b := make([]byte, length)
	for i := range b {
		b[i] = charset[mathrand.Intn(len(charset))]
	}
	return string(b)
}

// GenerateSecurePassword creates a cryptographically secure password using
// characters accepted by the managed MySQL and Redis component scripts.
func GenerateSecurePassword(length int) (string, error) {
	if length < 12 || length > 128 {
		return "", fmt.Errorf("password length must be between 12 and 128 characters")
	}
	const alphabet = "abcdefghijkmnopqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789_@%+=!#?-"
	result := make([]byte, length)
	limit := big.NewInt(int64(len(alphabet)))
	for i := range result {
		index, err := cryptorand.Int(cryptorand.Reader, limit)
		if err != nil {
			return "", fmt.Errorf("generate secure password: %w", err)
		}
		result[i] = alphabet[index.Int64()]
	}
	return string(result), nil
}
