package router

import (
	"github.com/abhishekjha17/intern/internal/classifier"
	"github.com/abhishekjha17/intern/internal/models"
)

type RouteDecision string

const (
	RouteLocal RouteDecision = "LOCAL"
	RouteCloud RouteDecision = "CLOUD"
)

type Router struct {
	classifier *classifier.Classifier
}

func New(c *classifier.Classifier) *Router {
	return &Router{classifier: c}
}

func (r *Router) Decide(req models.AnthropicRequest) RouteDecision {
	decision := r.classifier.Classify(req)
	if decision == "LOCAL" {
		return RouteLocal
	}
	return RouteCloud
}
