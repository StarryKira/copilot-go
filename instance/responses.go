package instance

import (
	"bufio"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"

	"copilot-go/config"
	"copilot-go/store"

	"github.com/gin-gonic/gin"
)

// DoResponsesProxy forwards requests directly to GitHub Copilot /responses endpoint.
func DoResponsesProxy(state *config.State, bodyBytes []byte) (*http.Response, error) {
	// Convert model ID
	var payload map[string]interface{}
	if err := json.Unmarshal(bodyBytes, &payload); err == nil {
		if model, ok := payload["model"].(string); ok {
			payload["model"] = store.ToCopilotID(model)
			bodyBytes, _ = json.Marshal(payload)
		}
	}

	extraHeaders := make(http.Header)
	extraHeaders.Set("X-Initiator", "user")

	return ProxyRequestWithBytes(state, "POST", "/responses", bodyBytes, extraHeaders, false)
}

// ForwardResponsesResponse forwards the upstream response directly to client.
func ForwardResponsesResponse(c *gin.Context, resp *http.Response) {
	defer func() { _ = resp.Body.Close() }()

	contentType := resp.Header.Get("Content-Type")
	isStream := strings.Contains(contentType, "text/event-stream")

	if isStream {
		c.Header("Content-Type", "text/event-stream")
		c.Header("Cache-Control", "no-cache")
		c.Header("Connection", "keep-alive")
		c.Header("X-Accel-Buffering", "no")
		c.Status(resp.StatusCode)

		reader := bufio.NewReaderSize(resp.Body, 10*1024*1024)
		c.Stream(func(w io.Writer) bool {
			line, err := reader.ReadBytes('\n')
			if len(line) > 0 {
				if _, writeErr := w.Write(line); writeErr != nil {
					return false
				}
				if flusher, ok := w.(http.Flusher); ok {
					flusher.Flush()
				}
			}
			if err != nil {
				if err != io.EOF {
					log.Printf("Responses stream read error: %v", err)
				}
				return false
			}
			return true
		})
	} else {
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": "failed to read response"})
			return
		}
		c.Data(resp.StatusCode, "application/json", body)
	}
}