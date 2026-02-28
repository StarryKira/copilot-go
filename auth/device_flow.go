package auth

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"copilot-go/config"

	"github.com/google/uuid"
)

type AuthSession struct {
	ID              string `json:"id"`
	DeviceCode      string `json:"deviceCode"`
	UserCode        string `json:"userCode"`
	VerificationURI string `json:"verificationUri"`
	ExpiresAt       time.Time `json:"expiresAt"`
	Interval        int    `json:"interval"`
	Status          string `json:"status"` // "pending", "complete", "expired", "error"
	AccessToken     string `json:"accessToken,omitempty"`
	Error           string `json:"error,omitempty"`
}

var (
	authSessions sync.Map
)

type deviceCodeResponse struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

type tokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	Error       string `json:"error"`
}

func StartDeviceFlow() (*AuthSession, error) {
	body, _ := json.Marshal(map[string]string{
		"client_id": config.GithubClientID,
		"scope":     "read:user",
	})

	req, err := http.NewRequest("POST", config.GithubDeviceURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var dcResp deviceCodeResponse
	if err := json.Unmarshal(respBody, &dcResp); err != nil {
		return nil, fmt.Errorf("failed to parse device code response: %w", err)
	}

	session := &AuthSession{
		ID:              uuid.New().String(),
		DeviceCode:      dcResp.DeviceCode,
		UserCode:        dcResp.UserCode,
		VerificationURI: dcResp.VerificationURI,
		ExpiresAt:       time.Now().Add(time.Duration(dcResp.ExpiresIn) * time.Second),
		Interval:        dcResp.Interval,
		Status:          "pending",
	}

	if session.Interval < 5 {
		session.Interval = 5
	}

	authSessions.Store(session.ID, session)

	go pollForToken(session)

	return session, nil
}

func pollForToken(session *AuthSession) {
	ticker := time.NewTicker(time.Duration(session.Interval) * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if time.Now().After(session.ExpiresAt) {
				session.Status = "expired"
				session.Error = "device code expired"
				authSessions.Store(session.ID, session)
				return
			}

			token, err := requestToken(session.DeviceCode)
			if err != nil {
				continue // slow_down or authorization_pending
			}

			if token != "" {
				session.Status = "complete"
				session.AccessToken = token
				authSessions.Store(session.ID, session)
				return
			}
		}
	}
}

func requestToken(deviceCode string) (string, error) {
	body, _ := json.Marshal(map[string]string{
		"client_id":   config.GithubClientID,
		"device_code": deviceCode,
		"grant_type":  "urn:ietf:params:oauth:grant-type:device_code",
	})

	req, err := http.NewRequest("POST", config.GithubTokenURL, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	var tokenResp tokenResponse
	if err := json.Unmarshal(respBody, &tokenResp); err != nil {
		return "", err
	}

	if tokenResp.Error != "" {
		return "", fmt.Errorf("%s", tokenResp.Error)
	}

	return tokenResp.AccessToken, nil
}

func GetSession(sessionID string) *AuthSession {
	v, ok := authSessions.Load(sessionID)
	if !ok {
		return nil
	}
	return v.(*AuthSession)
}

func CleanupSession(sessionID string) {
	authSessions.Delete(sessionID)
}
