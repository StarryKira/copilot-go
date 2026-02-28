package config

import (
	"fmt"
	"net/http"
	"sync"
)

const (
	CopilotVersion = "0.26.7"
	GithubClientID = "Iv1.b507a08c87ecfe98"

	CopilotChatURL        = "https://api.individual.githubcopilot.com"
	CopilotBusinessChatURL = "https://api.business.githubcopilot.com"

	GithubAPIURL      = "https://api.github.com"
	GithubCopilotURL  = "https://api.github.com/copilot_internal/v2/token"
	GithubDeviceURL   = "https://github.com/login/device/code"
	GithubTokenURL    = "https://github.com/login/oauth/access_token"
	GithubUserURL     = "https://api.github.com/user"
)

type ModelsResponse struct {
	Object string       `json:"object"`
	Data   []ModelEntry `json:"data"`
}

type ModelEntry struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

type CopilotTokenResponse struct {
	Token     string `json:"token"`
	ExpiresAt int64  `json:"expires_at"`
}

type State struct {
	mu           sync.RWMutex
	GithubToken  string
	CopilotToken string
	AccountType  string
	Models       *ModelsResponse
	VSCodeVersion string
}

func NewState() *State {
	return &State{}
}

func (s *State) Lock()    { s.mu.Lock() }
func (s *State) Unlock()  { s.mu.Unlock() }
func (s *State) RLock()   { s.mu.RLock() }
func (s *State) RUnlock() { s.mu.RUnlock() }

func CopilotBaseURL(accountType string) string {
	if accountType == "business" {
		return CopilotBusinessChatURL
	}
	return CopilotChatURL
}

func CopilotHeaders(state *State, vision bool) http.Header {
	state.RLock()
	defer state.RUnlock()

	h := make(http.Header)
	h.Set("Authorization", "Bearer "+state.CopilotToken)
	h.Set("Content-Type", "application/json")
	h.Set("Accept", "application/json")
	h.Set("Copilot-Integration-Id", "vscode-chat")
	h.Set("Editor-Version", "vscode/"+state.VSCodeVersion)
	h.Set("Editor-Plugin-Version", "copilot-chat/"+CopilotVersion)
	h.Set("User-Agent", fmt.Sprintf("GitHubCopilotChat/%s", CopilotVersion))
	h.Set("Openai-Intent", "conversation-panel")
	if vision {
		h.Set("Copilot-Vision-Enabled", "true")
		h.Set("Copilot-Vision-Request", "true")
	}
	return h
}

func GithubHeaders(state *State) http.Header {
	state.RLock()
	defer state.RUnlock()

	h := make(http.Header)
	h.Set("Authorization", "token "+state.GithubToken)
	h.Set("Accept", "application/json")
	h.Set("User-Agent", fmt.Sprintf("GitHubCopilotChat/%s", CopilotVersion))
	return h
}

func StandardHeaders() http.Header {
	h := make(http.Header)
	h.Set("Content-Type", "application/json")
	h.Set("Accept", "application/json")
	return h
}
