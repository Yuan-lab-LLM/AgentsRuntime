package llmconfig

// ContextFor resolves a physical/provider-qualified model before its bare name.
// An explicit zero means unknown and must not inherit another model's limit.
func (s Settings) ContextFor(provider, model string) int {
	if value, ok := s.ContextWindows[provider+"/"+model]; ok {
		return value
	}
	if value, ok := s.ContextWindows[model]; ok {
		return value
	}
	return s.ContextWindow
}

func cloneContextWindows(values map[string]int) map[string]int {
	if values == nil {
		return nil
	}
	result := make(map[string]int, len(values))
	for key, value := range values {
		result[key] = value
	}
	return result
}
