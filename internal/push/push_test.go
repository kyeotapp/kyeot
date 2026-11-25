package push

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type seen struct {
	proto, path, auth, topic, pushType, priority string
	body                                         []byte
}

func fakeAPNs(t *testing.T, status int, reason string) (*httptest.Server, chan seen) {
	t.Helper()
	got := make(chan seen, 4)
	ts := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		got <- seen{r.Proto, r.URL.Path, r.Header.Get("authorization"), r.Header.Get("apns-topic"), r.Header.Get("apns-push-type"), r.Header.Get("apns-priority"), body}
		w.WriteHeader(status)
		if reason != "" {
			json.NewEncoder(w).Encode(map[string]string{"reason": reason})
		}
	}))
	ts.EnableHTTP2 = true
	ts.StartTLS()
	t.Cleanup(ts.Close)
	return ts, got
}

func newKey(t *testing.T) (*ecdsa.PrivateKey, []byte) {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, _ := x509.MarshalPKCS8PrivateKey(k)
	return k, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
}

func newSender(t *testing.T, ts *httptest.Server, keyPEM []byte) *APNs {
	t.Helper()
	a, err := NewAPNs(Config{KeyPEM: keyPEM, KeyID: "KEY123", TeamID: "TEAM456", Topic: "com.example.kyeot"})
	if err != nil {
		t.Fatal(err)
	}
	a.Client = ts.Client()
	a.Sandbox = ts.URL
	a.Production = ts.URL + "/prod"
	a.Backoff = []time.Duration{time.Millisecond, time.Millisecond}
	return a
}

const devToken = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestSendHeadersPayloadAndSignature(t *testing.T) {
	ts, got := fakeAPNs(t, http.StatusOK, "")
	key, keyPEM := newKey(t)
	a := newSender(t, ts, keyPEM)

	err := a.Send(context.Background(), Device{Token: devToken, Env: EnvSandbox}, Notice{Title: "Agent A", Body: "Done", TaskID: "t1", AgentID: "a1", ServerID: "s1"})
	if err != nil {
		t.Fatal(err)
	}
	s := <-got
	if s.proto != "HTTP/2.0" {
		t.Fatalf("proto %s", s.proto)
	}
	if s.path != "/3/device/"+devToken {
		t.Fatalf("path %s", s.path)
	}
	if s.topic != "com.example.kyeot" || s.pushType != "alert" || s.priority != "10" {
		t.Fatalf("headers %+v", s)
	}
	var p map[string]any
	if err := json.Unmarshal(s.body, &p); err != nil {
		t.Fatal(err)
	}
	want := `{"agent":"a1","aps":{"alert":{"body":"Done","title":"Agent A"},"sound":"default","thread-id":"t1"},"server":"s1","task":"t1"}`
	if b, _ := json.Marshal(p); string(b) != want {
		t.Fatalf("payload %s", b)
	}

	jwt, ok := strings.CutPrefix(s.auth, "bearer ")
	if !ok {
		t.Fatalf("authorization %q", s.auth)
	}
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		t.Fatalf("jwt parts %d", len(parts))
	}
	dec := base64.RawURLEncoding
	var head, claims map[string]any
	h, _ := dec.DecodeString(parts[0])
	c, _ := dec.DecodeString(parts[1])
	json.Unmarshal(h, &head)
	json.Unmarshal(c, &claims)
	if head["alg"] != "ES256" || head["kid"] != "KEY123" || claims["iss"] != "TEAM456" || claims["iat"] == nil {
		t.Fatalf("jwt head %v claims %v", head, claims)
	}
	sig, _ := dec.DecodeString(parts[2])
	if len(sig) != 64 {
		t.Fatalf("signature must be raw r||s 64 bytes, got %d", len(sig))
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	r, ss := new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])
	if !ecdsa.Verify(&key.PublicKey, sum[:], r, ss) {
		t.Fatal("jwt signature does not verify with the key's public key")
	}
}

func TestProductionEnvUsesProductionHost(t *testing.T) {
	ts, got := fakeAPNs(t, http.StatusOK, "")
	_, keyPEM := newKey(t)
	a := newSender(t, ts, keyPEM)
	if err := a.Send(context.Background(), Device{Token: devToken, Env: EnvProduction}, Notice{}); err != nil {
		t.Fatal(err)
	}
	if s := <-got; s.path != "/prod/3/device/"+devToken {
		t.Fatalf("path %s", s.path)
	}
}

