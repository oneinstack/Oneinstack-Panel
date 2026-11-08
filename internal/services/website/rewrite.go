package website

import (
	"errors"
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
)

type websiteRewriteConfig struct {
	directives    string
	locations     string
	phpLocations  string
	denyLocations string
	locationLines map[string]int
}

type websiteRewriteToken struct {
	value string
	line  int
}

type websiteRewriteDirective struct {
	header   []websiteRewriteToken
	children []websiteRewriteDirective
	block    bool
}

var (
	rewriteFileCondition = regexp.MustCompile(`^\(\s*!?-[fdex]\s+\$request_filename\s*\)$`)
	rewriteHeaderName    = regexp.MustCompile(`^[A-Za-z0-9!#$%&'*+.^_|~-]+$`)
	rewriteExpiresTime   = regexp.MustCompile(`^([+-]?([0-9]+[yMwdhms]?)+|@([0-9]+[hms]?)+)$`)
)

// renderRewriteRules accepts routing and bounded location settings, not an
// arbitrary Nginx configuration. Root-location contents replace the template's
// default body; only unconditional regex denials may precede the PHP handler.
func renderRewriteRules(value string) (websiteRewriteConfig, error) {
	tokens, err := tokenizeWebsiteRewrite(value)
	if err != nil {
		return websiteRewriteConfig{}, err
	}
	position, count := 0, 0
	nodes, err := parseWebsiteRewrite(tokens, &position, &count, nil, 0)
	if err != nil {
		return websiteRewriteConfig{}, err
	}
	var root, locations, phpLocations, denials []websiteRewriteDirective
	locationLines := make(map[string]int)
	for _, node := range nodes {
		if node.header[0].value != "location" {
			root = append(root, node)
			continue
		}
		if len(node.header) == 2 && rewriteArgumentValue(node.header[1].value) == "/" {
			root = append(root, node.children...)
		} else {
			locationLines[rewriteLocationKey(node.header)] = node.header[0].line
			locations = append(locations, node)
			if isWebsiteRewriteDenyLocation(node) {
				denials = append(denials, node)
			} else {
				phpLocations = append(phpLocations, node)
			}
		}
	}
	tryFiles := 0
	seen := make(map[string]bool)
	for _, node := range root {
		name := node.header[0].value
		if rewriteSingleDirective(name) && seen[name] {
			return websiteRewriteConfig{}, rewriteRuleError(node.header[0].line, "根 location 不能重复配置 "+name)
		}
		seen[name] = true
		if node.header[0].value == "try_files" {
			tryFiles++
		}
	}
	var directives strings.Builder
	if len(root) > 0 && tryFiles == 0 {
		directives.WriteString("        try_files $uri $uri/ =404;\n")
	}
	renderWebsiteRewrite(&directives, root, 2)
	var extra strings.Builder
	renderWebsiteRewrite(&extra, locations, 1)
	var denied strings.Builder
	renderWebsiteRewrite(&denied, denials, 1)
	var afterPHP strings.Builder
	renderWebsiteRewrite(&afterPHP, phpLocations, 1)
	return websiteRewriteConfig{
		directives:    strings.TrimSuffix(directives.String(), "\n"),
		locations:     strings.TrimSuffix(extra.String(), "\n"),
		phpLocations:  strings.TrimSuffix(afterPHP.String(), "\n"),
		denyLocations: strings.TrimSuffix(denied.String(), "\n"),
		locationLines: locationLines,
	}, nil
}

func rewriteRuleError(line int, message string) error {
	return fmt.Errorf("第 %d 行伪静态规则无效：%s", line, message)
}

