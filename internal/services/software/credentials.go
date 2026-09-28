package software

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"oneinstack/app"
	"oneinstack/internal/models"
	"oneinstack/internal/services/componentstate"
	"oneinstack/internal/services/scriptregistry"
	"oneinstack/router/input"
	"oneinstack/router/output"
	"oneinstack/utils"

	"gorm.io/gorm"
)

var (
	ErrInstallCredentialsUnavailable = errors.New("software install credentials are not managed")
	ErrInstallCredentialsCorrupt     = errors.New("software install credentials cannot be decrypted")
)

const maxCredentialProbeBytes = 16 * 1024

type RevealedCredentialField struct {
	Key    string `json:"key"`
	Label  string `json:"label"`
	Type   string `json:"type"`
	Value  string `json:"value"`
	Secret bool   `json:"secret,omitempty"`
}

type RevealedCredentials struct {
	Component string                    `json:"component"`
	Fields    []RevealedCredentialField `json:"fields"`
}

func installedSoftwareRow(key, component string) (models.Software, error) {
	if app.DB() == nil {
		return models.Software{}, errors.New("software database is unavailable")
	}
	key = strings.TrimSpace(key)
	component = strings.TrimSpace(component)
	query := app.DB().Where("installed = ?", true)
	switch {
	case key != "" && component != "":
		query = query.Where("(`key` = ? OR component = ?)", key, component)
	case key != "":
		query = query.Where("(`key` = ? OR component = ?)", key, key)
	case component != "":
		query = query.Where("component = ?", component)
	default:
		return models.Software{}, gorm.ErrRecordNotFound
	}
	var row models.Software
	if err := query.Order("install_time DESC, id DESC").First(&row).Error; err != nil {
		return models.Software{}, err
	}
	return row, nil
}

func decodeInstallCredentials(ciphertext string) (map[string]string, error) {
	if strings.TrimSpace(ciphertext) == "" {
		return nil, ErrInstallCredentialsUnavailable
	}
	plaintext, err := utils.DecryptCredential(ciphertext, utils.CredentialPurposeSoftwareInstall)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInstallCredentialsCorrupt, err)
	}
	values := make(map[string]string)
	if err := json.Unmarshal([]byte(plaintext), &values); err != nil {
		return nil, fmt.Errorf("%w: invalid credential payload", ErrInstallCredentialsCorrupt)
	}
	result := make(map[string]string, len(values))
	for key, value := range values {
		normalized := componentstate.NormalizeParameterName(key)
		if normalized != "" && value != "" {
			result[normalized] = value
		}
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("%w: credential payload is empty", ErrInstallCredentialsCorrupt)
	}
	return result, nil
}

func parameterWasExplicit(explicit map[string]bool, key string) bool {
	target := compactInstallParameterName(key)
	for name, present := range explicit {
		if !present {
			continue
		}
		candidate := compactInstallParameterName(name)
		if candidate == target || (candidate == "port" && strings.HasSuffix(target, "port")) ||
			(candidate == "username" && strings.HasSuffix(target, "username")) ||
			(candidate == "pwd" && (target == "pwd" || strings.HasSuffix(target, "password"))) ||
			(strings.HasSuffix(candidate, "password") && strings.HasSuffix(target, "password")) {
			return true
		}
	}
	return false
}

func parameterMapContains(values map[string]string, key string) bool {
	target := compactInstallParameterName(key)
	for name, value := range values {
		if value != "" && compactInstallParameterName(name) == target {
			return true
		}
	}
	return false
}

// RestoreInstalledParameters merges the last successful install state into an
// upgrade request. Caller-supplied values always win, and version/package
// selection remains controlled by the current request and resolver.
func RestoreInstalledParameters(params *input.InstallParams, explicit map[string]bool) error {
	if params == nil || strings.TrimSpace(params.Key) == "" {
		return nil
	}
	row, err := installedSoftwareRow(params.Key, params.Key)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read installed software parameters: %w", err)
	}
	if params.Parameters == nil {
		params.Parameters = make(map[string]string)
	}
	if params.RestoredParameters == nil {
		params.RestoredParameters = make(map[string]bool)
	}
	var runtime map[string]string
	if strings.TrimSpace(row.RuntimeParamsJSON) != "" {
		if err := json.Unmarshal([]byte(row.RuntimeParamsJSON), &runtime); err != nil {
			return fmt.Errorf("read installed software runtime parameters: %w", err)
		}
	}
	for key, value := range runtime {
		normalized := componentstate.NormalizeParameterName(key)
		if normalized == "" || strings.TrimSpace(value) == "" ||
			componentstate.IsSensitiveParameter(normalized) ||
			scriptregistry.IsServerOwnedInstallParameterName(normalized) ||
			compactInstallParameterName(normalized) == "softwareversion" ||
			compactInstallParameterName(normalized) == "version" ||
			parameterWasExplicit(explicit, normalized) || parameterMapContains(params.Parameters, normalized) {
			continue
		}
		params.Parameters[normalized] = value
		params.RestoredParameters[normalized] = true
	}
	if strings.TrimSpace(row.CredentialCiphertext) == "" {
		return nil
	}
	credentials, err := decodeInstallCredentials(row.CredentialCiphertext)
	if err != nil {
		return err
	}
	for key, value := range credentials {
		if parameterWasExplicit(explicit, key) || parameterMapContains(params.Parameters, key) {
			continue
		}
		params.Parameters[key] = value
		params.RestoredParameters[key] = true
	}
	return nil
}

