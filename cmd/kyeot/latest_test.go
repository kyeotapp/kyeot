package main

import (
	"slices"
	"testing"

	"github.com/kyeotapp/kyeot/internal/server"
)

func TestLatestURLOff(t *testing.T) {
	for v, want := range map[string]string{"": server.DefaultLatestURL, "on": server.DefaultLatestURL, "off": "", "0": "", "FALSE": "", "no": ""} {
		t.Setenv("KYEOT_UPDATE_CHECK", v)
		if got := latestURL(); got != want {
			t.Errorf("KYEOT_UPDATE_CHECK=%q: %q, want %q", v, got, want)
		}
	}
}

func TestServiceKeepsUpdateCheck(t *testing.T) {
	if !slices.Contains(serverSpec.env, "KYEOT_UPDATE_CHECK") {
		t.Fatal("KYEOT_UPDATE_CHECK=off would be lost when the service restarts")
	}
}
