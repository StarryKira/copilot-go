package store

import (
	"encoding/json"
	"os"
	"sync"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

type AdminData struct {
	PasswordHash string `json:"passwordHash"`
}

type AdminSession struct {
	Token     string
	ExpiresAt time.Time
}

var (
	adminMu      sync.RWMutex
	sessionsMu   sync.RWMutex
	sessions     = make(map[string]*AdminSession)
	sessionTTL   = 7 * 24 * time.Hour
)

func IsSetupRequired() (bool, error) {
	adminMu.RLock()
	defer adminMu.RUnlock()

	data, err := os.ReadFile(AdminFile())
	if err != nil {
		return true, nil
	}
	if len(data) == 0 || string(data) == "{}" {
		return true, nil
	}
	var admin AdminData
	if err := json.Unmarshal(data, &admin); err != nil {
		return true, nil
	}
	return admin.PasswordHash == "", nil
}

func SetupAdmin(password string) error {
	adminMu.Lock()
	defer adminMu.Unlock()

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}

	admin := AdminData{PasswordHash: string(hash)}
	data, err := json.MarshalIndent(admin, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(AdminFile(), data, 0644)
}

func LoginAdmin(password string) (string, error) {
	adminMu.RLock()
	defer adminMu.RUnlock()

	data, err := os.ReadFile(AdminFile())
	if err != nil {
		return "", err
	}
	var admin AdminData
	if err := json.Unmarshal(data, &admin); err != nil {
		return "", err
	}

	if err := bcrypt.CompareHashAndPassword([]byte(admin.PasswordHash), []byte(password)); err != nil {
		return "", err
	}

	token := uuid.New().String()
	sessionsMu.Lock()
	sessions[token] = &AdminSession{
		Token:     token,
		ExpiresAt: time.Now().Add(sessionTTL),
	}
	sessionsMu.Unlock()

	return token, nil
}

func ValidateSession(token string) bool {
	sessionsMu.RLock()
	defer sessionsMu.RUnlock()

	session, ok := sessions[token]
	if !ok {
		return false
	}
	if time.Now().After(session.ExpiresAt) {
		delete(sessions, token)
		return false
	}
	return true
}
