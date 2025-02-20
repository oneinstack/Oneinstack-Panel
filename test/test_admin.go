package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"net/http/cookiejar"
	"net/url"
)

type LoginRequest struct {
	Username      string `json:""`
	Password      string `json:"password"`
	PhpMyAdminURL string `json:"phpmyadmin_url"`
}

type LoginResponse struct {
	Status  string `json:"status"`
	Message string `json:"message"`
	Cookie  string `json:"cookie"`
}

func loginPhpMyAdmin(username, password, phpMyAdminURL string) (string, error) {
	// 创建 CookieJar，用于维护会话
	jar, err := cookiejar.New(nil)
	if err != nil {
		return "", fmt.Errorf("failed to create cookie jar: %v", err)
	}
	client := &http.Client{Jar: jar}

	// 构造 POST 请求
	data := url.Values{}
	data.Set("pma_username", username)
	data.Set("pma_password", password)

	loginURL := fmt.Sprintf("%s/index.php", phpMyAdminURL)
	req, err := http.NewRequest("POST", loginURL, bytes.NewBufferString(data.Encode()))
	if err != nil {
		return "", fmt.Errorf("failed to create login request: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	// 发送请求
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to send login request: %v", err)
	}
	defer resp.Body.Close()

	// 检查响应状态
	if resp.StatusCode != http.StatusOK {
		body, _ := ioutil.ReadAll(resp.Body)
		return "", fmt.Errorf("login failed: %s", string(body))
	}

	// 提取登录后的 Cookie
	cookies := jar.Cookies(req.URL)
	var cookieString string
	for _, cookie := range cookies {
		cookieString += cookie.Name + "=" + cookie.Value + "; "
	}

	return cookieString, nil
}

func loginHandler(w http.ResponseWriter, r *http.Request) {
	// 解析请求体
	var loginReq LoginRequest
	if err := json.NewDecoder(r.Body).Decode(&loginReq); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	// 调用登录函数
	cookie, err := loginPhpMyAdmin(loginReq.Username, loginReq.Password, loginReq.PhpMyAdminURL)
	if err != nil {
		log.Printf("Login error: %v", err)
		http.Error(w, fmt.Sprintf("Login failed: %v", err), http.StatusUnauthorized)
		return
	}

	// 返回登录结果和 Cookie
	loginResp := LoginResponse{
		Status:  "success",
		Message: "Login successful",
		Cookie:  cookie,
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(loginResp)
}

func main() {
	http.HandleFunc("/v1/login", loginHandler)
	log.Println("Server running at http://localhost:8081")
	log.Fatal(http.ListenAndServe(":8081", nil))
}
