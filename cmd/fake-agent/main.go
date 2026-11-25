package main

import (
	"context"
	"encoding/json"
	"log"
	"os"

	"github.com/kyeotapp/kyeot/internal/fakeagent"
)

func main() {
	token := os.Getenv("AGENT_TOKEN")
	if token == "" {
		log.Fatal("AGENT_TOKEN is required")
	}
	url := deckURL()
	if url == "" {
		url = "http://127.0.0.1:7700"
	}
	ctx := context.Background()
	agent, err := fakeagent.Dial(ctx, url, token)
	if err != nil {
		log.Fatal(err)
	}
	agent.Demo = os.Getenv("FAKE_AGENT_DEMO") == "1"
	if raw := os.Getenv("FAKE_AGENT_DATA"); raw != "" { // {"bundle name": {...}}
		if err := json.Unmarshal([]byte(raw), &agent.Bundles); err != nil {
			log.Fatal("FAKE_AGENT_DATA: ", err)
		}
	}
	log.Print("fake agent connected")
	log.Print("disconnected: ", agent.Run(ctx))
}

// deckURL reads KYEOT_URL first, then the old name AGENT_DECK_URL.
func deckURL() string {
	if v := os.Getenv("KYEOT_URL"); v != "" {
		return v
	}
	return os.Getenv("AGENT_DECK_URL")
}
