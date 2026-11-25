package protocol

import (
	"os"
	"slices"
	"strings"
	"testing"
)

func TestGuideCoversEveryAgentMessage(t *testing.T) {
	const path = "../../docs/connect-your-agent.md"
	doc, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var missing []string
	for typ := range required {
		if !strings.Contains(string(doc), "`"+typ+"`") {
			missing = append(missing, typ)
		}
	}
	slices.Sort(missing)
	if len(missing) > 0 {
		t.Fatalf("%s: messages not found: %v", path, missing)
	}
}
