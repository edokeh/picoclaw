// +build ignore

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"
)

const dingtalkAPIBase = "https://oapi.dingtalk.com"

type DingTalkAccessTokenResponse struct {
	ErrCode     int    `json:"errcode"`
	ErrMsg      string `json:"errmsg"`
	AccessToken string `json:"access_token"`
	ExpiresIn   int    `json:"expires_in"`
}

type DingTalkMediaUploadResponse struct {
	ErrCode int    `json:"errcode"`
	ErrMsg  string `json:"errmsg"`
	MediaID string `json:"media_id"`
}

func main() {
	clientID := "dinge79j84ynidfo9jyq"
	clientSecret := "yH3CXNmJv4fmzNxrkAczgceJqJkVCZvj1zfyneZRfToqysdqPNl0Kf_USxx4N5r1"

	// 检查参数
	if len(os.Args) < 3 {
		fmt.Println("用法: go run test_dingtalk_image.go <sessionWebhook> <图片路径>")
		fmt.Println("")
		fmt.Println("步骤:")
		fmt.Println("1. 在钉钉上给你的机器人发一条消息")
		fmt.Println("2. 从 picoclaw 日志中找到 sessionWebhook (类似 https://oapi.dingtalk.com/robot/send?session=xxx)")
		fmt.Println("3. 运行此脚本: go run test_dingtalk_image.go \"<sessionWebhook>\" <图片路径>")
		fmt.Println("")
		fmt.Println("示例: go run test_dingtalk_image.go \"https://oapi.dingtalk.com/robot/send?session=xxxx\" /tmp/test.png")
		os.Exit(1)
	}

	sessionWebhook := os.Args[1]
	imagePath := os.Args[2]

	// 验证图片文件存在
	if _, err := os.Stat(imagePath); os.IsNotExist(err) {
		fmt.Printf("❌ 图片文件不存在: %s\n", imagePath)
		os.Exit(1)
	}

	fmt.Printf("📷 图片路径: %s\n", imagePath)
	fmt.Printf("🔗 Session Webhook: %s...\n", sessionWebhook[:50])

	// 1. 获取 access_token
	fmt.Println("\n步骤 1: 获取 access_token...")
	accessToken, err := getAccessToken(clientID, clientSecret)
	if err != nil {
		fmt.Printf("❌ 获取 access_token 失败: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("✅ access_token: %s...\n", accessToken[:20])

	// 2. 上传图片
	fmt.Println("\n步骤 2: 上传图片...")
	mediaID, err := uploadMedia(accessToken, imagePath)
	if err != nil {
		fmt.Printf("❌ 上传图片失败: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("✅ media_id: %s\n", mediaID)

	// 3. 发送图片消息
	fmt.Println("\n步骤 3: 发送图片消息...")
	if err := sendImageMessage(sessionWebhook, mediaID); err != nil {
		fmt.Printf("❌ 发送图片失败: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("\n✅ 图片发送成功！请检查钉钉消息。")
}

func getAccessToken(clientID, clientSecret string) (string, error) {
	apiURL := fmt.Sprintf("%s/gettoken?appkey=%s&appsecret=%s",
		dingtalkAPIBase,
		url.QueryEscape(clientID),
		url.QueryEscape(clientSecret))

	httpClient := &http.Client{Timeout: 10 * time.Second}
	resp, err := httpClient.Get(apiURL)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	var tokenResp DingTalkAccessTokenResponse
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return "", err
	}

	if tokenResp.ErrCode != 0 {
		return "", fmt.Errorf("%s (code: %d)", tokenResp.ErrMsg, tokenResp.ErrCode)
	}

	return tokenResp.AccessToken, nil
}

func uploadMedia(accessToken, filePath string) (string, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer file.Close()

	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)

	part, err := writer.CreateFormFile("media", filepath.Base(filePath))
	if err != nil {
		return "", err
	}

	if _, err := io.Copy(part, file); err != nil {
		return "", err
	}

	if err := writer.Close(); err != nil {
		return "", err
	}

	apiURL := fmt.Sprintf("%s/media/upload?access_token=%s&type=image",
		dingtalkAPIBase, accessToken)

	req, err := http.NewRequest(http.MethodPost, apiURL, &buf)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())

	httpClient := &http.Client{Timeout: 30 * time.Second}
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	var uploadResp DingTalkMediaUploadResponse
	if err := json.Unmarshal(body, &uploadResp); err != nil {
		return "", err
	}

	if uploadResp.ErrCode != 0 {
		return "", fmt.Errorf("%s (code: %d)", uploadResp.ErrMsg, uploadResp.ErrCode)
	}

	return uploadResp.MediaID, nil
}

func sendImageMessage(sessionWebhook, mediaID string) error {
	imgMsg := map[string]interface{}{
		"msgtype": "image",
		"image": map[string]interface{}{
			"media_id": mediaID,
		},
	}

	msgBytes, err := json.Marshal(imgMsg)
	if err != nil {
		return err
	}

	req, err := http.NewRequest(http.MethodPost, sessionWebhook, bytes.NewReader(msgBytes))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	httpClient := &http.Client{Timeout: 10 * time.Second}
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	fmt.Printf("   响应: %s\n", string(body))

	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		return err
	}

	if errcode, ok := result["errcode"].(float64); ok && errcode != 0 {
		return fmt.Errorf("%v", result["errmsg"])
	}

	return nil
}