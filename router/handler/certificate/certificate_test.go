package certificate

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestListAlgorithmsResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/certificates/algorithms", ListAlgorithms)
	request := httptest.NewRequest("GET", "/certificates/algorithms", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != 200 {
		t.Fatalf("status = %d, body=%s", response.Code, response.Body.String())
	}
	var body struct {
		Data []struct {
			Value string `json:"value"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Data) != 5 || body.Data[0].Value != "ec-256" {
		t.Fatalf("unexpected response: %s", response.Body.String())
	}
}