// Keep quoted and escaped arguments intact. Commas belong to regexes and URLs;
// only Nginx statement delimiters separate directives. Comments are discarded.
func tokenizeWebsiteRewrite(value string) ([]websiteRewriteToken, error) {
	if len(value) > 64*1024 {
		return nil, errors.New("伪静态规则不能超过 64 KB")
	}
	if strings.ContainsRune(value, 0) {
		return nil, errors.New("伪静态规则包含不支持的字符")
	}
	value = strings.ReplaceAll(strings.ReplaceAll(value, "\r\n", "\n"), "\r", "\n")
	var tokens []websiteRewriteToken
	line := 1
	for index := 0; index < len(value); {
		if strings.ContainsRune(" \t\n", rune(value[index])) {
			if value[index] == '\n' {
				line++
			}
			index++
			continue
		}
		if value[index] == '#' {
			for index < len(value) && value[index] != '\n' {
				index++
			}
			continue
		}
		// Older API clients separated complete rewrite statements with commas.
		// Accept that separator without splitting commas inside an argument.
		if value[index] == ',' && len(tokens) > 0 && tokens[len(tokens)-1].value == ";" {
			index++
			continue
		}
		start, startLine := index, line
		if strings.ContainsRune(";{}", rune(value[index])) {
			index++
		} else {
			var quote byte
			for index < len(value) {
				char := value[index]
				if char == '\\' {
					if index+1 == len(value) {
						return nil, rewriteRuleError(line, "转义字符后缺少内容")
					}
					if value[index+1] == '\n' {
						line++
					}
					index += 2
					continue
				}
				if quote != 0 {
					if char == quote {
						quote = 0
					}
				} else if char == '"' || char == '\'' {
					quote = char
				} else if char == '$' && index+1 < len(value) && value[index+1] == '{' {
					end := strings.IndexByte(value[index+2:], '}')
					if end < 0 {
						return nil, rewriteRuleError(line, "变量缺少右花括号")
					}
					variable := value[index+2 : index+2+end]
					if variable == "" || strings.IndexFunc(variable, func(char rune) bool {
						return !(char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '_')
					}) >= 0 {
						return nil, rewriteRuleError(line, "变量名称无效")
					}
					index += end + 3
					continue
				} else if strings.ContainsRune(" \t\n;{}#", rune(char)) {
					break
				}
				if char == '\n' {
					line++
				}
				index++
			}
			if quote != 0 {
				return nil, rewriteRuleError(startLine, "引号未闭合")
			}
		}
		if strings.ContainsRune(value[start:index], '`') {
			return nil, rewriteRuleError(startLine, "指令包含不支持的字符")
		}
		tokens = append(tokens, websiteRewriteToken{value: value[start:index], line: startLine})
	}
	return tokens, nil
}

func parseWebsiteRewrite(tokens []websiteRewriteToken, position, count *int, parent *websiteRewriteDirective, depth int) ([]websiteRewriteDirective, error) {
	var nodes []websiteRewriteDirective
	seen := make(map[string]bool)
	seenLocations := make(map[string]bool)
	for *position < len(tokens) {
		start := *position
		if tokens[start].value == "}" {
			if parent == nil {
				return nil, rewriteRuleError(tokens[start].line, "多余的右花括号")
			}
			*position++
			return nodes, nil
		}
		for *position < len(tokens) && !strings.Contains(";{}", tokens[*position].value) {
			*position++
		}
		if start == *position || *position == len(tokens) || tokens[*position].value == "}" {
			return nil, rewriteRuleError(tokens[start].line, "指令缺少分号或配置块，正则中的花括号请使用引号包裹")
		}
		node := websiteRewriteDirective{header: tokens[start:*position], block: tokens[*position].value == "{"}
		*position++
		*count++
		if *count > 100 {
			return nil, errors.New("伪静态规则不能超过 100 条指令")
		}
		name := node.header[0].value
		if err := validateWebsiteRewriteDirective(node, parent); err != nil {
			return nil, rewriteRuleError(tokens[start].line, err.Error())
		}
		if rewriteSingleDirective(name) && seen[name] {
			return nil, rewriteRuleError(tokens[start].line, "同一 location 不能重复配置 "+name)
		}
		seen[name] = true
		if node.block {
			if depth >= 4 {
				return nil, rewriteRuleError(tokens[start].line, "配置块最多允许嵌套 4 层")
			}
			if name == "location" {
				key := rewriteLocationKey(node.header)
				if seenLocations[key] {
					return nil, rewriteRuleError(tokens[start].line, "location 被重复配置")
				}
				seenLocations[key] = true
			}
			var err error
			node.children, err = parseWebsiteRewrite(tokens, position, count, &node, depth+1)
			if err != nil {
				return nil, err
			}
		}
		nodes = append(nodes, node)
	}
	if parent != nil {
		return nil, rewriteRuleError(tokens[len(tokens)-1].line, "配置块缺少右花括号")
	}
	return nodes, nil
}

