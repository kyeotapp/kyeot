package hermesbridge

import (
	"reflect"
	"testing"

	"github.com/kyeotapp/kyeot/internal/protocol"
)

func TestHermesModels(t *testing.T) {
	cfg := `security:
  x: 1
delegation:
  model: big
  provider: anthropic
model:
  default: small
  provider: anthropic
auxiliary:
  compression:
    reasoning_effort: low
  title_generation:
    language: Korean
    model: tiny
  vision:
    model: "eye"
skills:
  disabled:
    - a
`
	want := []protocol.AgentModel{{Role: "chat", Name: "small"}, {Role: "delegate", Name: "big"}, {Role: "title", Name: "tiny"}, {Role: "vision", Name: "eye"}}
	if got := hermesModels(cfg); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v", got)
	}
	if got := hermesModels("model:\n  provider: x\n"); len(got) != 0 {
		t.Fatalf("no default should give nothing: %+v", got)
	}
}