func credentialConfiguredForParameters(ciphertext string, parameters []*ComponentInstallParameter) bool {
	if strings.TrimSpace(ciphertext) == "" {
		return false
	}
	credentials, err := decodeInstallCredentials(ciphertext)
	if err != nil {
		for _, parameter := range parameters {
			if parameter != nil && parameter.Secret {
				parameter.CredentialConfigured = true
			}
		}
		return true
	}
	configured := false
	for _, parameter := range parameters {
		if parameter == nil || !parameter.Secret {
			continue
		}
		_, parameter.CredentialConfigured = credentials[componentstate.NormalizeParameterName(parameter.Key)]
		configured = configured || parameter.CredentialConfigured
	}
	return configured
}

func markSoftwareParamCredentialStatus(ciphertext string, parameters []*output.SoftParam) bool {
	if strings.TrimSpace(ciphertext) == "" {
		return false
	}
	credentials, err := decodeInstallCredentials(ciphertext)
	configured := false
	for _, parameter := range parameters {
		if parameter == nil || (!strings.EqualFold(strings.TrimSpace(parameter.Types), "password") &&
			!componentstate.IsSensitiveParameter(parameter.Key)) {
			continue
		}
		if err != nil {
			parameter.CredentialConfigured = true
		} else {
			_, parameter.CredentialConfigured = credentials[componentstate.NormalizeParameterName(parameter.Key)]
		}
		configured = configured || parameter.CredentialConfigured
	}
	return configured || err != nil
}

func markConfigurationCredentialStatus(configuration *ComponentConfiguration) {
	if configuration == nil {
		return
	}
	row, err := installedSoftwareRow(configuration.SoftwareKey, configuration.Component)
	if err != nil {
		return
	}
	parameters := make([]*ComponentInstallParameter, 0, len(configuration.InstallParameters))
	for index := range configuration.InstallParameters {
		parameters = append(parameters, &configuration.InstallParameters[index])
	}
	configuration.CredentialConfigured = credentialConfiguredForParameters(row.CredentialCiphertext, parameters)
}

func isCredentialIdentityParameter(key string) bool {
	compact := compactInstallParameterName(key)
	return compact == "username" || strings.HasSuffix(compact, "username") || compact == "user"
}

func installedParameterLabels(paramsJSON string) map[string]string {
	var parameters []*output.SoftParam
	if err := json.Unmarshal([]byte(paramsJSON), &parameters); err != nil {
		return nil
	}
	labels := make(map[string]string, len(parameters))
	for _, parameter := range parameters {
		if parameter == nil {
			continue
		}
		key := componentstate.NormalizeParameterName(parameter.Key)
		label := strings.TrimSpace(parameter.Value)
		if key != "" && label != "" {
			labels[key] = label
		}
	}
	return labels
}

func RevealInstalledCredentials(configuration ComponentConfiguration) (RevealedCredentials, error) {
	row, err := installedSoftwareRow(configuration.SoftwareKey, configuration.Component)
	if err != nil {
		return RevealedCredentials{}, err
	}
	credentials, err := decodeInstallCredentials(row.CredentialCiphertext)
	if err != nil {
		return RevealedCredentials{}, err
	}
	result := RevealedCredentials{Component: configuration.Component, Fields: make([]RevealedCredentialField, 0)}
	labels := installedParameterLabels(row.Params)
	seen := make(map[string]bool)
	for _, parameter := range configuration.InstallParameters {
		normalized := componentstate.NormalizeParameterName(parameter.Key)
		value := strings.TrimSpace(parameter.Value)
		if parameter.Secret {
			value = credentials[normalized]
		} else if !isCredentialIdentityParameter(parameter.Key) {
			continue
		}
		if value == "" {
			continue
		}
		label := parameter.Label
		if installedLabel := labels[normalized]; installedLabel != "" {
			label = installedLabel
		}
		result.Fields = append(result.Fields, RevealedCredentialField{
			Key: parameter.Key, Label: label, Type: parameter.Type,
			Value: value, Secret: parameter.Secret,
		})
		seen[normalized] = true
	}
	if configuration.Connection != nil && strings.TrimSpace(configuration.Connection.Username) != "" && !seen["username"] {
		result.Fields = append([]RevealedCredentialField{{
			Key: "username", Label: componentInstallParameterLabel("username"), Type: "text",
			Value: strings.TrimSpace(configuration.Connection.Username),
		}}, result.Fields...)
	}
	if len(result.Fields) == 0 {
		return RevealedCredentials{}, ErrInstallCredentialsUnavailable
	}
	return result, nil
}

