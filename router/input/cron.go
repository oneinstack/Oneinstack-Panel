package input

import "encoding/json"

type CronParam struct {
	Page
	ID      int    `json:"id"`
	Name    string `json:"name"`
	Status  string `json:"status"`
	StartAt string `json:"start_at"`
	EndAt   string `json:"end_at"`
}

type AddCronParam struct {
	ID                 uint              `json:"id"`
	Name               string            `json:"name"`
	Command            string            `json:"command"`
	TaskType           string            `json:"task_type"`
	TemplateID         string            `json:"template_id"`
	TemplateParams     map[string]string `json:"template_params"`
	ConfirmUnsafeShell bool              `json:"confirm_unsafe_shell"`
	Schedule           []string          `json:"schedule"`
	Description        string            `json:"description"`
	Enabled            bool              `json:"enabled"`
	NotifyOnFailure    bool              `json:"notify_on_failure"`
	TimeoutSeconds     int               `json:"timeout_seconds"`
	ConcurrencyPolicy  string            `json:"concurrency_policy"`
}

func (p *AddCronParam) UnmarshalJSON(data []byte) error {
	type base AddCronParam
	type payload struct {
		base
		TaskTypeSnake           *string           `json:"task_type"`
		TaskTypeCamel           *string           `json:"taskType"`
		TemplateIDSnake         *string           `json:"template_id"`
		TemplateIDCamel         *string           `json:"templateId"`
		TemplateParamsSnake     map[string]string `json:"template_params"`
		TemplateParamsCamel     map[string]string `json:"templateParams"`
		ConfirmUnsafeShellCamel *bool             `json:"confirmUnsafeShell"`
		NotifyOnFailureCamel    *bool             `json:"notifyOnFailure"`
		TimeoutSecondsCamel     *int              `json:"timeoutSeconds"`
		ConcurrencyPolicyCamel  *string           `json:"concurrencyPolicy"`
	}
	var raw payload
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*p = AddCronParam(raw.base)
	if raw.TaskTypeSnake != nil {
		p.TaskType = *raw.TaskTypeSnake
	} else if raw.TaskTypeCamel != nil {
		p.TaskType = *raw.TaskTypeCamel
	}
	if raw.TemplateIDSnake != nil {
		p.TemplateID = *raw.TemplateIDSnake
	} else if raw.TemplateIDCamel != nil {
		p.TemplateID = *raw.TemplateIDCamel
	}
	if raw.TemplateParamsSnake != nil {
		p.TemplateParams = raw.TemplateParamsSnake
	} else if raw.TemplateParamsCamel != nil {
		p.TemplateParams = raw.TemplateParamsCamel
	}
	if raw.ConfirmUnsafeShellCamel != nil {
		p.ConfirmUnsafeShell = *raw.ConfirmUnsafeShellCamel
	}
	if raw.NotifyOnFailureCamel != nil {
		p.NotifyOnFailure = *raw.NotifyOnFailureCamel
	}
	if raw.TimeoutSecondsCamel != nil {
		p.TimeoutSeconds = *raw.TimeoutSecondsCamel
	}
	if raw.ConcurrencyPolicyCamel != nil {
		p.ConcurrencyPolicy = *raw.ConcurrencyPolicyCamel
	}
	return nil
}

type CronIDs struct {
	IDs []int `json:"ids"`
}

type RunCronParam struct {
	ID uint `json:"id" binding:"required"`
}
