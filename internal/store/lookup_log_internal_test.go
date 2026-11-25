package store

import (
	"bytes"
	"log"
	"os"
	"strings"
	"testing"
)

func TestLookupErrorsAreLogged(t *testing.T) {
	s, _ := openFile(t)
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)
	if s.OwnerName("nobody") != "" || buf.Len() != 0 {
		t.Fatalf("a missing row must not be logged: %q", buf.String())
	}
	s.Close()
	s.OwnerName(Owner)
	s.Closed()
	if !strings.Contains(buf.String(), "store OwnerName") || !strings.Contains(buf.String(), "store Closed") {
		t.Fatalf("swallowed the DB error: %q", buf.String())
	}
}
