package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"copilot-go/auth"
	"copilot-go/config"
	"copilot-go/instance"
	"copilot-go/store"

	"github.com/gin-gonic/gin"
)

// RegisterConsoleAPI registers all Web Console management API routes.
func RegisterConsoleAPI(r *gin.Engine, proxyPort int) {
	// Serve frontend static files
	webDist := findWebDist()
	if webDist != "" {
		r.Static("/assets", filepath.Join(webDist, "assets"))
		r.StaticFile("/", filepath.Join(webDist, "index.html"))
		// SPA fallback: serve index.html for non-API, non-asset routes
		r.NoRoute(func(c *gin.Context) {
			if !strings.HasPrefix(c.Request.URL.Path, "/api") {
				c.File(filepath.Join(webDist, "index.html"))
			}
		})
	}

	api := r.Group("/api")

	// Public endpoints
	api.GET("/config", func(c *gin.Context) {
		needsSetup, _ := store.IsSetupRequired()
		c.JSON(http.StatusOK, gin.H{
			"proxyPort":  proxyPort,
			"needsSetup": needsSetup,
		})
	})

	api.POST("/auth/setup", handleSetup)
	api.POST("/auth/login", handleLogin)

	// Protected endpoints
	protected := api.Group("")
	protected.Use(adminAuthMiddleware())

	protected.GET("/auth/check", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"valid": true})
	})

	// Account routes
	protected.GET("/accounts", handleGetAccounts)
	protected.GET("/accounts/usage", handleGetAllUsage)
	protected.GET("/accounts/:id", handleGetAccount)
	protected.POST("/accounts", handleAddAccount)
	protected.PUT("/accounts/:id", handleUpdateAccount)
	protected.DELETE("/accounts/:id", handleDeleteAccount)
	protected.POST("/accounts/:id/regenerate-key", handleRegenerateKey)
	protected.POST("/accounts/:id/start", handleStartAccount)
	protected.POST("/accounts/:id/stop", handleStopAccount)
	protected.GET("/accounts/:id/usage", handleGetAccountUsage)

	// Device flow auth
	protected.POST("/auth/device-code", handleDeviceCode)
	protected.GET("/auth/poll/:sessionId", handlePollSession)
	protected.POST("/auth/complete", handleCompleteAuth)

	// Pool config
	protected.GET("/pool", handleGetPool)
	protected.PUT("/pool", handleUpdatePool)
	protected.POST("/pool/regenerate-key", handleRegeneratePoolKey)

	// Model mapping
	protected.GET("/model-map", handleGetModelMap)
	protected.PUT("/model-map", handleSetModelMap)
	protected.POST("/model-map", handleAddModelMapping)
	protected.DELETE("/model-map/:copilotId", handleDeleteModelMapping)
}

func adminAuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		authHeader := c.GetHeader("Authorization")
		if authHeader == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "missing authorization header"})
			return
		}

		token := strings.TrimPrefix(authHeader, "Bearer ")
		if !store.ValidateSession(token) {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid or expired session"})
			return
		}
		c.Next()
	}
}

// --- Auth handlers ---

func handleSetup(c *gin.Context) {
	var body struct {
		Password string `json:"password"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Password == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "password is required"})
		return
	}

	needsSetup, _ := store.IsSetupRequired()
	if !needsSetup {
		c.JSON(http.StatusBadRequest, gin.H{"error": "admin already configured"})
		return
	}

	if err := store.SetupAdmin(body.Password); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	token, err := store.LoginAdmin(body.Password)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"token": token})
}

func handleLogin(c *gin.Context) {
	var body struct {
		Password string `json:"password"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Password == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "password is required"})
		return
	}

	token, err := store.LoginAdmin(body.Password)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid password"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"token": token})
}

// --- Account handlers ---

func handleGetAccounts(c *gin.Context) {
	accounts, err := store.GetAccounts()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	type accountWithStatus struct {
		store.Account
		Status string `json:"status"`
		Error  string `json:"error,omitempty"`
	}

	var result []accountWithStatus
	for _, a := range accounts {
		aws := accountWithStatus{
			Account: a,
			Status:  instance.GetInstanceStatus(a.ID),
			Error:   instance.GetInstanceError(a.ID),
		}
		result = append(result, aws)
	}

	if result == nil {
		result = []accountWithStatus{}
	}
	c.JSON(http.StatusOK, result)
}

func handleGetAllUsage(c *gin.Context) {
	accounts, err := store.GetAccounts()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	type batchUsageItem struct {
		AccountID string      `json:"accountId"`
		Name      string      `json:"name"`
		Status    string      `json:"status"`
		Usage     interface{} `json:"usage"`
	}

	var result []batchUsageItem
	for _, a := range accounts {
		status := instance.GetInstanceStatus(a.ID)
		item := batchUsageItem{
			AccountID: a.ID,
			Name:      a.Name,
			Status:    status,
			Usage:     nil,
		}
		// Only fetch usage for running instances
		if status == "running" {
			usage, err := fetchCopilotUsage(a.ID)
			if err == nil {
				item.Usage = usage
			}
		}
		result = append(result, item)
	}
	if result == nil {
		result = []batchUsageItem{}
	}
	c.JSON(http.StatusOK, result)
}

func handleGetAccount(c *gin.Context) {
	id := c.Param("id")
	account, err := store.GetAccount(id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if account == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "account not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"account": account,
		"status":  instance.GetInstanceStatus(id),
		"error":   instance.GetInstanceError(id),
	})
}