func TestJWTIsCachedFor50Minutes(t *testing.T) {
	ts, got := fakeAPNs(t, http.StatusOK, "")
	_, keyPEM := newKey(t)
	a := newSender(t, ts, keyPEM)
	now := time.Unix(1_800_000_000, 0)
	a.Now = func() time.Time { return now }
	send := func() string {
		if err := a.Send(context.Background(), Device{Token: devToken}, Notice{}); err != nil {
			t.Fatal(err)
		}
		return (<-got).auth
	}
	first := send()
	now = now.Add(49 * time.Minute)
	if send() != first {
		t.Fatal("jwt changed before 50 minutes")
	}
	now = now.Add(2 * time.Minute)
	if send() == first {
		t.Fatal("jwt not renewed after 50 minutes")
	}
}

func TestInvalidTokenIsDistinguished(t *testing.T) {
	cases := []struct {
		status int
		reason string
		bad    bool
	}{
		{http.StatusGone, "Unregistered", true},
		{http.StatusGone, "", true},
		{http.StatusBadRequest, "BadDeviceToken", true},
		{http.StatusBadRequest, "BadTopic", false},
		{http.StatusTooManyRequests, "TooManyRequests", false},
	}
	for _, c := range cases {
		ts, got := fakeAPNs(t, c.status, c.reason)
		_, keyPEM := newKey(t)
		a := newSender(t, ts, keyPEM)
		err := a.Send(context.Background(), Device{Token: devToken}, Notice{})
		<-got
		if err == nil {
			t.Fatalf("%d %s: no error", c.status, c.reason)
		}
		if errors.Is(err, ErrBadToken) != c.bad {
			t.Fatalf("%d %s: bad token=%v, want %v (%v)", c.status, c.reason, !c.bad, c.bad, err)
		}
		if strings.Contains(err.Error(), devToken) {
			t.Fatal("error leaks device token")
		}
	}
}

func TestRejectsNonP256Key(t *testing.T) {
	k, _ := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	der, _ := x509.MarshalPKCS8PrivateKey(k)
	_, err := NewAPNs(Config{KeyPEM: pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), KeyID: "k", TeamID: "t", Topic: "x"})
	if err == nil {
		t.Fatal("P-384 key accepted")
	}
	if _, err := NewAPNs(Config{KeyPEM: []byte("garbage"), KeyID: "k", TeamID: "t", Topic: "x"}); err == nil {
		t.Fatal("garbage key accepted")
	}
}

type reply struct {
	status int
	reason string
}

// seqAPNs answers each request with the next of replies. When they run out, it repeats the last one.
func seqAPNs(t *testing.T, replies ...reply) (*httptest.Server, chan seen) {
	t.Helper()
	got := make(chan seen, 16)
	n := 0
	ts := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got <- seen{path: r.URL.Path, auth: r.Header.Get("authorization")}
		rp := replies[min(n, len(replies)-1)]
		n++
		w.WriteHeader(rp.status)
		if rp.reason != "" {
			json.NewEncoder(w).Encode(map[string]string{"reason": rp.reason})
		}
	}))
	ts.EnableHTTP2 = true
	ts.StartTLS()
	t.Cleanup(ts.Close)
	return ts, got
}

func drain(got chan seen) []seen {
	var out []seen
	for {
		select {
		case s := <-got:
			out = append(out, s)
		default:
			return out
		}
	}
}

func TestExpiredProviderTokenResigns(t *testing.T) {
	for _, reason := range []string{"ExpiredProviderToken", "InvalidProviderToken"} {
		ts, got := seqAPNs(t, reply{http.StatusForbidden, reason}, reply{http.StatusOK, ""})
		_, keyPEM := newKey(t)
		a := newSender(t, ts, keyPEM)
		now := time.Unix(1_800_000_000, 0)
		a.Now = func() time.Time { now = now.Add(time.Second); return now }
		if err := a.Send(context.Background(), Device{Token: devToken}, Notice{}); err != nil {
			t.Fatalf("%s: %v", reason, err)
		}
		s := drain(got)
		if len(s) != 2 || s[0].auth == s[1].auth {
			t.Fatalf("%s: must re-sign instead of the dropped JWT and send once more: %d times", reason, len(s))
		}
	}
}