func validateWebsiteRewriteDirective(node websiteRewriteDirective, parent *websiteRewriteDirective) error {
	args := make([]string, len(node.header))
	for index, token := range node.header {
		args[index] = rewriteArgumentValue(token.value)
	}
	context := ""
	if parent != nil {
		context = parent.header[0].value
	}
	name := node.header[0].value
	if node.block {
		switch name {
		case "location":
			if context == "if" || len(args) < 2 || len(args) > 3 {
				return errors.New("location 格式无效，且不能放在 if 内")
			}
			path := args[len(args)-1]
			modifier := ""
			if len(args) == 3 {
				modifier = args[1]
				if modifier != "=" && modifier != "^~" && modifier != "~" && modifier != "~*" {
					return errors.New("location 匹配方式无效")
				}
			}
			if modifier != "~" && modifier != "~*" && !strings.HasPrefix(path, "/") && !(modifier == "" && strings.HasPrefix(path, "@")) {
				return errors.New("location 路径必须以 / 开头，命名 location 必须以 @ 开头")
			}
			if strings.HasPrefix(path, "/.well-known/acme-challenge") || modifier == "^~" && path == "/" {
				return errors.New("location 不能覆盖证书验证路径或使用 ^~ /，根路径请使用 location /")
			}
			if context == "location" {
				parentPath := rewriteArgumentValue(parent.header[len(parent.header)-1].value)
				parentModifier := ""
				if len(parent.header) == 3 {
					parentModifier = rewriteArgumentValue(parent.header[1].value)
				}
				if parentPath == "/" || strings.HasPrefix(parentPath, "@") || parentModifier != "" && parentModifier != "^~" {
					return errors.New("嵌套 location 仅支持非根路径的普通或 ^~ 前缀父块")
				}
				if strings.HasPrefix(path, "@") || modifier != "~" && modifier != "~*" && !strings.HasPrefix(path, parentPath) {
					return errors.New("嵌套 location 必须位于父路径内，且不能使用命名 location")
				}
			}
			return nil
		case "if":
			if context != "if" && rewriteFileCondition.MatchString(rewriteDirectiveHeader(node.header[1:])) {
				return nil
			}
			return errors.New("if 仅支持对 $request_filename 的文件或目录检查，且不能嵌套")
		}
	} else {
		switch name {
		case "rewrite":
			if len(args) == 3 || len(args) == 4 && (args[3] == "last" || args[3] == "break" || args[3] == "redirect" || args[3] == "permanent") {
				return nil
			}
			return errors.New("rewrite 格式应为 rewrite 正则 目标 [last|break|redirect|permanent];")
		case "try_files":
			if context != "if" && len(args) >= 3 {
				return nil
			}
			return errors.New("try_files 至少需要检查路径和回退目标，且不能放在 if 内")
		case "return":
			if len(args) == 2 || len(args) == 3 {
				code, err := strconv.Atoi(args[1])
				if err == nil && code >= 100 && code <= 599 {
					return nil
				}
			}
			return errors.New("return 格式应为 return 状态码 [目标];")
		case "allow", "deny":
			if context == "if" || len(args) != 2 {
				return errors.New(name + " 仅支持一个 IP、CIDR、unix: 或 all 参数，且不能放在 if 内")
			}
			if args[1] == "all" || args[1] == "unix:" || net.ParseIP(args[1]) != nil {
				return nil
			}
			if _, _, err := net.ParseCIDR(args[1]); err == nil {
				return nil
			}
			return errors.New(name + " 参数必须是有效的 IP、CIDR、unix: 或 all")
		case "expires":
			if len(args) == 2 && (args[1] == "off" || args[1] == "epoch" || args[1] == "max" || rewriteExpiresTime.MatchString(args[1])) {
				return nil
			}
			if len(args) == 3 && args[1] == "modified" && !strings.HasPrefix(args[2], "@") && rewriteExpiresTime.MatchString(args[2]) {
				return nil
			}
			return errors.New("expires 格式应为 expires [modified] 时间; 或 expires off|epoch|max;")
		case "add_header":
			if (len(args) == 3 || len(args) == 4 && args[3] == "always") && rewriteHeaderName.MatchString(args[1]) &&
				strings.IndexFunc(args[2], func(char rune) bool { return char < 32 || char == 127 }) < 0 {
				return nil
			}
			return errors.New("add_header 格式应为 add_header 响应头名称 值 [always];，名称必须有效且值不能包含控制字符")
		case "access_log":
			if len(args) == 2 && args[1] == "off" {
				return nil
			}
			return errors.New("伪静态仅支持 access_log off;，日志路径由网站日志设置管理")
		case "etag", "log_not_found":
			if context != "if" && len(args) == 2 && (args[1] == "on" || args[1] == "off") {
				return nil
			}
			return errors.New(name + " 仅支持 on 或 off，且不能放在 if 内")
		case "break":
			if len(args) == 1 {
				return nil
			}
			return errors.New("break 不接受参数，请使用 break;")
		}
	}
	return fmt.Errorf("不支持指令 %q，支持 rewrite、try_files、return、break、location、文件检查 if、allow、deny、expires、add_header、access_log off、etag 和 log_not_found", name)
}

