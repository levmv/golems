package openai

import (
	"net/http"
)

const (
	DeepSeekChat     = "deepseek-chat"
	DeepSeekReasoner = "deepseek-reasoner"
)

type ClientConfig struct {
	AuthToken  string
	BaseURL    string
	HTTPClient *http.Client
	Header     http.Header
}

func DefaultConfig(authToken string) ClientConfig {
	return ClientConfig{
		AuthToken:  authToken,
		BaseURL:    "https://api.openai.com/v1",
		HTTPClient: &http.Client{},
		Header:     make(http.Header),
	}
}

func DeepSeekConfig(authToken string) ClientConfig {
	return ClientConfig{
		AuthToken:  authToken,
		BaseURL:    "https://api.deepseek.com/v1",
		HTTPClient: &http.Client{},
		Header:     make(http.Header),
	}
}

func OpenRouterConfig(authToken, appTitle, appURL string) ClientConfig {
	header := make(http.Header)
	header.Set("Http-Referer", appURL)
	header.Set("X-Title", appTitle)

	return ClientConfig{
		AuthToken:  authToken,
		BaseURL:    "https://openrouter.ai/api/v1",
		HTTPClient: &http.Client{},
		Header:     header,
	}
}

func OllamaConfig() ClientConfig {
	return ClientConfig{
		AuthToken:  "ollama", // Ollama doesn't care, but needs to not be empty
		BaseURL:    "http://localhost:11434/v1",
		HTTPClient: &http.Client{},
		Header:     make(http.Header),
	}
}
