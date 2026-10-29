package openclaw

import (
	"strconv"
	"strings"
)

const OpenClaw81Version = "2026.8.1"

func IsOpenClawAtLeast(version string, required string) bool {
	current := numericVersion(version)
	want := numericVersion(required)
	for index := 0; index < len(current) || index < len(want); index++ {
		var left, right int
		if index < len(current) {
			left = current[index]
		}
		if index < len(want) {
			right = want[index]
		}
		if left != right {
			return left > right
		}
	}
	return len(current) > 0
}

func numericVersion(raw string) []int {
	raw = strings.TrimSpace(strings.TrimPrefix(strings.ToLower(raw), "v"))
	parts := strings.FieldsFunc(raw, func(r rune) bool { return r == '.' || r == '-' || r == '+' })
	values := make([]int, 0, 3)
	for _, part := range parts {
		value, err := strconv.Atoi(part)
		if err != nil {
			break
		}
		values = append(values, value)
		if len(values) == 3 {
			break
		}
	}
	return values
}
