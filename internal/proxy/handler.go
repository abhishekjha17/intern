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
)

type ProxyHandler struct {
	localTarget *url.URL
	cloudTarget *url.URL
}

func NewProxyHandler(ollamaUrl, cloudUrl string) *ProxyHandler {
	local, _ := url.Parse(ollamaUrl) // Default Ollama port
	cloud, _ := url.Parse(cloudUrl)
	return &ProxyHandler{localTarget: local, cloudTarget: cloud}
}

func (h *ProxyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// 1. Read body to peek
	body, _ := io.ReadAll(r.Body)
	var anthroReq models.AnthropicRequest
	json.Unmarshal(body, &anthroReq)
	r.Body = io.NopCloser(bytes.NewBuffer(body))

	log.Printf("recv inference request :%s", anthroReq)

	// 2. Get decision from Bouncer
	decision := router.Decide(anthroReq)

	log.Printf("routing decision : %s", decision)

	// 3. Setup Reverse Proxy
	var target *url.URL
	if decision == router.RouteLocal {
		target = h.localTarget
		// Note: Here you would call internal/translator to fix headers/paths
	} else {
		target = h.cloudTarget
	}

	proxy := httputil.NewSingleHostReverseProxy(target)

	// Update the request for the target
	r.URL.Host = target.Host
	r.URL.Scheme = target.Scheme
	r.Header.Set("X-Forwarded-Host", r.Header.Get("Host"))
	r.Host = target.Host

	proxy.ServeHTTP(w, r)
}
