package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	pluginv1 "github.com/Tencent/WeKnora/api/proto/plugin/v1"
	plugin "github.com/Tencent/WeKnora/sdk/plugin/go"
)

const (
	tokenLakeBaseURL = "https://tokenlake.com.cn:8081/v1"
	maxResponseBytes = 16 * 1024 * 1024
	maxErrorRunes    = 512
)

type tokenLakeConfig struct {
	APIKey string `json:"api_key"`
}

type tokenLakeHandler struct {
	client  *http.Client
	baseURL string
}

type tokenLakeModel struct {
	ID           string   `json:"id"`
	DisplayName  string   `json:"display_name"`
	OwnedBy      string   `json:"owned_by"`
	Modality     string   `json:"modality"`
	Aliases      []string `json:"aliases"`
	Capabilities []string `json:"capabilities"`
}

func newTokenLakeHandler() *tokenLakeHandler {
	return &tokenLakeHandler{
		client:  &http.Client{Timeout: 110 * time.Second},
		baseURL: tokenLakeBaseURL,
	}
}

func (h *tokenLakeHandler) ValidateConfig(_ context.Context, raw json.RawMessage) []plugin.FieldViolation {
	if _, err := parseTokenLakeConfig(raw); err != nil {
		return []plugin.FieldViolation{{Field: "api_key", Description: err.Error()}}
	}
	return nil
}

func (*tokenLakeHandler) HealthCheck(context.Context) plugin.Health {
	return plugin.Health{
		Status:  pluginv1.HealthCheckResponse_STATUS_SERVING,
		Message: "芯元无界模型插件运行正常",
	}
}

func (h *tokenLakeHandler) ListModels(ctx context.Context, _ plugin.Invocation, raw json.RawMessage) ([]plugin.Model, error) {
	config, err := parseTokenLakeConfig(raw)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, h.endpoint("models"), nil)
	if err != nil {
		return nil, err
	}
	setHeaders(request, config.APIKey)
	responseBody, err := h.do(request, config.APIKey)
	if err != nil {
		return nil, fmt.Errorf("查询芯元无界模型: %w", err)
	}
	var response struct {
		Data []tokenLakeModel `json:"data"`
	}
	if err := json.Unmarshal(responseBody, &response); err != nil {
		return nil, fmt.Errorf("解析芯元无界模型列表: %w", err)
	}

	models := make([]plugin.Model, 0, len(response.Data))
	for _, model := range response.Data {
		model.ID = strings.TrimSpace(model.ID)
		if model.ID == "" || !strings.EqualFold(strings.TrimSpace(model.Modality), "chat") {
			continue
		}
		name := strings.TrimSpace(model.DisplayName)
		if name == "" {
			name = model.ID
		}
		metadata := map[string]string{"modality": "chat"}
		if ownedBy := strings.TrimSpace(model.OwnedBy); ownedBy != "" {
			metadata["owned_by"] = ownedBy
		}
		if aliases := cleanList(model.Aliases); len(aliases) > 0 {
			metadata["aliases"] = strings.Join(aliases, ",")
		}
		if capabilities := cleanList(model.Capabilities); len(capabilities) > 0 {
			metadata["provider_capabilities"] = strings.Join(capabilities, ",")
		}
		models = append(models, plugin.Model{
			ID:           model.ID,
			Name:         name,
			Capabilities: []pluginv1.ModelCapability{pluginv1.ModelCapability_MODEL_CAPABILITY_CHAT},
			Metadata:     metadata,
		})
	}
	sort.Slice(models, func(i, j int) bool {
		if models[i].Name == models[j].Name {
			return models[i].ID < models[j].ID
		}
		return models[i].Name < models[j].Name
	})
	return models, nil
}

