package server

import (
	"encoding/json"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kyeotapp/kyeot/internal/protocol"
)

func TestSignedFileURLExpires(t *testing.T) {
	s, err := New(Options{DBPath: ":memory:", OwnerToken: "owner-token-0123456789"})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	at := func(exp int64, sig string) bool {
		r := httptest.NewRequest("GET", "/api/uploads/f1?exp="+strconv.FormatInt(exp, 10)+"&sig="+sig, nil)
		return s.signedOK(r, "f1")
	}
	future, past := time.Now().Add(time.Hour).Unix(), time.Now().Add(-time.Second).Unix()
	if !at(future, s.sign("f1", future)) {
		t.Fatal("fresh url rejected")
	}
	if at(past, s.sign("f1", past)) {
		t.Fatal("expired url accepted")
	}
	if at(future+3600, s.sign("f1", future)) {
		t.Fatal("exp bumped without re-signing accepted")
	}
	if at(future, s.sign("f2", future)) {
		t.Fatal("other file's sig accepted")
	}
}

func TestSignEventReplacesStoredURL(t *testing.T) {
	s, err := New(Options{DBPath: ":memory:", OwnerToken: "owner-token-0123456789"})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	e := protocol.Event{Payload: json.RawMessage(`{"text":"photo","attachments":[{"id":"f1","name":"a.png","mime":"image/png","size":1,"url":"/api/uploads/f1?sig=old"}]}`)}
	var out struct {
		Text        string
		Attachments []protocol.Attachment
	}
	json.Unmarshal(s.signEvent(e).Payload, &out)
	if out.Text != "photo" || len(out.Attachments) != 1 || !strings.Contains(out.Attachments[0].URL, "?exp=") {
		t.Fatalf("resigned: %+v", out)
	}
	r := httptest.NewRequest("GET", out.Attachments[0].URL, nil)
	if !s.signedOK(r, "f1") {
		t.Fatal("resigned url not accepted")
	}
}
