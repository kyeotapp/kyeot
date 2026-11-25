package server_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/kyeotapp/kyeot/internal/decktest"
	"github.com/kyeotapp/kyeot/internal/protocol"
)

func TestPairAgentFromAnotherComputer(t *testing.T) {
	h := decktest.New(t)
	c := h.ConnectClient()
	created := h.CreateAgent("Other Mac")
	id := created.Agent.ID

	var issued struct{ Code string }
	if s := h.API("POST", "/api/agents/"+id+"/pair", nil, &issued); s != http.StatusOK || len(issued.Code) != 6 {
		t.Fatalf("issue: %d", s)
	}
	if s := h.APIAs("not-the-owner-token-000000", "POST", "/api/agents/"+id+"/pair", nil, nil); s != http.StatusUnauthorized {
		t.Fatalf("non-owner issue: %d", s)
	}

	pair := func(code string, header bool) (int, string) {
		req, _ := http.NewRequest("POST", h.URL+"/pair/agent", strings.NewReader(`{"code":"`+code+`"}`))
		req.Header.Set("Origin", "https://page.example") // pretend to be a browser page; programs (no Origin) work without the header
		if header {
			req.Header.Set("X-Kyeot-Pair", "1")
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var out struct{ Token string }
		json.NewDecoder(res.Body).Decode(&out)
		return res.StatusCode, out.Token
	}
	if s, _ := pair(issued.Code, false); s != http.StatusBadRequest {
		t.Fatalf("browser without the header should be refused: %d", s)
	}
	s, token := pair(issued.Code, true)
	if s != http.StatusOK || token == "" || token == created.Token {
		t.Fatalf("exchange: %d", s)
	}
	if s, _ := pair(issued.Code, true); s != http.StatusUnauthorized {
		t.Fatalf("reuse: %d", s)
	}
	got := struct{ Token string }{token}

	h.StartFakeAgent(got.Token)
	h.WaitFor(c, "paired agent connects", func(m protocol.ClientMessage) bool {
		return m.Type == "agent" && m.Agent.ID == id && m.Agent.State == protocol.AgentIdle
	})
}