// Decode Nginx quoting for validation and conflict keys, while retaining the
// original token for rendering. Unknown escapes (notably regex \.) stay intact.
func rewriteArgumentValue(value string) string {
	var result strings.Builder
	var quote byte
	for index := 0; index < len(value); index++ {
		char := value[index]
		if char == '\\' && index+1 < len(value) {
			index++
			switch value[index] {
			case 't':
				result.WriteByte('\t')
			case 'r':
				result.WriteByte('\r')
			case 'n':
				result.WriteByte('\n')
			case '\\', '\'', '"':
				result.WriteByte(value[index])
			default:
				result.WriteByte('\\')
				result.WriteByte(value[index])
			}
			continue
		}
		if char == '\'' || char == '"' {
			if quote == 0 {
				quote = char
				continue
			}
			if quote == char {
				quote = 0
				continue
			}
		}
		result.WriteByte(char)
	}
	return result.String()
}

func rewriteLocationKey(header []websiteRewriteToken) string {
	modifier := ""
	if len(header) == 3 {
		modifier = rewriteArgumentValue(header[1].value)
	}
	if modifier == "^~" {
		modifier = ""
	}
	return modifier + " " + rewriteArgumentValue(header[len(header)-1].value)
}

func rewriteSingleDirective(name string) bool {
	switch name {
	case "try_files", "expires", "access_log", "etag", "log_not_found":
		return true
	}
	return false
}

// Only a regex location that cannot serve file contents is moved ahead of PHP.
// Moving general static regexes ahead of PHP could expose PHP source files.
func isWebsiteRewriteDenyLocation(node websiteRewriteDirective) bool {
	if len(node.header) != 3 {
		return false
	}
	modifier := rewriteArgumentValue(node.header[1].value)
	if modifier != "~" && modifier != "~*" {
		return false
	}
	denied := false
	for _, child := range node.children {
		if child.block {
			return false
		}
		switch child.header[0].value {
		case "allow", "rewrite", "try_files", "break":
			return false
		case "deny":
			denied = denied || rewriteArgumentValue(child.header[1].value) == "all"
		case "return":
			code := rewriteArgumentValue(child.header[1].value)
			if code != "403" && code != "404" && code != "410" && code != "444" {
				return false
			}
			denied = true
		}
	}
	return denied
}

func validateWebsiteRewriteLocationConflicts(rewrite websiteRewriteConfig, managed string) error {
	if len(rewrite.locationLines) == 0 {
		return nil
	}
	tokens, err := tokenizeWebsiteRewrite(managed)
	if err != nil {
		return err
	}
	depth := 0
	for index, token := range tokens {
		switch token.value {
		case "{":
			depth++
		case "}":
			depth--
		case "location":
			if depth != 0 {
				continue
			}
			end := index + 1
			for end < len(tokens) && tokens[end].value != "{" {
				end++
			}
			if end-index < 2 || end-index > 3 {
				continue
			}
			if line, conflict := rewrite.locationLines[rewriteLocationKey(tokens[index:end])]; conflict {
				return rewriteRuleError(line, "location 与网站的目录绑定、重定向、反向代理或禁止访问路径配置冲突")
			}
		}
	}
	return nil
}

func rewriteDirectiveHeader(tokens []websiteRewriteToken) string {
	values := make([]string, len(tokens))
	for index, token := range tokens {
		values[index] = token.value
	}
	return strings.Join(values, " ")
}

func renderWebsiteRewrite(result *strings.Builder, nodes []websiteRewriteDirective, depth int) {
	indent := strings.Repeat("    ", depth)
	for _, node := range nodes {
		result.WriteString(indent + rewriteDirectiveHeader(node.header))
		if node.block {
			result.WriteString(" {\n")
			renderWebsiteRewrite(result, node.children, depth+1)
			result.WriteString(indent + "}\n")
		} else {
			result.WriteString(";\n")
		}
	}
}