func TestExpiredProviderTokenRetriesOnce(t *testing.T) {
	ts, got := seqAPNs(t, reply{http.StatusForbidden, "ExpiredProviderToken"})
	_, keyPEM := newKey(t)
	a := newSender(t, ts, keyPEM)
	if err := a.Send(context.Background(), Device{Token: devToken}, Notice{}); err == nil || errors.Is(err, ErrBadToken) {
		t.Fatalf("err %v", err)
	}
	if n := len(drain(got)); n != 2 {
		t.Fatalf("re-sign only once: %d times", n)
	}
}

func TestTransientErrorsRetry(t *testing.T) {
	ts, got := seqAPNs(t, reply{http.StatusTooManyRequests, "TooManyRequests"}, reply{http.StatusServiceUnavailable, ""}, reply{http.StatusOK, ""})
	_, keyPEM := newKey(t)
	a := newSender(t, ts, keyPEM)
	if err := a.Send(context.Background(), Device{Token: devToken}, Notice{}); err != nil {
		t.Fatal(err)
	}
	if n := len(drain(got)); n != 3 {
		t.Fatalf("%d requests", n)
	}

	ts, got = seqAPNs(t, reply{http.StatusInternalServerError, "InternalServerError"})
	a = newSender(t, ts, keyPEM)
	if err := a.Send(context.Background(), Device{Token: devToken}, Notice{}); err == nil {
		t.Fatal("persistent 500 should fail")
	}
	if n := len(drain(got)); n != 1+len(a.Backoff) {
		t.Fatalf("%d retries", n)
	}

	ts, got = seqAPNs(t, reply{http.StatusBadRequest, "BadTopic"})
	a = newSender(t, ts, keyPEM)
	a.Send(context.Background(), Device{Token: devToken}, Notice{})
	if n := len(drain(got)); n != 1 {
		t.Fatalf("400 must not be resent: %d times", n)
	}
}

func TestNetworkErrorRetries(t *testing.T) {
	ts, got := seqAPNs(t, reply{http.StatusOK, ""})
	_, keyPEM := newKey(t)
	a := newSender(t, ts, keyPEM)
	a.Sandbox = "https://127.0.0.1:1" // closed port
	if err := a.Send(context.Background(), Device{Token: devToken}, Notice{}); err == nil || strings.Contains(err.Error(), devToken) {
		t.Fatalf("err %v", err)
	}
	if n := len(drain(got)); n != 0 {
		t.Fatalf("%d requests", n)
	}
}

func TestBadDeviceTokenTriesOtherEnv(t *testing.T) {
	ts, got := seqAPNs(t, reply{http.StatusBadRequest, "BadDeviceToken"}, reply{http.StatusOK, ""})
	_, keyPEM := newKey(t)
	a := newSender(t, ts, keyPEM)
	err := a.Send(context.Background(), Device{Token: devToken, Env: EnvSandbox}, Notice{})
	if !errors.Is(err, ErrWrongEnv) || errors.Is(err, ErrBadToken) {
		t.Fatalf("ErrWrongEnv expected when sent to the other environment: %v", err)
	}
	s := drain(got)
	if len(s) != 2 || s[0].path != "/3/device/"+devToken || s[1].path != "/prod/3/device/"+devToken {
		t.Fatalf("requests %+v", s)
	}
}

func TestWallClockGoingBackResigns(t *testing.T) {
	ts, got := fakeAPNs(t, http.StatusOK, "")
	_, keyPEM := newKey(t)
	a := newSender(t, ts, keyPEM)
	now := time.Unix(1_800_000_000, 0)
	a.Now = func() time.Time { return now }
	a.Send(context.Background(), Device{Token: devToken}, Notice{})
	first := (<-got).auth
	now = now.Add(-2 * time.Hour)
	a.Send(context.Background(), Device{Token: devToken}, Notice{})
	if (<-got).auth == first {
		t.Fatal("re-sign when the clock goes backwards")
	}
}
