package handler

import (
	"net/http"
	"strings"

	"copilot-go/config"
	"copilot-go/instance"
	"copilot-go/store"

	"github.com/gin-gonic/gin"
)

// RegisterProxy sets up the proxy server routes.
func RegisterProxy(r *gin.Engine) {
	r.Use(proxyAuth())

	// OpenAI compatible endpoints
	r.POST("/chat/completions", proxyCompletions)
	r.POST("/v1/chat/completions", proxyCompletions)
	r.GET("/models", proxyModels)
	r.GET("/v1/models", proxyModels)
	r.POST("/embeddings", proxyEmbeddings)
	r.POST("/v1/embeddings", proxyEmbeddings)

	// Anthropic compatible endpoints
	r.POST("/v1/messages", proxyMessages)
	r.POST("/v1/messages/count_tokens", proxyCountTokens)
}

func proxyAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		authHeader := c.GetHeader("Authorization")
		if authHeader == "" {
			// Also check x-api-key for Anthropic-style auth
			apiKey := c.GetHeader("x-api-key")
			if apiKey != "" {
				authHeader = "Bearer " + apiKey
			}
		}

		if authHeader == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "missing authorization"})
			return
		}

		token := strings.TrimPrefix(authHeader, "Bearer ")

		// Check pool API key first
		poolCfg, _ := store.GetPoolConfig()
		if poolCfg != nil && poolCfg.Enabled && poolCfg.ApiKey == token {
			c.Set("isPool", true)
			c.Set("poolStrategy", poolCfg.Strategy)
			c.Next()
			return
		}

		// Check individual account API key
		account, err := store.GetAccountByApiKey(token)
		if err != nil || account == nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid API key"})
			return
		}

		c.Set("accountID", account.ID)
		c.Set("isPool", false)
		c.Next()
	}
}

func resolveState(c *gin.Context) *config.State {
	isPool, _ := c.Get("isPool")
	if isPool == true {
		strategy := ""
		if s, ok := c.Get("poolStrategy"); ok {
			strategy = s.(string)
		}
		account, err := instance.SelectAccount(strategy, nil)
		if err != nil || account == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "no available accounts in pool"})
			return nil
		}
		state := instance.GetInstanceState(account.ID)
		if state == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "selected account instance not running"})
			return nil
		}
		return state
	}

	accountID, exists := c.Get("accountID")
	if !exists {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "no account context"})
		return nil
	}
	state := instance.GetInstanceState(accountID.(string))
	if state == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "account instance not running"})
		return nil
	}
	return state
}

func proxyCompletions(c *gin.Context) {
	state := resolveState(c)
	if state == nil {
		return
	}
	instance.CompletionsHandler(c, state)
}

func proxyModels(c *gin.Context) {
	state := resolveState(c)
	if state == nil {
		return
	}
	instance.ModelsHandler(c, state)
}

func proxyEmbeddings(c *gin.Context) {
	state := resolveState(c)
	if state == nil {
		return
	}
	instance.EmbeddingsHandler(c, state)
}

func proxyMessages(c *gin.Context) {
	state := resolveState(c)
	if state == nil {
		return
	}
	instance.MessagesHandler(c, state)
}

func proxyCountTokens(c *gin.Context) {
	state := resolveState(c)
	if state == nil {
		return
	}
	instance.CountTokensHandler(c, state)
}
