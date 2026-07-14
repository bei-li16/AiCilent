package router

import "ai-proxy/internal/config"

type Matcher struct {
	routes map[string]string
}

func NewMatcher(routes []config.ModelRoute) *Matcher {
	m := &Matcher{routes: make(map[string]string)}
	for _, r := range routes {
		m.routes[r.Alias] = r.Target
	}
	return m
}

func (m *Matcher) Match(modelName string) (string, bool) {
	if target, ok := m.routes[modelName]; ok {
		return target, true
	}
	return "", false
}

func (m *Matcher) Default() (string, bool) {
	if target, ok := m.routes["default"]; ok {
		return target, true
	}
	return "", false
}