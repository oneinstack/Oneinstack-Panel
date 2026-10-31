package website

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

type websiteRewriteConfig struct {
	directives string
	locations  string
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

var rewriteFileCondition = regexp.MustCompile(`^\(\s*!?-[fdex]\s+\$request_filename\s*\)$`)

// renderRewriteRules accepts routing directives, not an arbitrary Nginx
// configuration. Root-location contents replace the template's default body;
// other locations must be rendered alongside it, never nested inside it.
func renderRewriteRules(value string) (websiteRewriteConfig, error) {
	tokens, err := tokenizeWebsiteRewrite(value)
	if err != nil {
		return websiteRewriteConfig{}, err
	}
	position, count := 0, 0
	nodes, err := parseWebsiteRewrite(tokens, &position, &count, "")
	if err != nil {
		return websiteRewriteConfig{}, err
	}
	var root, locations []websiteRewriteDirective
	seenLocations := make(map[string]struct{})
	for _, node := range nodes {
		if node.header[0].value != "location" {
			root = append(root, node)
			continue
		}
		key := rewriteDirectiveHeader(node.header)
		if _, exists := seenLocations[key]; exists {
			return websiteRewriteConfig{}, rewriteRuleError(node.header[0].line, "location 被重复配置")
		}
		seenLocations[key] = struct{}{}
		if len(node.header) == 2 && node.header[1].value == "/" {
			root = append(root, node.children...)
		} else {
			locations = append(locations, node)
		}
	}
	tryFiles := 0
	for _, node := range root {
		if node.header[0].value == "try_files" {
			tryFiles++
			if tryFiles > 1 {
				return websiteRewriteConfig{}, rewriteRuleError(node.header[0].line, "根 location 只能配置一条 try_files")
			}
		}
	}
	var directives strings.Builder
	if len(root) > 0 && tryFiles == 0 {
		directives.WriteString("        try_files $uri $uri/ =404;\n")
	}
	renderWebsiteRewrite(&directives, root, 2)
	var extra strings.Builder
	renderWebsiteRewrite(&extra, locations, 1)
	return websiteRewriteConfig{
		directives: strings.TrimSuffix(directives.String(), "\n"),
		locations:  strings.TrimSuffix(extra.String(), "\n"),
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

func parseWebsiteRewrite(tokens []websiteRewriteToken, position, count *int, context string) ([]websiteRewriteDirective, error) {
	var nodes []websiteRewriteDirective
	tryFiles := false
	for *position < len(tokens) {
		start := *position
		if tokens[start].value == "}" {
			if context == "" {
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
		if err := validateWebsiteRewriteDirective(node, context); err != nil {
			return nil, rewriteRuleError(tokens[start].line, err.Error())
		}
		if name == "try_files" {
			if tryFiles {
				return nil, rewriteRuleError(tokens[start].line, "同一 location 只能配置一条 try_files")
			}
			tryFiles = true
		}
		if node.block {
			var err error
			node.children, err = parseWebsiteRewrite(tokens, position, count, name)
			if err != nil {
				return nil, err
			}
		}
		nodes = append(nodes, node)
	}
	if context != "" {
		return nil, rewriteRuleError(tokens[len(tokens)-1].line, "配置块缺少右花括号")
	}
	return nodes, nil
}

func validateWebsiteRewriteDirective(node websiteRewriteDirective, context string) error {
	args := node.header
	name := args[0].value
	if node.block {
		switch name {
		case "location":
			if context != "" || len(args) < 2 || len(args) > 3 {
				return errors.New("location 只能配置在最外层，不能嵌套")
			}
			path := args[len(args)-1].value
			modifier := ""
			if len(args) == 3 {
				modifier = args[1].value
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
			return nil
		case "if":
			if context != "if" && rewriteFileCondition.MatchString(rewriteDirectiveHeader(args[1:])) {
				return nil
			}
			return errors.New("if 仅支持对 $request_filename 的文件或目录检查，且不能嵌套")
		}
	} else {
		switch name {
		case "rewrite":
			if len(args) == 3 || len(args) == 4 && (args[3].value == "last" || args[3].value == "break" || args[3].value == "redirect" || args[3].value == "permanent") {
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
				code, err := strconv.Atoi(args[1].value)
				if err == nil && code >= 100 && code <= 599 {
					return nil
				}
			}
			return errors.New("return 格式应为 return 状态码 [目标];")
		}
	}
	return fmt.Errorf("不支持指令 %q，仅支持 rewrite、try_files、return、location 和文件检查 if", name)
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