// RevealManagedServiceCredentials reads credentials generated or retained by a
// signed component package. The caller must perform user reauthentication
// before invoking this method; ordinary configuration reads never use it.
func (installer *Installer) RevealManagedServiceCredentials(
	ctx context.Context,
	configuration ComponentConfiguration,
	version string,
) (RevealedCredentials, error) {
	definition, err := componentConfigurationDefinition(configuration.Component)
	if err != nil {
		return RevealedCredentials{}, err
	}
	registry, err := scriptregistry.New(app.ONE_CONFIG.ScriptCenter)
	if err != nil {
		return RevealedCredentials{}, err
	}
	componentPackage, err := registry.ResolveInstalled(
		ctx,
		definition.Component,
		strings.TrimSpace(version),
		"credentialGet",
	)
	if err != nil {
		return RevealedCredentials{}, fmt.Errorf("resolve %s credential package: %w", definition.Component, err)
	}
	scriptInfo, err := scriptInfoFromPackage(componentPackage, "credentialGet")
	if err != nil {
		return RevealedCredentials{}, err
	}
	params := installedServiceInstallParams(
		definition.SoftwareKey,
		definition.Component,
		strings.TrimSpace(version),
	)
	installer.setScriptParams(scriptInfo, params)
	output, err := installer.scriptManager.ExecuteProbe(ctx, scriptInfo, maxCredentialProbeBytes)
	if err != nil {
		return RevealedCredentials{}, fmt.Errorf("read managed %s credentials: %w", definition.Component, err)
	}
	return parseManagedCredentialOutput(output, configuration, componentPackage.Manifest.Parameters)
}

func parseManagedCredentialOutput(
	output []byte,
	configuration ComponentConfiguration,
	parameters []scriptregistry.Parameter,
) (RevealedCredentials, error) {
	if len(output) == 0 || len(output) > maxCredentialProbeBytes {
		return RevealedCredentials{}, errors.New("managed credential output size is invalid")
	}
	type credentialParameter struct {
		secret bool
	}
	allowed := make(map[string]credentialParameter)
	for _, parameter := range parameters {
		normalized := componentstate.NormalizeParameterName(parameter.Name)
		secret := parameter.Secret || strings.EqualFold(strings.TrimSpace(parameter.Type), "password")
		if normalized == "" || (!secret && !isCredentialIdentityParameter(parameter.Name)) {
			continue
		}
		allowed[normalized] = credentialParameter{secret: secret}
	}

	component := ""
	values := make(map[string]string)
	scanner := bufio.NewScanner(bytes.NewReader(output))
	scanner.Buffer(make([]byte, 1024), maxCredentialProbeBytes)
	for scanner.Scan() {
		line := scanner.Text()
		key, value, found := strings.Cut(line, "=")
		if !found || key == "" || value == "" || len(value) > 4096 || strings.ContainsAny(value, "\x00\r") {
			return RevealedCredentials{}, errors.New("managed credential output contains an invalid line")
		}
		if key == "component" {
			if component != "" {
				return RevealedCredentials{}, errors.New("managed credential output repeats component identity")
			}
			component = strings.TrimSpace(value)
			continue
		}
		if !strings.HasPrefix(key, "credential.") {
			return RevealedCredentials{}, fmt.Errorf("managed credential output contains unknown field %q", key)
		}
		normalized := componentstate.NormalizeParameterName(strings.TrimPrefix(key, "credential."))
		if _, exists := allowed[normalized]; !exists {
			return RevealedCredentials{}, fmt.Errorf("managed credential output contains undeclared field %q", key)
		}
		if _, exists := values[normalized]; exists {
			return RevealedCredentials{}, fmt.Errorf("managed credential output repeats field %q", key)
		}
		values[normalized] = value
	}
	if err := scanner.Err(); err != nil {
		return RevealedCredentials{}, fmt.Errorf("read managed credential output: %w", err)
	}
	if component == "" || component != configuration.Component {
		return RevealedCredentials{}, errors.New("managed credential output identity is invalid")
	}

	row, err := installedSoftwareRow(configuration.SoftwareKey, configuration.Component)
	if err != nil {
		return RevealedCredentials{}, err
	}
	labels := installedParameterLabels(row.Params)
	result := RevealedCredentials{Component: component, Fields: make([]RevealedCredentialField, 0, len(values))}
	hasSecret := false
	for _, parameter := range parameters {
		normalized := componentstate.NormalizeParameterName(parameter.Name)
		value, exists := values[normalized]
		if !exists {
			continue
		}
		metadata := allowed[normalized]
		label := labels[normalized]
		if label == "" {
			label = componentInstallParameterLabel(parameter.Name)
		}
		result.Fields = append(result.Fields, RevealedCredentialField{
			Key: parameter.Name, Label: label, Type: parameter.Type,
			Value: value, Secret: metadata.secret,
		})
		hasSecret = hasSecret || metadata.secret
	}
	if !hasSecret {
		return RevealedCredentials{}, ErrInstallCredentialsUnavailable
	}
	return result, nil
}
