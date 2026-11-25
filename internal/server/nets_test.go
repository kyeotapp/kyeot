package server

import "testing"

func TestAllowNets(t *testing.T) {
	nets, err := ParseNets("loopback, tailscale")
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{opts: Options{AllowNets: nets}}
	for remote, want := range map[string]bool{
		"127.0.0.1:5000":       true,
		"[::1]:5000":           true,
		"100.101.102.103:5000": true,  // public-ok
		"192.168.0.7:5000":     false, // public-ok
		"8.8.8.8:5000":         false,
		"garbage":              false,
	} {
		if got := s.allowed(remote); got != want {
			t.Errorf("%s: got %v want %v", remote, got, want)
		}
	}
	if open := (&Server{}); !open.allowed("8.8.8.8:1") {
		t.Error("no AllowNets should allow everything")
	}
	if _, err := ParseNets("lan,10.1.0.0/16"); err != nil { // public-ok
		t.Fatal(err)
	}
	all, err := ParseNets("all")
	if err != nil {
		t.Fatal(err)
	}
	if s := (&Server{opts: Options{AllowNets: all}}); !s.allowed("8.8.8.8:1") || !s.allowed("[2001:db8::1]:1") {
		t.Error("all should allow every address")
	}
	if _, err := ParseNets("moon"); err == nil {
		t.Fatal("unknown name should fail")
	}
}
