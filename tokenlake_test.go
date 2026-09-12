package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	pluginv1 "github.com/Tencent/WeKnora/api/proto/plugin/v1"
	plugin "github.com/Tencent/WeKnora/sdk/plugin/go"
	"github.com/stretchr/testify/require"
)

type testEmitter struct {
	content string
	done    bool
	usage   plugin.ModelUsage
}

func (e *testEmitter) Emit(content string, done bool, usage plugin.ModelUsage) error {
	e.content += content
	e.done = done
	e.usage = usage
	return nil
}

func TestValidateConfig(t *testing.T) {
	handler := newTokenLakeHandler()
	require.Empty(t, handler.ValidateConfig(context.Background(), json.RawMessage(`{"api_key":"sk-tr-test"}`)))
	require.NotEmpty(t, handler.ValidateConfig(context.Background(), json.RawMessage(`{"api_key":""}`)))
	require.NotEmpty(t, handler.ValidateConfig(context.Background(), json.RawMessage(`{`)))
}

func TestListModelsFiltersAndSortsChatModels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodGet, r.Method)
		require.Equal(t, "/models", r.URL.Path)
		require.Equal(t, "Bearer sk-tr-test", r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[
			{"id":"z-model","display_name":"Zulu","owned_by":"vendor-z","modality":"chat","aliases":["z"],"capabilities":["Hot"]},
			{"id":"image-model","display_name":"Image","modality":"image_generation"},
			{"id":"a-model","display_name":"Alpha","owned_by":"vendor-a","modality":"CHAT"},
			{"id":"","display_name":"Missing ID","modality":"chat"}
		]}`))
	}))
	defer server.Close()
	handler := newTokenLakeHandler()
	handler.baseURL = server.URL

	models, err := handler.ListModels(context.Background(), plugin.Invocation{}, json.RawMessage(`{"api_key":"sk-tr-test"}`))
	require.NoError(t, err)
	require.Len(t, models, 2)
	require.Equal(t, "a-model", models[0].ID)
	require.Equal(t, "Alpha", models[0].Name)
	require.Equal(t, "vendor-a", models[0].Metadata["owned_by"])
	require.Equal(t, []pluginv1.ModelCapability{pluginv1.ModelCapability_MODEL_CAPABILITY_CHAT}, models[0].Capabilities)
	require.Equal(t, "z-model", models[1].ID)
	require.Equal(t, "z", models[1].Metadata["aliases"])
	require.Equal(t, "Hot", models[1].Metadata["provider_capabilities"])
}

func TestChat(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "/chat/completions", r.URL.Path)
		require.Equal(t, "Bearer sk-tr-test", r.Header.Get("Authorization"))
		var payload map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
		require.Equal(t, "deepseek-v4-pro-0813", payload["model"])
		require.Equal(t, false, payload["stream"])
		require.Equal(t, 0.2, payload["temperature"])
		require.Equal(t, float64(128), payload["max_tokens"])
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":2,"completion_tokens":1}}`))
	}))
	defer server.Close()
	handler := newTokenLakeHandler()
	handler.baseURL = server.URL
	emitter := &testEmitter{}
	err := handler.Chat(context.Background(), plugin.ChatInput{
		Config:   json.RawMessage(`{"api_key":"sk-tr-test"}`),
		Model:    "deepseek-v4-pro-0813",
		Messages: []plugin.ChatMessage{{Role: "user", Content: "hello"}},
		Options:  map[string]string{"temperature": "0.2", "max_tokens": "128"},
	}, emitter)
	require.NoError(t, err)
	require.Equal(t, "ok", emitter.content)
	require.True(t, emitter.done)
	require.Equal(t, plugin.ModelUsage{InputTokens: 2, OutputTokens: 1}, emitter.usage)
}

func TestChatRejectsEmptyInput(t *testing.T) {
	handler := newTokenLakeHandler()
	err := handler.Chat(context.Background(), plugin.ChatInput{Config: json.RawMessage(`{"api_key":"sk-tr-test"}`)}, &testEmitter{})
	require.EqualError(t, err, "model 不能为空")
	err = handler.Chat(context.Background(), plugin.ChatInput{Config: json.RawMessage(`{"api_key":"sk-tr-test"}`), Model: "model"}, &testEmitter{})
	require.EqualError(t, err, "messages 不能为空")
}

func TestAPIErrorRedactsKeyAndLimitsOutput(t *testing.T) {
	const key = "sk-tr-secret"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(key + strings.Repeat("错", maxErrorRunes+20)))
	}))
	defer server.Close()
	handler := newTokenLakeHandler()
	handler.baseURL = server.URL

	_, err := handler.ListModels(context.Background(), plugin.Invocation{}, json.RawMessage(`{"api_key":"`+key+`"}`))
	require.Error(t, err)
	require.NotContains(t, err.Error(), key)
	require.LessOrEqual(t, len([]rune(err.Error())), maxErrorRunes+80)
}

func TestResponseSizeLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(make([]byte, maxResponseBytes+1))
	}))
	defer server.Close()
	handler := newTokenLakeHandler()
	handler.baseURL = server.URL

	_, err := handler.ListModels(context.Background(), plugin.Invocation{}, json.RawMessage(`{"api_key":"sk-tr-test"}`))
	require.ErrorContains(t, err, "响应超过 16 MiB")
}
