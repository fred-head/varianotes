package app

import (
	"strings"
	"testing"
)

func TestSanitizeLogValueRemovesControlCharactersAndLimitsLength(t *testing.T) {
	input := "safe\r\nforged\t\x1b[31m" + strings.Repeat("a", 1100)
	got := sanitizeLogValue(input)

	if strings.ContainsAny(got, "\r\n\t\x1b") {
		t.Fatalf("sanitized log value still contains control characters: %q", got)
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("expected oversized log value to be visibly truncated")
	}
	if count := len([]rune(strings.TrimSuffix(got, "…"))); count != 1024 {
		t.Fatalf("expected 1024 retained runes, got %d", count)
	}
}
