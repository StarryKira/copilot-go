package anthropic

import (
	"encoding/json"
	"fmt"
	"strings"

	"copilot-go/store"
)

// TranslateToOpenAI converts an Anthropic messages payload to OpenAI chat completions payload.
func TranslateToOpenAI(payload AnthropicMessagesPayload) ChatCompletionsPayload {
	result := ChatCompletionsPayload{
		Model:       store.ToCopilotID(payload.Model),
		Stream:      payload.Stream,
		Temperature: payload.Temperature,
		TopP:        payload.TopP,
	}

	if payload.MaxTokens > 0 {
		result.MaxTokens = payload.MaxTokens
	}

	if payload.Stream {
		result.StreamOptions = &StreamOptions{IncludeUsage: true}
	}

	if payload.StopSequences != nil && len(payload.StopSequences) > 0 {
		result.Stop = payload.StopSequences
	}

	// Convert system prompt
	var messages []OpenAIMessage
	if payload.System != nil {
		systemText := extractSystemText(payload.System)
		if systemText != "" {
			messages = append(messages, OpenAIMessage{
				Role:    "system",
				Content: systemText,
			})
		}
	}

	// Convert messages
	for _, msg := range payload.Messages {
		converted := convertMessage(msg)
		messages = append(messages, converted...)
	}
	result.Messages = messages

	// Convert tools
	if len(payload.Tools) > 0 {
		for _, tool := range payload.Tools {
			result.Tools = append(result.Tools, OpenAITool{
				Type: "function",
				Function: OpenAIFunction{
					Name:        tool.Name,
					Description: tool.Description,
					Parameters:  tool.InputSchema,
				},
			})
		}
	}

	// Convert tool_choice
	if payload.ToolChoice != nil {
		result.ToolChoice = convertToolChoice(payload.ToolChoice)
	}

	return result
}

func extractSystemText(system interface{}) string {
	switch v := system.(type) {
	case string:
		return v
	case []interface{}:
		var parts []string
		for _, item := range v {
			if m, ok := item.(map[string]interface{}); ok {
				if t, ok := m["text"].(string); ok {
					parts = append(parts, t)
				}
			}
		}
		return strings.Join(parts, "\n")
	default:
		// Try JSON marshal/unmarshal as []SystemBlock
		data, err := json.Marshal(v)
		if err != nil {
			return fmt.Sprintf("%v", v)
		}
		var blocks []SystemBlock
		if err := json.Unmarshal(data, &blocks); err != nil {
			return string(data)
		}
		var parts []string
		for _, b := range blocks {
			parts = append(parts, b.Text)
		}
		return strings.Join(parts, "\n")
	}
}

func convertMessage(msg AnthropicMessage) []OpenAIMessage {
	var result []OpenAIMessage

	// Handle string content
	if str, ok := msg.Content.(string); ok {
		result = append(result, OpenAIMessage{
			Role:    msg.Role,
			Content: str,
		})
		return result
	}

	// Handle array content
	blocks := parseContentBlocks(msg.Content)
	if len(blocks) == 0 {
		result = append(result, OpenAIMessage{
			Role:    msg.Role,
			Content: "",
		})
		return result
	}

	if msg.Role == "assistant" {
		return convertAssistantMessage(blocks)
	}

	if msg.Role == "user" {
		return convertUserMessage(blocks)
	}

	// Default: join text blocks
	var texts []string
	for _, b := range blocks {
		if b.Type == "text" {
			texts = append(texts, b.Text)
		}
	}
	result = append(result, OpenAIMessage{
		Role:    msg.Role,
		Content: strings.Join(texts, "\n"),
	})
	return result
}

func convertAssistantMessage(blocks []ContentBlock) []OpenAIMessage {
	var textParts []string
	var toolCalls []ToolCall

	for _, block := range blocks {
		switch block.Type {
		case "text":
			textParts = append(textParts, block.Text)
		case "thinking":
			if block.Thinking != "" {
				textParts = append(textParts, block.Thinking)
			}
		case "tool_use":
			argsJSON, _ := json.Marshal(block.Input)
			toolCalls = append(toolCalls, ToolCall{
				ID:   block.ID,
				Type: "function",
				Function: FunctionCall{
					Name:      block.Name,
					Arguments: string(argsJSON),
				},
			})
		}
	}

	msg := OpenAIMessage{Role: "assistant"}
	if len(textParts) > 0 {
		msg.Content = strings.Join(textParts, "\n")
	}
	if len(toolCalls) > 0 {
		msg.ToolCalls = toolCalls
	}
	return []OpenAIMessage{msg}
}

