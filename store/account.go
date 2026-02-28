package store

import (
	"encoding/json"
	"os"
	"sync"
	"time"

	"github.com/google/uuid"
)

type Account struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	GithubToken string `json:"githubToken"`
	AccountType string `json:"accountType"`
	ApiKey      string `json:"apiKey"`
	Enabled     bool   `json:"enabled"`
	CreatedAt   string `json:"createdAt"`
	Priority    int    `json:"priority"`
}

type PoolConfig struct {
	Enabled  bool   `json:"enabled"`
	Strategy string `json:"strategy"`
	ApiKey   string `json:"apiKey"`
}

type accountStore struct {
	Accounts []Account `json:"accounts"`
}

var (
	accountMu sync.RWMutex
	poolMu    sync.RWMutex
)

func readAccounts() ([]Account, error) {
	data, err := os.ReadFile(AccountsFile())
	if err != nil {
		return nil, err
	}
	if len(data) == 0 || string(data) == "{}" {
		return []Account{}, nil
	}
	var s accountStore
	if err := json.Unmarshal(data, &s); err != nil {
		// Try as array directly
		var accounts []Account
		if err2 := json.Unmarshal(data, &accounts); err2 != nil {
			return []Account{}, nil
		}
		return accounts, nil
	}
	return s.Accounts, nil
}

func writeAccounts(accounts []Account) error {
	s := accountStore{Accounts: accounts}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(AccountsFile(), data, 0644)
}

func GetAccounts() ([]Account, error) {
	accountMu.RLock()
	defer accountMu.RUnlock()
	return readAccounts()
}

func GetAccount(id string) (*Account, error) {
	accounts, err := GetAccounts()
	if err != nil {
		return nil, err
	}
	for _, a := range accounts {
		if a.ID == id {
			return &a, nil
		}
	}
	return nil, nil
}

func GetAccountByApiKey(apiKey string) (*Account, error) {
	accounts, err := GetAccounts()
	if err != nil {
		return nil, err
	}
	for _, a := range accounts {
		if a.ApiKey == apiKey {
			return &a, nil
		}
	}
	return nil, nil
}

func GetEnabledAccounts() ([]Account, error) {
	accounts, err := GetAccounts()
	if err != nil {
		return nil, err
	}
	var enabled []Account
	for _, a := range accounts {
		if a.Enabled {
			enabled = append(enabled, a)
		}
	}
	return enabled, nil
}

func AddAccount(name, githubToken, accountType string) (*Account, error) {
	accountMu.Lock()
	defer accountMu.Unlock()

	accounts, err := readAccounts()
	if err != nil {
		return nil, err
	}

	account := Account{
		ID:          uuid.New().String(),
		Name:        name,
		GithubToken: githubToken,
		AccountType: accountType,
		ApiKey:      "sk-" + uuid.New().String(),
		Enabled:     true,
		CreatedAt:   time.Now().UTC().Format(time.RFC3339),
		Priority:    0,
	}

	accounts = append(accounts, account)
	if err := writeAccounts(accounts); err != nil {
		return nil, err
	}
	return &account, nil
}

func UpdateAccount(id string, updates map[string]interface{}) (*Account, error) {
	accountMu.Lock()
	defer accountMu.Unlock()

	accounts, err := readAccounts()
	if err != nil {
		return nil, err
	}

	for i, a := range accounts {
		if a.ID == id {
			if v, ok := updates["name"].(string); ok {
				accounts[i].Name = v
			}
			if v, ok := updates["githubToken"].(string); ok {
				accounts[i].GithubToken = v
			}
			if v, ok := updates["accountType"].(string); ok {
				accounts[i].AccountType = v
			}
			if v, ok := updates["enabled"].(bool); ok {
				accounts[i].Enabled = v
			}
			if v, ok := updates["priority"]; ok {
				switch pv := v.(type) {
				case float64:
					accounts[i].Priority = int(pv)
				case int:
					accounts[i].Priority = pv
				}
			}
			if err := writeAccounts(accounts); err != nil {
				return nil, err
			}
			return &accounts[i], nil
		}
	}
	return nil, nil
}

func DeleteAccount(id string) error {
	accountMu.Lock()
	defer accountMu.Unlock()

	accounts, err := readAccounts()
	if err != nil {
		return err
	}

	var filtered []Account
	for _, a := range accounts {
		if a.ID != id {
			filtered = append(filtered, a)
		}
	}
	return writeAccounts(filtered)
}

func RegenerateApiKey(id string) (string, error) {
	accountMu.Lock()
	defer accountMu.Unlock()

	accounts, err := readAccounts()
	if err != nil {
		return "", err
	}

	for i, a := range accounts {
		if a.ID == id {
			newKey := "sk-" + uuid.New().String()
			accounts[i].ApiKey = newKey
			if err := writeAccounts(accounts); err != nil {
				return "", err
			}
			return newKey, nil
		}
	}
	return "", nil
}

func GetPoolConfig() (*PoolConfig, error) {
	poolMu.RLock()
	defer poolMu.RUnlock()

	data, err := os.ReadFile(PoolConfigFile())
	if err != nil {
		return &PoolConfig{Strategy: "round-robin"}, nil
	}
	var cfg PoolConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return &PoolConfig{Strategy: "round-robin"}, nil
	}
	if cfg.Strategy == "" {
		cfg.Strategy = "round-robin"
	}
	return &cfg, nil
}

func UpdatePoolConfig(cfg *PoolConfig) error {
	poolMu.Lock()
	defer poolMu.Unlock()

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(PoolConfigFile(), data, 0644)
}

func RegeneratePoolApiKey() (string, error) {
	poolMu.Lock()
	defer poolMu.Unlock()

	data, err := os.ReadFile(PoolConfigFile())
	if err != nil {
		return "", err
	}
	var cfg PoolConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return "", err
	}
	cfg.ApiKey = "sk-pool-" + uuid.New().String()
	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(PoolConfigFile(), out, 0644); err != nil {
		return "", err
	}
	return cfg.ApiKey, nil
}
