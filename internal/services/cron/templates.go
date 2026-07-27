package cron

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"oneinstack/app"
)

const (
	TaskTypeShell    = "shell"
	TaskTypeTemplate = "template"
)

type TemplateParameter struct {
	Name        string   `json:"name"`
	Label       string   `json:"label"`
	Type        string   `json:"type"`
	Required    bool     `json:"required"`
	Description string   `json:"description,omitempty"`
	Options     []string `json:"options,omitempty"`
	Placeholder string   `json:"placeholder,omitempty"`
}

type TemplateDefinition struct {
	ID          string              `json:"id"`
	Name        string              `json:"name"`
	Description string              `json:"description"`
	Parameters  []TemplateParameter `json:"parameters"`
}

type templateSpec struct {
	definition TemplateDefinition
	validate   func(map[string]string) (map[string]string, error)
	command    func(map[string]string) (string, []string, error)
}

var safeNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

var supportedServices = []string{
	"nginx", "mysql", "mysqld", "redis", "redis-server",
	"php8.1-fpm", "php8.2-fpm", "php8.3-fpm",
}

var templateRegistry = map[string]templateSpec{
	"disk-usage-report": {
		definition: TemplateDefinition{
			ID: "disk-usage-report", Name: "磁盘空间报告",
			Description: "使用 df 输出所有挂载点的容量和使用率，不执行 Shell。",
		},
		validate: noTemplateParameters,
		command: func(map[string]string) (string, []string, error) {
			executable, err := resolveExecutable("/bin/df", "/usr/bin/df")
			return executable, []string{"-P", "-h"}, err
		},
	},
	"service-status": {
		definition: TemplateDefinition{
			ID: "service-status", Name: "服务状态检查",
			Description: "检查受支持的 OneinStack 服务是否处于 active 状态。",
			Parameters: []TemplateParameter{{
				Name: "service", Label: "服务", Type: "select", Required: true,
				Options: supportedServices,
			}},
		},
		validate: validateServiceParameters,
		command: func(parameters map[string]string) (string, []string, error) {
			executable, err := resolveExecutable("/bin/systemctl", "/usr/bin/systemctl")
			return executable, []string{"is-active", "--", parameters["service"]}, err
		},
	},
	"website-directory-size": {
		definition: TemplateDefinition{
			ID: "website-directory-size", Name: "网站目录容量报告",
			Description: "统计受管网站根目录下单个站点的占用空间，不接受任意路径。",
			Parameters: []TemplateParameter{{
				Name: "site", Label: "站点目录名", Type: "text", Required: true,
				Description: "只能填写网站根目录下的单级目录名。",
				Placeholder: "example.com",
			}},
		},
		validate: validateWebsiteParameters,
		command: func(parameters map[string]string) (string, []string, error) {
			executable, err := resolveExecutable("/usr/bin/du", "/bin/du")
			if err != nil {
				return "", nil, err
			}
			root := filepath.Clean(app.ONE_CONFIG.System.WebPath)
			target := filepath.Join(root, parameters["site"])
			if filepath.Dir(target) != root {
				return "", nil, errors.New("website directory escaped the managed root")
			}
			return executable, []string{"-s", "-h", "--", target}, nil
		},
	},
}

func Templates() []TemplateDefinition {
	ids := make([]string, 0, len(templateRegistry))
	for id := range templateRegistry {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	result := make([]TemplateDefinition, 0, len(ids))
	for _, id := range ids {
		definition := templateRegistry[id].definition
		definition.Parameters = append([]TemplateParameter(nil), definition.Parameters...)
		result = append(result, definition)
	}
	return result
}

func normalizeTemplate(
	templateID string,
	parameters map[string]string,
) (string, map[string]string, error) {
	templateID = strings.ToLower(strings.TrimSpace(templateID))
	spec, exists := templateRegistry[templateID]
	if !exists {
		return "", nil, errors.New("unsupported task template")
	}
	normalized, err := spec.validate(parameters)
	if err != nil {
		return "", nil, err
	}
	return templateID, normalized, nil
}

func templateCommand(
	templateID string,
	parameters map[string]string,
) (string, []string, error) {
	spec, exists := templateRegistry[templateID]
	if !exists {
		return "", nil, errors.New("unsupported task template")
	}
	return spec.command(parameters)
}

func noTemplateParameters(parameters map[string]string) (map[string]string, error) {
	if len(parameters) > 0 {
		return nil, errors.New("this template does not accept parameters")
	}
	return map[string]string{}, nil
}

func validateServiceParameters(parameters map[string]string) (map[string]string, error) {
	if err := requireOnlyParameters(parameters, "service"); err != nil {
		return nil, err
	}
	service := strings.ToLower(strings.TrimSpace(parameters["service"]))
	for _, allowed := range supportedServices {
		if service == allowed {
			return map[string]string{"service": service}, nil
		}
	}
	return nil, errors.New("unsupported service name")
}

func validateWebsiteParameters(parameters map[string]string) (map[string]string, error) {
	if err := requireOnlyParameters(parameters, "site"); err != nil {
		return nil, err
	}
	site := strings.TrimSpace(parameters["site"])
	if !safeNamePattern.MatchString(site) || site == "." || site == ".." {
		return nil, errors.New("site must be a single safe directory name")
	}
	return map[string]string{"site": site}, nil
}

func requireOnlyParameters(parameters map[string]string, names ...string) error {
	allowed := make(map[string]struct{}, len(names))
	for _, name := range names {
		allowed[name] = struct{}{}
		if strings.TrimSpace(parameters[name]) == "" {
			return fmt.Errorf("template parameter %s is required", name)
		}
	}
	for name := range parameters {
		if _, exists := allowed[name]; !exists {
			return fmt.Errorf("unknown template parameter %s", name)
		}
	}
	return nil
}

func resolveExecutable(candidates ...string) (string, error) {
	for _, candidate := range candidates {
		info, err := os.Stat(candidate)
		if err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0111 != 0 {
			return candidate, nil
		}
	}
	return "", errors.New("required template executable is not installed")
}
