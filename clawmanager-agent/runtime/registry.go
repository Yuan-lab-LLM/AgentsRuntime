package runtime

import (
	"fmt"
	"strings"

	"github.com/iamlovingit/clawmanager-agent/gateway"
)

type Registry struct {
	profiles map[string]gateway.RuntimeProfile
}

func NewRegistry() *Registry {
	return &Registry{profiles: map[string]gateway.RuntimeProfile{}}
}

func (r *Registry) Register(profile gateway.RuntimeProfile) error {
	runtimeType := normalizeRuntimeType(profile.Type())
	if runtimeType == "" {
		return fmt.Errorf("runtime profile type is empty")
	}
	if _, exists := r.profiles[runtimeType]; exists {
		return fmt.Errorf("runtime profile %q already registered", runtimeType)
	}
	r.profiles[runtimeType] = profile
	return nil
}

func (r *Registry) Get(runtimeType string) (gateway.RuntimeProfile, bool) {
	profile, ok := r.profiles[normalizeRuntimeType(runtimeType)]
	return profile, ok
}

func normalizeRuntimeType(runtimeType string) string {
	return strings.ToLower(strings.TrimSpace(runtimeType))
}
