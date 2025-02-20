package utils

import "math/rand"

// GenerateRandomString 根据给定的最小长度和最大长度生成一个包含大小写字母的随机字符串
func GenerateRandomString(minLen, maxLen int) string {
	// 定义字符集，只包含大小写字母
	charset := "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"
	// 设置随机长度
	length := minLen
	if maxLen > minLen {
		length = minLen + rand.Intn(maxLen-minLen+1)
	}

	// 构造随机字符串
	b := make([]byte, length)
	for i := range b {
		b[i] = charset[rand.Intn(len(charset))]
	}
	return string(b)
}