func handleAddAccount(c *gin.Context) {
	var body struct {
		Name        string `json:"name"`
		GithubToken string `json:"githubToken"`
		AccountType string `json:"accountType"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	if body.AccountType == "" {
		body.AccountType = "individual"
	}

	account, err := store.AddAccount(body.Name, body.GithubToken, body.AccountType)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusCreated, account)
}

func handleUpdateAccount(c *gin.Context) {
	id := c.Param("id")
	var updates map[string]interface{}
	if err := c.ShouldBindJSON(&updates); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}

	account, err := store.UpdateAccount(id, updates)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if account == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "account not found"})
		return
	}
	c.JSON(http.StatusOK, account)
}

func handleDeleteAccount(c *gin.Context) {
	id := c.Param("id")
	instance.StopInstance(id)
	if err := store.DeleteAccount(id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true})
}

func handleRegenerateKey(c *gin.Context) {
	id := c.Param("id")
	newKey, err := store.RegenerateApiKey(id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"apiKey": newKey})
}

func handleStartAccount(c *gin.Context) {
	id := c.Param("id")
	account, err := store.GetAccount(id)
	if err != nil || account == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "account not found"})
		return
	}

	if err := instance.StartInstance(*account); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "running"})
}

func handleStopAccount(c *gin.Context) {
	id := c.Param("id")
	instance.StopInstance(id)
	c.JSON(http.StatusOK, gin.H{"status": "stopped"})
}

func handleGetAccountUsage(c *gin.Context) {
	id := c.Param("id")
	user, err := instance.GetUser(id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, user)
}

// --- Device flow handlers ---

func handleDeviceCode(c *gin.Context) {
	session, err := auth.StartDeviceFlow()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"sessionId":       session.ID,
		"userCode":        session.UserCode,
		"verificationUri": session.VerificationURI,
		"expiresAt":       session.ExpiresAt,
	})
}

func handlePollSession(c *gin.Context) {
	sessionID := c.Param("sessionId")
	session := auth.GetSession(sessionID)
	if session == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "session not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"status":      session.Status,
		"accessToken": session.AccessToken,
		"error":       session.Error,
	})
}

func handleCompleteAuth(c *gin.Context) {
	var body struct {
		SessionID   string `json:"sessionId"`
		Name        string `json:"name"`
		AccountType string `json:"accountType"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}

	session := auth.GetSession(body.SessionID)
	if session == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "session not found"})
		return
	}
	if session.Status != "complete" || session.AccessToken == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "auth not completed"})
		return
	}

	if body.AccountType == "" {
		body.AccountType = "individual"
	}
	if body.Name == "" {
		body.Name = "GitHub Account"
	}

	account, err := store.AddAccount(body.Name, session.AccessToken, body.AccountType)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	auth.CleanupSession(body.SessionID)
	c.JSON(http.StatusCreated, account)
}

// --- Pool handlers ---

func handleGetPool(c *gin.Context) {
	cfg, err := store.GetPoolConfig()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, cfg)
}

func handleUpdatePool(c *gin.Context) {
	var cfg store.PoolConfig
	if err := c.ShouldBindJSON(&cfg); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	if cfg.Strategy == "" {
		cfg.Strategy = "round-robin"
	}
	if err := store.UpdatePoolConfig(&cfg); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, cfg)
}

func handleRegeneratePoolKey(c *gin.Context) {
	newKey, err := store.RegeneratePoolApiKey()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"apiKey": newKey})
}

// --- Model map handlers ---

func handleGetModelMap(c *gin.Context) {
	mappings, err := store.GetModelMappings()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"mappings": mappings})
}

func handleSetModelMap(c *gin.Context) {
	var body struct {
		Mappings []store.ModelMapping `json:"mappings"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	if err := store.SetModelMappings(body.Mappings); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"mappings": body.Mappings})
}

func handleAddModelMapping(c *gin.Context) {
	var mapping store.ModelMapping
	if err := c.ShouldBindJSON(&mapping); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	if err := store.AddModelMapping(mapping); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusCreated, mapping)
}

func handleDeleteModelMapping(c *gin.Context) {
	copilotID := c.Param("copilotId")
	if err := store.DeleteModelMapping(copilotID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true})
}

// findWebDist locates the web/dist directory relative to the executable or working directory.
func findWebDist() string {
	// Try relative to working directory
	if info, err := os.Stat("web/dist/index.html"); err == nil && !info.IsDir() {
		abs, _ := filepath.Abs("web/dist")
		return abs
	}
	// Try relative to executable
	exe, err := os.Executable()
	if err == nil {
		dir := filepath.Dir(exe)
		candidate := filepath.Join(dir, "web", "dist")
		if info, err := os.Stat(filepath.Join(candidate, "index.html")); err == nil && !info.IsDir() {
			return candidate
		}
	}
	return ""
}

// fetchCopilotUsage fetches usage/quota data for a running account from GitHub Copilot API.
func fetchCopilotUsage(accountID string) (interface{}, error) {
	state := instance.GetInstanceState(accountID)
	if state == nil {
		return nil, fmt.Errorf("instance not running")
	}

	req, err := http.NewRequest("GET", "https://api.github.com/copilot_internal/v2/token", nil)
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

	// Try to get usage from the copilot billing endpoint
	usageReq, err := http.NewRequest("GET", "https://api.github.com/copilot_internal/user/usage", nil)
	if err != nil {
		return nil, err
	}
	for k, v := range config.GithubHeaders(state) {
		usageReq.Header[k] = v
	}

	usageResp, err := client.Do(usageReq)
	if err != nil {
		return nil, err
	}
	defer usageResp.Body.Close()

	var usage interface{}
	if err := json.NewDecoder(usageResp.Body).Decode(&usage); err != nil {
		return nil, err
	}
	return usage, nil
}
