package pkg

import (
	"fmt"
	"oneinstack/app"
	"oneinstack/internal/models"

	"github.com/imroc/req/v3"
)

var BaseUrl = app.ONE_CONFIG.System.Remote

func PostServer(path string, body interface{}, result interface{}) (interface{}, error) {
	client := req.C()
	if BaseUrl == "" {
		BaseUrl = "http://localhost:8189/v1" + path
	}
	r := client.R()
	if body != nil {
		r.SetBody(body)
	}
	r.SetSuccessResult(result)
	resp, err := r.Post(BaseUrl + path)
	if err != nil || resp.Response.StatusCode != 200 {
		return nil, fmt.Errorf("请求失败: %v", err)
	}
	return result, nil
}

func SyncSoftware() ([]*models.Softwaren, error) {
	type Response struct {
		Code    int                 `json:"code"`
		Message string              `json:"message"`
		Data    []*models.Softwaren `json:"data"`
	}
	result := &Response{}
	_, err := PostServer("/sys/update", nil, result)
	if err != nil {
		return nil, err
	}
	if result.Code != 0 {
		return nil, fmt.Errorf(result.Message)
	}
	return result.Data, nil
}

func NoticeInstallSoftware(soft *models.Softwaren) error {
	type Response struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
	result := &Response{}
	_, err := PostServer("/sys/install", soft, result)
	if err != nil {
		return err
	}
	if result.Code != 0 {
		return fmt.Errorf(result.Message)
	}
	return nil
}
