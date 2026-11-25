package hermesbridge

import (
	"strings"
	"testing"
)

func TestRunInstructions(t *testing.T) {
	if !strings.Contains(runInstructions("ko"), "Korean") || !strings.Contains(runInstructions("en"), "English") {
		t.Fatalf("ko=%q en=%q", runInstructions("ko"), runInstructions("en"))
	}
	if runInstructions("fr") != "" || runInstructions("") != "" {
		t.Error("an unknown language must get no instruction")
	}
}
