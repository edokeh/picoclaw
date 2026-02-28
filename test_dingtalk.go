// +build ignore

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

const dingtalkAPIBase = "https://oapi.dingtalk.com"

type DingTalkAccessTokenResponse struct {
	ErrCode     int    `json:"errcode"`
	ErrMsg      string `json:"errmsg"`
	AccessToken string `json:"access_token"`
	ExpiresIn   int    `json:"expires_in"`
}

func main() {
	// 从环境变量或命令行参数获取
	clientID := "dinge79j84ynidfo9jyq"
	clientSecret := "yH3CXNmJv4fmzNxrkAczgceJqJkVCZvj1zfyneZRfToqysdqPNl0Kf_USxx4N5r1"

	if clientID == "" || clientSecret == "" {
		fmt.Println("请在代码中填入 client_id 和 client_secret")
		return
	}

	// 测试获取 access_token
	apiURL := fmt.Sprintf("%s/gettoken?appkey=%s&appsecret=%s",
		dingtalkAPIBase,
		url.QueryEscape(clientID),
		url.QueryEscape(clientSecret))

	httpClient := &http.Client{Timeout: 10 * time.Second}
	resp, err := httpClient.Get(apiURL)
	if err != nil {
		fmt.Printf("请求失败: %v\n", err)
		return
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		fmt.Printf("读取响应失败: %v\n", err)
		return
	}

	fmt.Printf("原始响应: %s\n", string(body))

	var tokenResp DingTalkAccessTokenResponse
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		fmt.Printf("解析响应失败: %v\n", err)
		return
	}

	if tokenResp.ErrCode != 0 {
		fmt.Printf("API 错误: %s (code: %d)\n", tokenResp.ErrMsg, tokenResp.ErrCode)
		return
	}

	fmt.Printf("\n✅ 成功获取 access_token!\n")
	fmt.Printf("   Token: %s...\n", tokenResp.AccessToken[:50])
	fmt.Printf("   过期时间: %d 秒\n", tokenResp.ExpiresIn)
}