func convertUserMessage(blocks []ContentBlock) []OpenAIMessage {
	var result []OpenAIMessage
	var contentParts []interface{}
	hasToolResults := false

	for _, block := range blocks {
		switch block.Type {
		case "tool_result":
			hasToolResults = true
		}
	}

	if !hasToolResults {
		// Simple user message - may have text + images
		for _, block := range blocks {
			switch block.Type {
			case "text":
				contentParts = append(contentParts, OpenAIContentPart{
					Type: "text",
					Text: block.Text,
				})
			case "image":
				if block.Source != nil {
					dataURI := fmt.Sprintf("data:%s;base64,%s", block.Source.MediaType, block.Source.Data)
					contentParts = append(contentParts, OpenAIContentPart{
						Type: "image_url",
						ImageURL: &OpenAIImageURL{
							URL: dataURI,
						},
					})
				}
			}
		}
		if len(contentParts) == 1 {
			if tp, ok := contentParts[0].(OpenAIContentPart); ok && tp.Type == "text" {
				result = append(result, OpenAIMessage{
					Role:    "user",
					Content: tp.Text,
				})
				return result
			}
		}
		result = append(result, OpenAIMessage{
			Role:    "user",
			Content: contentParts,
		})
		return result
	}

	// Has tool results - split into tool messages and user messages
	for _, block := range blocks {
		switch block.Type {
		case "tool_result":
			toolContent := extractToolResultContent(block)
			result = append(result, OpenAIMessage{
				Role:       "tool",
				Content:    toolContent,
				ToolCallID: block.ToolUseID,
			})
		case "text":
			result = append(result, OpenAIMessage{
				Role:    "user",
				Content: block.Text,
			})
		case "image":
			if block.Source != nil {
				dataURI := fmt.Sprintf("data:%s;base64,%s", block.Source.MediaType, block.Source.Data)
				result = append(result, OpenAIMessage{
					Role: "user",
					Content: []OpenAIContentPart{{
						Type: "image_url",
						ImageURL: &OpenAIImageURL{
							URL: dataURI,
						},
					}},
				})
			}
		}
	}
	return result
}

func extractToolResultContent(block ContentBlock) string {
	if block.Content2 == nil {
		return ""
	}
	switch v := block.Content2.(type) {
	case string:
		return v
	case []interface{}:
		var parts []string
		for _, item := range v {
			if m, ok := item.(map[string]interface{}); ok {
				if t, ok := m["text"].(string); ok {
					parts = append(parts, t)
				}
			}
		}
		return strings.Join(parts, "\n")
	default:
		data, _ := json.Marshal(v)
		return string(data)
	}
}

func convertToolChoice(tc interface{}) interface{} {
	switch v := tc.(type) {
	case string:
		switch v {
		case "auto":
			return "auto"
		case "any":
			return "required"
		case "none":
			return "none"
		default:
			return "auto"
		}
	case map[string]interface{}:
		t, _ := v["type"].(string)
		switch t {
		case "auto":
			return "auto"
		case "any":
			return "required"
		case "none":
			return "none"
		case "tool":
			name, _ := v["name"].(string)
			return map[string]interface{}{
				"type": "function",
				"function": map[string]string{
					"name": name,
				},
			}
		default:
			return "auto"
		}
	default:
		return "auto"
	}
}

// ParseContentBlocksPublic is the exported version of parseContentBlocks.
func ParseContentBlocksPublic(content interface{}) []ContentBlock {
	return parseContentBlocks(content)
}

func parseContentBlocks(content interface{}) []ContentBlock {
	data, err := json.Marshal(content)
	if err != nil {
		return nil
	}
	var blocks []ContentBlock
	if err := json.Unmarshal(data, &blocks); err != nil {
		return nil
	}
	return blocks
}
