package server

import (
	"slices"
	"strings"
	"testing"
)

// If server sensor names are dropped from hello, that feature disappears from the screen. App-side names are checked by scripts/check-contract.mjs.
func TestSensorFeaturesAreAgentNames(t *testing.T) {
	for _, f := range Features {
		if strings.HasPrefix(f, "sensor.") && !slices.Contains(agentFeatureNames, f) {
			t.Errorf("name the server drops: %s (add it to agentFeatureNames)", f)
		}
	}
}
