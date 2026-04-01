package handler

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"copilot-go/auth"
	"copilot-go/config"
	"copilot-go/instance"
	"copilot-go/store"
	"copilot-go/web"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// RegisterConsoleAPI registers all Web Console management API routes.
func RegisterConsoleAPI(r *gin.Engine, proxyPort int) {
	// Serve frontend static files
	webDist := findWebDist()
	if webDist != "" {
		// Development: serve from filesystem
		r.Static("/assets", filepath.Join(webDist, "assets"))
		r.NoRoute(func(c *gin.Context) {
			if !strings.HasPrefix(c.Request.URL.Path, "/api") && !strings.HasPrefix(c.Request.URL.Path, "/assets") {
				c.File(filepath.Join(webDist, "index.html"))
			}
		})
	} else {
		// Production: serve from embedded filesystem
		distFS, err := fs.Sub(web.Dist, "dist")
		if err != nil {
			log.Printf("Warning: failed to access embedded web dist: %v", err)
		} else {
			assetsFS, _ := fs.Sub(distFS, "assets")
			r.StaticFS("/assets", http.FS(assetsFS))
			r.NoRoute(func(c *gin.Context) {
				if !strings.HasPrefix(c.Request.URL.Path, "/api") && !strings.HasPrefix(c.Request.URL.Path, "/assets") {
					data, err := fs.ReadFile(distFS, "index.html")
					if err != nil {
						c.String(http.StatusInternalServerError, "failed to load index.html")
						return
					}
					c.Data(http.StatusOK, "text/html; charset=utf-8", data)
				}
			})
		}
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

	// Copilot models
	protected.GET("/copilot-models", handleGetCopilotModels)

	// Proxy config (outbound HTTP proxy)
	protected.GET("/proxy-config", handleGetProxyConfig)
	protected.PUT("/proxy-config", handleUpdateProxyConfig)

	// Proxy usage stats (from in-memory tracking)
	protected.GET("/usage", handleGetProxyUsage)
	protected.GET("/usage/:id", handleGetProxyAccountUsage)

	// Claude Code command generator
	protected.POST("/claude-code-command", handleClaudeCodeCommand(proxyPort))
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
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Password == "" || body.Username == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "username and password are required"})
		return
	}

	needsSetup, _ := store.IsSetupRequired()
	if !needsSetup {
		c.JSON(http.StatusBadRequest, gin.H{"error": "admin already configured"})
		return
	}

	if err := store.SetupAdmin(body.Username, body.Password); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	token, err := store.LoginAdmin(body.Username, body.Password)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"token": token})
}

func handleLogin(c *gin.Context) {
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Password == "" || body.Username == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "username and password are required"})
		return
	}

	token, err := store.LoginAdmin(body.Username, body.Password)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid username or password"})
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
	usage, err := fetchCopilotUsage(id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, usage)
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
	if session.Status != "completed" || session.AccessToken == "" {
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
	var updates map[string]interface{}
	if err := c.ShouldBindJSON(&updates); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}

	// Read existing config first, then merge updates
	existing, err := store.GetPoolConfig()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	if v, ok := updates["enabled"]; ok {
		if b, ok := v.(bool); ok {
			existing.Enabled = b
		}
	}
	if v, ok := updates["strategy"]; ok {
		if s, ok := v.(string); ok && s != "" {
			existing.Strategy = s
		}
	}
	if v, ok := updates["rateLimitRPM"]; ok {
		switch rv := v.(type) {
		case float64:
			existing.RateLimitRPM = int(rv)
		case int:
			existing.RateLimitRPM = rv
		}
	}

	// Generate a key if pool is being enabled and has no key yet
	if existing.Enabled && existing.ApiKey == "" {
		existing.ApiKey = "sk-pool-" + uuid.New().String()
	}

	if existing.Strategy == "" {
		existing.Strategy = "round-robin"
	}

	if err := store.UpdatePoolConfig(existing); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Sync per-account rate limiter.
	instance.SetPerAccountRPM(existing.RateLimitRPM)

	c.JSON(http.StatusOK, existing)
}

func handleRegeneratePoolKey(c *gin.Context) {
	_, err := store.RegeneratePoolApiKey()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	// Return the full config so frontend gets the complete PoolConfig object
	cfg, err := store.GetPoolConfig()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, cfg)
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

// --- Copilot models handler ---

