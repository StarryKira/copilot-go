package instance

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"copilot-go/config"
	"copilot-go/copilot"
	"copilot-go/store"
)

type ProxyInstance struct {
	Account     store.Account
	State       *config.State
	Status      string // "running", "stopped", "error"
	Error       string
	stopChan    chan struct{}
}

type CopilotUser struct {
	Login     string `json:"login"`
	AvatarURL string `json:"avatar_url"`
}

func StartInstance(account store.Account) error {
	mu.Lock()
	if inst, ok := instances[account.ID]; ok {
		if inst.Status == "running" {
			mu.Unlock()
			return nil
		}
	}
	mu.Unlock()

	state := config.NewState()
	state.Lock()
	state.GithubToken = account.GithubToken
	state.AccountType = account.AccountType
	state.Unlock()

	// Get VSCode version
	vsVer := copilot.GetVSCodeVersion()
	state.Lock()
	state.VSCodeVersion = vsVer
	state.Unlock()

	// Get Copilot token
	if err := refreshCopilotToken(state); err != nil {
		mu.Lock()
		instances[account.ID] = &ProxyInstance{
			Account: account,
			State:   state,
			Status:  "error",
			Error:   err.Error(),
		}
		mu.Unlock()
		return err
	}

	// Get models
	if err := fetchModels(state); err != nil {
		log.Printf("Warning: failed to fetch models for account %s: %v", account.Name, err)
	}

	stopChan := make(chan struct{})
	inst := &ProxyInstance{
		Account:  account,
		State:    state,
		Status:   "running",
		stopChan: stopChan,
	}

	mu.Lock()
	instances[account.ID] = inst
	mu.Unlock()

	// Start background token refresh
	go tokenRefreshLoop(inst)

	log.Printf("Instance started for account: %s", account.Name)
	return nil
}

func StopInstance(accountID string) {
	mu.Lock()
	inst, ok := instances[accountID]
	if !ok {
		mu.Unlock()
		return
	}
	if inst.Status == "running" {
		close(inst.stopChan)
	}
	inst.Status = "stopped"
	mu.Unlock()
	log.Printf("Instance stopped for account: %s", inst.Account.Name)
}

func GetInstance(accountID string) *ProxyInstance {
	mu.RLock()
	defer mu.RUnlock()
	return instances[accountID]
}

func GetInstanceStatus(accountID string) string {
	mu.RLock()
	defer mu.RUnlock()
	if inst, ok := instances[accountID]; ok {
		return inst.Status
	}
	return "stopped"
}

func GetInstanceError(accountID string) string {
	mu.RLock()
	defer mu.RUnlock()
	if inst, ok := instances[accountID]; ok {
		return inst.Error
	}
	return ""
}

func GetInstanceState(accountID string) *config.State {
	mu.RLock()
	defer mu.RUnlock()
	if inst, ok := instances[accountID]; ok {
		return inst.State
	}
	return nil
}

func GetAllInstances() map[string]*ProxyInstance {
	mu.RLock()
	defer mu.RUnlock()
	result := make(map[string]*ProxyInstance)
	for k, v := range instances {
		result[k] = v
	}
	return result
}

func GetUser(accountID string) (*CopilotUser, error) {
	state := GetInstanceState(accountID)
	if state == nil {
		return nil, fmt.Errorf("instance not found")
	}

	req, err := http.NewRequest("GET", config.GithubUserURL, nil)
	if err != nil {
		return nil, err
	}
	for k, v := range config.GithubHeaders(state) {
		req.Header[k] = v
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var user CopilotUser
	if err := json.NewDecoder(resp.Body).Decode(&user); err != nil {
		return nil, err
	}
	return &user, nil
}

func tokenRefreshLoop(inst *ProxyInstance) {
	ticker := time.NewTicker(25 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-inst.stopChan:
			return
		case <-ticker.C:
			if err := refreshCopilotToken(inst.State); err != nil {
				log.Printf("Token refresh failed for %s: %v", inst.Account.Name, err)
				mu.Lock()
				inst.Status = "error"
				inst.Error = err.Error()
				mu.Unlock()
				continue
			}
			// Also refresh models
			if err := fetchModels(inst.State); err != nil {
				log.Printf("Models refresh failed for %s: %v", inst.Account.Name, err)
			}
		}
	}
}

func refreshCopilotToken(state *config.State) error {
	req, err := http.NewRequest("GET", config.GithubCopilotURL, nil)
	if err != nil {
		return err
	}
	for k, v := range config.GithubHeaders(state) {
		req.Header[k] = v
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to get copilot token: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("copilot token request failed (status %d): %s", resp.StatusCode, string(body))
	}

	var tokenResp config.CopilotTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return fmt.Errorf("failed to decode copilot token: %w", err)
	}

	state.Lock()
	state.CopilotToken = tokenResp.Token
	state.Unlock()
	return nil
}

func fetchModels(state *config.State) error {
	state.RLock()
	baseURL := config.CopilotBaseURL(state.AccountType)
	state.RUnlock()

	req, err := http.NewRequest("GET", baseURL+"/models", nil)
	if err != nil {
		return err
	}
	for k, v := range config.CopilotHeaders(state, false) {
		req.Header[k] = v
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	var models config.ModelsResponse
	if err := json.Unmarshal(body, &models); err != nil {
		// Try parsing as array
		var modelList []config.ModelEntry
		if err2 := json.Unmarshal(body, &modelList); err2 != nil {
			return fmt.Errorf("failed to parse models: %w", err)
		}
		models = config.ModelsResponse{
			Object: "list",
			Data:   modelList,
		}
	}

	if models.Object == "" {
		models.Object = "list"
	}

	state.Lock()
	state.Models = &models
	state.Unlock()
	return nil
}

func ProxyRequest(state *config.State, method, path string, body io.Reader, extraHeaders http.Header) (*http.Response, error) {
	state.RLock()
	baseURL := config.CopilotBaseURL(state.AccountType)
	state.RUnlock()

	url := baseURL + path

	req, err := http.NewRequest(method, url, body)
	if err != nil {
		return nil, err
	}

	// Check for vision content
	hasVision := false
	if body != nil {
		// We can't peek into the body here, vision is set by caller
	}

	for k, v := range config.CopilotHeaders(state, hasVision) {
		req.Header[k] = v
	}
	for k, v := range extraHeaders {
		req.Header[k] = v
	}

	client := &http.Client{Timeout: 5 * time.Minute}
	return client.Do(req)
}

func ProxyRequestWithBytes(state *config.State, method, path string, bodyBytes []byte, extraHeaders http.Header, hasVision bool) (*http.Response, error) {
	state.RLock()
	baseURL := config.CopilotBaseURL(state.AccountType)
	state.RUnlock()

	url := baseURL + path

	req, err := http.NewRequest(method, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, err
	}

	for k, v := range config.CopilotHeaders(state, hasVision) {
		req.Header[k] = v
	}
	for k, v := range extraHeaders {
		req.Header[k] = v
	}

	client := &http.Client{Timeout: 5 * time.Minute}
	return client.Do(req)
}
