package proxy

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"

	"github.com/abhishekjha17/intern/internal/models"
	"github.com/abhishekjha17/intern/internal/router"
	"github.com/abhishekjha17/intern/internal/translator"
)

type ProxyHandler struct {
	localTarget *url.URL
	cloudTarget *url.URL
	router      *router.Router
	localModel  string
	httpClient  *http.Client
}

func NewProxyHandler(ollamaUrl, cloudUrl, localModel string, r *router.Router) *ProxyHandler {
	local, _ := url.Parse(ollamaUrl)
	cloud, _ := url.Parse(cloudUrl)
	return &ProxyHandler{
		localTarget: local,
		cloudTarget: cloud,
		router:      r,
		localModel:  localModel,
		httpClient:  &http.Client{},
	}
}

func (h *ProxyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var anthroReq models.AnthropicRequest
	json.Unmarshal(body, &anthroReq)

	log.Printf("recv inference request: model=%s stream=%v", anthroReq.Model, anthroReq.Stream)

	decision := h.router.Decide(anthroReq)
	log.Printf("routing decision: %s", decision)

	if decision == router.RouteLocal {
		h.handleLocal(w, anthroReq)
	} else {
		// Restore body for cloud passthrough
		r.Body = io.NopCloser(bytes.NewBuffer(body))
		h.handleCloud(w, r)
	}
}

func (h *ProxyHandler) handleLocal(w http.ResponseWriter, anthroReq models.AnthropicRequest) {
	// Translate Anthropic request to Ollama format
	ollamaReq := translator.AnthropicToOllama(anthroReq, h.localModel)
	reqBody, err := json.Marshal(ollamaReq)
	if err != nil {
		http.Error(w, "translation error", http.StatusInternalServerError)
		return
	}

	// POST to Ollama's OpenAI-compatible endpoint
	ollamaURL := h.localTarget.String() + "/v1/chat/completions"
	httpReq, err := http.NewRequest("POST", ollamaURL, bytes.NewReader(reqBody))
	if err != nil {
		http.Error(w, "request creation error", http.StatusInternalServerError)
		return
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := h.httpClient.Do(httpReq)
	if err != nil {
		log.Printf("ollama unreachable for inference: %v", err)
		http.Error(w, "local model unavailable", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	if anthroReq.Stream {
		// Streaming: translate Ollama SSE to Anthropic SSE in real-time
		translator.StreamOllamaToAnthropic(w, resp.Body, h.localModel)
	} else {
		// Non-streaming: read full response, translate, write back
		respBody, err := io.ReadAll(resp.Body)
		if err != nil {
			http.Error(w, "read error", http.StatusBadGateway)
			return
		}

		var ollamaResp models.OllamaResponse
		if err := json.Unmarshal(respBody, &ollamaResp); err != nil {
			http.Error(w, "response parse error", http.StatusBadGateway)
			return
		}

		anthropicResp := translator.OllamaToAnthropic(ollamaResp)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(anthropicResp)
	}
}

func (h *ProxyHandler) handleCloud(w http.ResponseWriter, r *http.Request) {
	target := h.cloudTarget
	proxy := httputil.NewSingleHostReverseProxy(target)
	r.URL.Host = target.Host
	r.URL.Scheme = target.Scheme
	r.Header.Set("X-Forwarded-Host", r.Header.Get("Host"))
	r.Host = target.Host
	proxy.ServeHTTP(w, r)
}