func (h *tokenLakeHandler) Chat(ctx context.Context, input plugin.ChatInput, emitter plugin.ChatEmitter) error {
	config, err := parseTokenLakeConfig(input.Config)
	if err != nil {
		return err
	}
	model := strings.TrimSpace(input.Model)
	if model == "" {
		return errors.New("model 不能为空")
	}
	if len(input.Messages) == 0 {
		return errors.New("messages 不能为空")
	}
	messages := make([]map[string]string, 0, len(input.Messages))
	for _, message := range input.Messages {
		messages = append(messages, map[string]string{"role": message.Role, "content": message.Content})
	}
	payload := map[string]any{"model": model, "messages": messages, "stream": false}
	copyNumericOption(payload, input.Options, "temperature")
	copyNumericOption(payload, input.Options, "top_p")
	copyIntegerOption(payload, input.Options, "max_tokens")
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, h.endpoint("chat/completions"), bytes.NewReader(body))
	if err != nil {
		return err
	}
	setHeaders(request, config.APIKey)
	responseBody, err := h.do(request, config.APIKey)
	if err != nil {
		return fmt.Errorf("调用芯元无界对话 API: %w", err)
	}
	var response struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     uint64 `json:"prompt_tokens"`
			CompletionTokens uint64 `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(responseBody, &response); err != nil {
		return fmt.Errorf("解析芯元无界对话响应: %w", err)
	}
	if len(response.Choices) == 0 {
		return errors.New("芯元无界对话响应不包含结果")
	}
	return emitter.Emit(response.Choices[0].Message.Content, true, plugin.ModelUsage{
		InputTokens:  response.Usage.PromptTokens,
		OutputTokens: response.Usage.CompletionTokens,
	})
}

func (*tokenLakeHandler) Embed(context.Context, plugin.EmbedInput) (plugin.EmbedOutput, error) {
	return plugin.EmbedOutput{}, errors.New("此插件未声明 embedding 能力")
}

func (*tokenLakeHandler) Rerank(context.Context, plugin.RerankInput) (plugin.RerankOutput, error) {
	return plugin.RerankOutput{}, errors.New("此插件未声明 rerank 能力")
}

func (h *tokenLakeHandler) endpoint(path string) string {
	return strings.TrimRight(h.baseURL, "/") + "/" + strings.TrimLeft(path, "/")
}

func (h *tokenLakeHandler) do(request *http.Request, apiKey string) ([]byte, error) {
	response, err := h.client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	limited := io.LimitReader(response.Body, maxResponseBytes+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return nil, fmt.Errorf("读取响应: %w", err)
	}
	if len(body) > maxResponseBytes {
		return nil, errors.New("响应超过 16 MiB")
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("HTTP %d: %s", response.StatusCode, safeAPIError(body, apiKey))
	}
	return body, nil
}

func parseTokenLakeConfig(raw json.RawMessage) (tokenLakeConfig, error) {
	var config tokenLakeConfig
	if err := json.Unmarshal(raw, &config); err != nil {
		return config, errors.New("配置必须是合法 JSON")
	}
	config.APIKey = strings.TrimSpace(config.APIKey)
	if config.APIKey == "" {
		return config, errors.New("api_key 不能为空")
	}
	return config, nil
}

func setHeaders(request *http.Request, apiKey string) {
	request.Header.Set("Authorization", "Bearer "+apiKey)
	request.Header.Set("Content-Type", "application/json")
}

func copyNumericOption(payload map[string]any, options map[string]string, key string) {
	if value, err := strconv.ParseFloat(options[key], 64); err == nil {
		payload[key] = value
	}
}

func copyIntegerOption(payload map[string]any, options map[string]string, key string) {
	if value, err := strconv.Atoi(options[key]); err == nil && value > 0 {
		payload[key] = value
	}
}

func cleanList(values []string) []string {
	cleaned := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			cleaned = append(cleaned, value)
		}
	}
	return cleaned
}

func safeAPIError(body []byte, secrets ...string) string {
	text := strings.TrimSpace(string(body))
	for _, secret := range secrets {
		if secret != "" {
			text = strings.ReplaceAll(text, secret, "[REDACTED]")
		}
	}
	if !utf8.ValidString(text) {
		text = strings.ToValidUTF8(text, "�")
	}
	runes := []rune(text)
	if len(runes) > maxErrorRunes {
		text = string(runes[:maxErrorRunes])
	}
	if text == "" {
		return "empty response"
	}
	return text
}
