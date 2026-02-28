package instance

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"

	"copilot-go/anthropic"
	"copilot-go/config"
	"copilot-go/store"

	"github.com/gin-gonic/gin"
)

// CompletionsHandler proxies chat completion requests to Copilot.
func CompletionsHandler(c *gin.Context, state *config.State) {
	bodyBytes, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "failed to read request body"})
		return
	}

	// Apply model mapping
	var payload map[string]interface{}
	if err := json.Unmarshal(bodyBytes, &payload); err == nil {
		if model, ok := payload["model"].(string); ok {
			payload["model"] = store.ToCopilotID(model)
			bodyBytes, _ = json.Marshal(payload)
		}
	}

	resp, err := ProxyRequestWithBytes(state, "POST", "/chat/completions", bodyBytes, nil, false)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": fmt.Sprintf("proxy request failed: %v", err)})
		return
	}
	defer resp.Body.Close()

	// Check if streaming
	contentType := resp.Header.Get("Content-Type")
	isStream := strings.Contains(contentType, "text/event-stream")

	if isStream {
		c.Header("Content-Type", "text/event-stream")
		c.Header("Cache-Control", "no-cache")
		c.Header("Connection", "keep-alive")
		c.Status(resp.StatusCode)

		// Pipe upstream SSE directly to client
		reader := bufio.NewReader(resp.Body)
		c.Stream(func(w io.Writer) bool {
			line, err := reader.ReadBytes('\n')
			if err != nil {
				return false
			}
			_, writeErr := w.Write(line)
			return writeErr == nil
		})
	} else {
		// Non-streaming: read full response and forward
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": "failed to read response"})
			return
		}
		c.Data(resp.StatusCode, "application/json", body)
	}
}

// ModelsHandler returns cached models with display ID mapping.
func ModelsHandler(c *gin.Context, state *config.State) {
	state.RLock()
	models := state.Models
	state.RUnlock()

	if models == nil {
		c.JSON(http.StatusOK, config.ModelsResponse{
			Object: "list",
			Data:   []config.ModelEntry{},
		})
		return
	}

	// Apply display ID mapping
	mapped := config.ModelsResponse{
		Object: models.Object,
		Data:   make([]config.ModelEntry, len(models.Data)),
	}
	for i, m := range models.Data {
		mapped.Data[i] = config.ModelEntry{
			ID:      store.ToDisplayID(m.ID),
			Object:  m.Object,
			Created: m.Created,
			OwnedBy: m.OwnedBy,
		}
	}

	c.JSON(http.StatusOK, mapped)
}

// EmbeddingsHandler proxies embedding requests to Copilot.
func EmbeddingsHandler(c *gin.Context, state *config.State) {
	bodyBytes, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "failed to read request body"})
		return
	}

	// Apply model mapping
	var payload map[string]interface{}
	if err := json.Unmarshal(bodyBytes, &payload); err == nil {
		if model, ok := payload["model"].(string); ok {
			payload["model"] = store.ToCopilotID(model)
			bodyBytes, _ = json.Marshal(payload)
		}
	}

	resp, err := ProxyRequestWithBytes(state, "POST", "/embeddings", bodyBytes, nil, false)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": fmt.Sprintf("proxy request failed: %v", err)})
		return
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "failed to read response"})
		return
	}
	c.Data(resp.StatusCode, "application/json", body)
}

// MessagesHandler handles Anthropic /v1/messages endpoint.
func MessagesHandler(c *gin.Context, state *config.State) {
	bodyBytes, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "failed to read request body"})
		return
	}

	var anthropicPayload anthropic.AnthropicMessagesPayload
	if err := json.Unmarshal(bodyBytes, &anthropicPayload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("invalid request: %v", err)})
		return
	}

	// Check for vision content
	hasVision := checkVisionContent(anthropicPayload)

	// Translate to OpenAI format
	openaiPayload := anthropic.TranslateToOpenAI(anthropicPayload)

	openaiBytes, err := json.Marshal(openaiPayload)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to marshal request"})
		return
	}

	resp, err := ProxyRequestWithBytes(state, "POST", "/chat/completions", openaiBytes, nil, hasVision)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": fmt.Sprintf("proxy request failed: %v", err)})
		return
	}
	defer resp.Body.Close()

	if anthropicPayload.Stream {
		handleAnthropicStream(c, resp)
	} else {
		handleAnthropicNonStream(c, resp)
	}
}

func handleAnthropicNonStream(c *gin.Context, resp *http.Response) {
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "failed to read response"})
		return
	}

	if resp.StatusCode != 200 {
		c.Data(resp.StatusCode, "application/json", body)
		return
	}

	var openaiResp anthropic.ChatCompletionResponse
	if err := json.Unmarshal(body, &openaiResp); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "failed to parse upstream response"})
		return
	}

	anthropicResp := anthropic.TranslateToAnthropic(openaiResp)
	c.JSON(http.StatusOK, anthropicResp)
}

func handleAnthropicStream(c *gin.Context, resp *http.Response) {
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Status(http.StatusOK)

	state := anthropic.NewStreamState()
	scanner := bufio.NewScanner(resp.Body)
	// Increase buffer size for large chunks
	scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024)

	c.Stream(func(w io.Writer) bool {
		for scanner.Scan() {
			line := scanner.Text()

			if !strings.HasPrefix(line, "data: ") {
				continue
			}

			data := strings.TrimPrefix(line, "data: ")
			if data == "[DONE]" {
				// Send message_stop
				writeSSE(w, "message_stop", map[string]string{"type": "message_stop"})
				return false
			}

			var chunk anthropic.ChatCompletionResponse
			if err := json.Unmarshal([]byte(data), &chunk); err != nil {
				log.Printf("Failed to parse SSE chunk: %v", err)
				continue
			}

			events := anthropic.TranslateChunkToAnthropicEvents(chunk, state)
			for _, event := range events {
				writeSSE(w, event.Event, event.Data)
			}
		}
		return false
	})
}

func writeSSE(w io.Writer, event string, data interface{}) {
	jsonData, err := json.Marshal(data)
	if err != nil {
		return
	}
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, string(jsonData))
}

// CountTokensHandler provides a simplified token count estimation.
func CountTokensHandler(c *gin.Context, state *config.State) {
	bodyBytes, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "failed to read request body"})
		return
	}

	var payload anthropic.AnthropicMessagesPayload
	if err := json.Unmarshal(bodyBytes, &payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("invalid request: %v", err)})
		return
	}

	// Rough estimation: ~4 chars per token
	totalChars := 0

	// Count system
	if payload.System != nil {
		sysData, _ := json.Marshal(payload.System)
		totalChars += len(string(sysData))
	}

	// Count messages
	for _, msg := range payload.Messages {
		msgData, _ := json.Marshal(msg.Content)
		totalChars += len(string(msgData))
	}

	// Count tools
	if len(payload.Tools) > 0 {
		toolData, _ := json.Marshal(payload.Tools)
		totalChars += len(string(toolData))
	}

	inputTokens := totalChars / 4
	if inputTokens < 1 {
		inputTokens = 1
	}

	c.JSON(http.StatusOK, gin.H{
		"input_tokens": inputTokens,
	})
}

func checkVisionContent(payload anthropic.AnthropicMessagesPayload) bool {
	for _, msg := range payload.Messages {
		blocks := anthropic.ParseContentBlocksPublic(msg.Content)
		for _, b := range blocks {
			if b.Type == "image" {
				return true
			}
		}
	}
	return false
}
