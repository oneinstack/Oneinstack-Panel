package core

// TruncateString 截断字符串到指定最大长度
func TruncateString(s string, maxLen int) string {
	if maxLen <= 0 {
		return ""
	}

	// 将字符串转换为rune数组以正确处理unicode
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen])
}