func handleGetCopilotModels(c *gin.Context) {
	models := instance.GetAllCachedModels()
	mappings, _ := store.GetModelMappings()

	// Build a lookup from copilotId -> displayId
	mappingLookup := make(map[string]string)
	for _, m := range mappings {
		mappingLookup[m.CopilotID] = m.DisplayID
	}

	type copilotModelItem struct {
		ID        string `json:"id"`
		OwnedBy   string `json:"ownedBy"`
		Mapped    bool   `json:"mapped"`
		DisplayID string `json:"displayId"`
	}

	result := make([]copilotModelItem, 0, len(models))
	for _, m := range models {
		item := copilotModelItem{
			ID:      m.ID,
			OwnedBy: m.OwnedBy,
		}
		if did, ok := mappingLookup[m.ID]; ok {
			item.Mapped = true
			item.DisplayID = did
		}
		result = append(result, item)
	}

	c.JSON(http.StatusOK, gin.H{"models": result})
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

// --- Proxy config handlers ---

func handleGetProxyConfig(c *gin.Context) {
	cfg, err := store.GetProxyConfig()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, cfg)
}

func handleUpdateProxyConfig(c *gin.Context) {
	var cfg store.ProxyConfig
	if err := c.ShouldBindJSON(&cfg); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	if cfg.ProxyURL != "" {
		if _, err := url.ParseRequestURI(cfg.ProxyURL); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid proxy URL"})
			return
		}
	}
	if err := store.UpdateProxyConfig(cfg); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	config.SetProxyURL(cfg.ProxyURL)
	instance.RebuildHTTPClients()
	c.JSON(http.StatusOK, cfg)
}

// --- Proxy usage handlers ---

func handleGetProxyUsage(c *gin.Context) {
	snapshots := instance.GetAllUsageSnapshots()
	c.JSON(http.StatusOK, snapshots)
}

func handleGetProxyAccountUsage(c *gin.Context) {
	id := c.Param("id")
	snapshot := instance.GetUsageSnapshot(id)
	c.JSON(http.StatusOK, snapshot)
}

// --- Claude Code command generator ---

func handleClaudeCodeCommand(proxyPort int) gin.HandlerFunc {
	return func(c *gin.Context) {
		var body struct {
			Model      string `json:"model"`
			SmallModel string `json:"smallModel"`
			ApiKey     string `json:"apiKey"`
		}
		if err := c.ShouldBindJSON(&body); err != nil || body.ApiKey == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "apiKey is required"})
			return
		}

		baseURL := fmt.Sprintf("http://127.0.0.1:%d", proxyPort)
		model := body.Model
		if model == "" {
			model = "claude-sonnet-4"
		}
		smallModel := body.SmallModel
		if smallModel == "" {
			smallModel = model
		}

		bash := fmt.Sprintf(
			`ANTHROPIC_BASE_URL=%s \`+"\n"+
				`ANTHROPIC_AUTH_TOKEN=%s \`+"\n"+
				`ANTHROPIC_MODEL=%s \`+"\n"+
				`ANTHROPIC_SMALL_FAST_MODEL=%s \`+"\n"+
				`DISABLE_NON_ESSENTIAL_MODEL_CALLS=1 \`+"\n"+
				`CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1 \`+"\n"+
				`claude`,
			baseURL, body.ApiKey, model, smallModel,
		)

		powershell := fmt.Sprintf(
			`$env:ANTHROPIC_BASE_URL="%s"`+"\n"+
				`$env:ANTHROPIC_AUTH_TOKEN="%s"`+"\n"+
				`$env:ANTHROPIC_MODEL="%s"`+"\n"+
				`$env:ANTHROPIC_SMALL_FAST_MODEL="%s"`+"\n"+
				`$env:DISABLE_NON_ESSENTIAL_MODEL_CALLS="1"`+"\n"+
				`$env:CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC="1"`+"\n"+
				`claude`,
			baseURL, body.ApiKey, model, smallModel,
		)

		cmd := fmt.Sprintf(
			`set ANTHROPIC_BASE_URL=%s`+"\n"+
				`set ANTHROPIC_AUTH_TOKEN=%s`+"\n"+
				`set ANTHROPIC_MODEL=%s`+"\n"+
				`set ANTHROPIC_SMALL_FAST_MODEL=%s`+"\n"+
				`set DISABLE_NON_ESSENTIAL_MODEL_CALLS=1`+"\n"+
				`set CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1`+"\n"+
				`claude`,
			baseURL, body.ApiKey, model, smallModel,
		)

		c.JSON(http.StatusOK, gin.H{
			"bash":       bash,
			"powershell": powershell,
			"cmd":        cmd,
		})
	}
}

// fetchCopilotUsage fetches usage/quota data for a running account from GitHub Copilot API.
func fetchCopilotUsage(accountID string) (interface{}, error) {
	state := instance.GetInstanceState(accountID)
	if state == nil {
		return nil, fmt.Errorf("instance not running")
	}

	req, err := http.NewRequest("GET", "https://api.github.com/copilot_internal/user", nil)
	if err != nil {
		return nil, err
	}
	for k, v := range config.GithubHeaders(state) {
		req.Header[k] = v
	}

	resp, err := instance.GetDefaultHTTPClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("copilot user API returned status %d", resp.StatusCode)
	}

	var usage interface{}
	if err := json.NewDecoder(resp.Body).Decode(&usage); err != nil {
		return nil, err
	}
	return usage, nil
}
