package openai

import (
	"net/http"
	"time"
)

// DefaultResponseHeaderTimeout guards against hung servers, not latency: for
// non-streaming requests the headers arrive only once generation has finished,
// so it must accommodate the slowest expected completion (reasoning models).
const DefaultResponseHeaderTimeout = 5 * time.Minute

const (
	DeepSeekV4Flash = "deepseek-v4-flash"
	DeepSeekV4Pro   = "deepseek-v4-pro"

	// Deprecated: use DeepSeekV4Flash or DeepSeekV4Pro.
	DeepSeekChat = "deepseek-chat"
	// Deprecated: use DeepSeekV4Flash or DeepSeekV4Pro.
	DeepSeekReasoner = "deepseek-reasoner"
)

type ClientConfig struct {
	AuthToken  string
	BaseURL    string
	HTTPClient *http.Client
	Header     http.Header
}

func DefaultHTTPClient() *http.Client {
	if transport, ok := http.DefaultTransport.(*http.Transport); ok {
		cloned := transport.Clone()
		cloned.ResponseHeaderTimeout = DefaultResponseHeaderTimeout
		return &http.Client{Transport: cloned}
	}
	return &http.Client{Transport: &http.Transport{ResponseHeaderTimeout: DefaultResponseHeaderTimeout}}
}

func DefaultConfig(authToken string) ClientConfig {
	return ClientConfig{
		AuthToken:  authToken,
		BaseURL:    "https://api.openai.com/v1",
		HTTPClient: DefaultHTTPClient(),
		Header:     make(http.Header),
	}
}

func DeepSeekConfig(authToken string) ClientConfig {
	return ClientConfig{
		AuthToken:  authToken,
		BaseURL:    "https://api.deepseek.com/v1",
		HTTPClient: DefaultHTTPClient(),
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
		HTTPClient: DefaultHTTPClient(),
		Header:     header,
	}
}

func OllamaConfig() ClientConfig {
	return ClientConfig{
		AuthToken:  "ollama", // Ollama doesn't care, but needs to not be empty
		BaseURL:    "http://localhost:11434/v1",
		HTTPClient: DefaultHTTPClient(),
		Header:     make(http.Header),
	}
}
