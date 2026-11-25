// Package push sends notifications to the phone while the app is closed. Currently APNs (iOS) only.
package push

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"
)

// ErrBadToken means the device token is no longer valid. The receiver deletes the token.
var ErrBadToken = errors.New("push: device token is no longer valid")

// ErrWrongEnv means the stored environment (sandbox/production) was wrong and it was sent to the other one. The notification was delivered.
var ErrWrongEnv = errors.New("push: device env is wrong, delivered via the other env")

const (
	EnvSandbox    = "sandbox"
	EnvProduction = "production"
)

type Device struct {
	Token string
	Env   string
}

type Notice struct {
	Title    string
	Body     string
	TaskID   string
	AgentID  string
	ServerID string
	RoomID   string // room report notification; tapping opens that room's report inbox
}

type Sender interface {
	Send(ctx context.Context, d Device, n Notice) error
}

type Config struct {
	KeyPEM []byte
	KeyID  string
	TeamID string
	Topic  string
}

// APNs sends to Apple's push server using token auth (.p8 key, ES256 JWT).
type APNs struct {
	keyID, teamID, topic string
	key                  *ecdsa.PrivateKey

	// swapped for a fake server in tests
	Client     *http.Client
	Sandbox    string
	Production string
	Now        func() time.Time
	Backoff    []time.Duration // retry intervals for transient errors; the length is the retry count

	mu    sync.Mutex
	jwt   string
	jwtAt time.Time
}

const jwtLifetime = 50 * time.Minute

func NewAPNs(c Config) (*APNs, error) {
	key, err := parseKey(c.KeyPEM)
	if err != nil {
		return nil, err
	}
	if c.KeyID == "" || c.TeamID == "" || c.Topic == "" {
		return nil, errors.New("push: key id, team id, topic are required")
	}
	return &APNs{
		keyID: c.KeyID, teamID: c.TeamID, topic: c.Topic, key: key,
		Client:     &http.Client{Timeout: 15 * time.Second},
		Sandbox:    "https://api.sandbox.push.apple.com",
		Production: "https://api.push.apple.com",
		Now:        time.Now,
		Backoff:    []time.Duration{500 * time.Millisecond, 2 * time.Second},
	}, nil
}

func parseKey(data []byte) (*ecdsa.PrivateKey, error) {
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("push: key file is not PEM")
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, errors.New("push: key is not PKCS8")
	}
	ec, ok := k.(*ecdsa.PrivateKey)
	if !ok || ec.Curve.Params().BitSize != 256 {
		return nil, errors.New("push: key is not EC P-256")
	}
	return ec, nil
}

// token returns a freshly signed JWT once 50 minutes of wall-clock time have passed.
func (a *APNs) token() (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.Now().Round(0) // the monotonic clock stops during sleep
	if age := now.Sub(a.jwtAt); a.jwt != "" && age >= 0 && age < jwtLifetime {
		return a.jwt, nil
	}
	enc := base64.RawURLEncoding
	head, _ := json.Marshal(map[string]string{"alg": "ES256", "kid": a.keyID})
	claims, _ := json.Marshal(map[string]any{"iss": a.teamID, "iat": now.Unix()})
	signing := enc.EncodeToString(head) + "." + enc.EncodeToString(claims)
	sum := sha256.Sum256([]byte(signing))
	r, s, err := ecdsa.Sign(rand.Reader, a.key, sum[:])
	if err != nil {
		return "", err
	}
	sig := make([]byte, 64) // r||s fixed length (JWS), not ASN.1
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	a.jwt, a.jwtAt = signing+"."+enc.EncodeToString(sig), now
	return a.jwt, nil
}

// dropToken discards a JWT Apple rejected. If another request has made a new one meanwhile, it is kept.
func (a *APNs) dropToken(jwt string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.jwt == jwt {
		a.jwt = ""
	}
}

type aps struct {
	Alert    alert  `json:"alert"`
	Sound    string `json:"sound"`
	ThreadID string `json:"thread-id,omitempty"`
}

type alert struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

type payload struct {
	APS    aps    `json:"aps"`
	Task   string `json:"task,omitempty"`
	Agent  string `json:"agent,omitempty"`
	Server string `json:"server,omitempty"`
	Room   string `json:"room,omitempty"`
}

// Send sends to one device. It returns ErrBadToken only when the device token is definitely invalid.
func (a *APNs) Send(ctx context.Context, d Device, n Notice) error {
	body, _ := json.Marshal(payload{APS: aps{Alert: alert{n.Title, n.Body}, Sound: "default", ThreadID: n.TaskID}, Task: n.TaskID, Agent: n.AgentID, Server: n.ServerID, Room: n.RoomID})
	status, reason, err := a.sendRetry(ctx, a.base(d.Env), d.Token, body)
	if err == nil && reason == "BadDeviceToken" {
		// a dev token sent to the production server (or vice versa) gets the same answer, so try the other environment once more
		other := EnvProduction
		if d.Env == EnvProduction {
			other = EnvSandbox
		}
		var s2 int
		var r2 string
		s2, r2, err = a.sendRetry(ctx, a.base(other), d.Token, body)
		if err == nil && s2 == http.StatusOK {
			return fmt.Errorf("%w (%s)", ErrWrongEnv, other)
		}
		if err == nil && r2 != "BadDeviceToken" {
			status, reason = s2, r2
		}
	}
	switch {
	case err != nil:
		return err
	case status == http.StatusOK:
		return nil
	case status == http.StatusGone || reason == "BadDeviceToken" || reason == "Unregistered":
		return fmt.Errorf("%w (%d %s)", ErrBadToken, status, reason)
	}
	return errors.New("push: apns " + strconv.Itoa(status) + " " + reason)
}

func (a *APNs) base(env string) string {
	if env == EnvProduction {
		return a.Production
	}
	return a.Sandbox
}

// sendRetry re-signs once if the JWT is rejected, and retries 429, 5xx, and network errors per Backoff.
func (a *APNs) sendRetry(ctx context.Context, base, token string, body []byte) (status int, reason string, err error) {
	resigned := false
	for try := 0; ; try++ {
		var jwt string
		if jwt, err = a.token(); err != nil {
			return 0, "", err
		}
		status, reason, err = a.post(ctx, base, token, jwt, body)
		if err == nil && status == http.StatusForbidden && (reason == "ExpiredProviderToken" || reason == "InvalidProviderToken") && !resigned {
			a.dropToken(jwt)
			resigned = true
			try--
			continue
		}
		transient := err != nil || status == http.StatusTooManyRequests || status >= 500
		if !transient || try >= len(a.Backoff) || ctx.Err() != nil {
			return status, reason, err
		}
		select {
		case <-ctx.Done():
			return status, reason, err
		case <-time.After(a.Backoff[try]):
		}
	}
}

func (a *APNs) post(ctx context.Context, base, token, jwt string, body []byte) (int, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/3/device/"+token, bytes.NewReader(body))
	if err != nil {
		return 0, "", err
	}
	req.Header.Set("authorization", "bearer "+jwt)
	req.Header.Set("apns-topic", a.topic)
	req.Header.Set("apns-push-type", "alert")
	req.Header.Set("apns-priority", "10")
	req.Header.Set("content-type", "application/json")
	res, err := a.Client.Do(req)
	if err != nil {
		if ue, ok := errors.AsType[*url.Error](err); ok { // the address contains the device token
			err = ue.Err
		}
		return 0, "", fmt.Errorf("push: %w", err)
	}
	defer res.Body.Close()
	var reply struct {
		Reason string `json:"reason"`
	}
	if res.StatusCode != http.StatusOK {
		json.NewDecoder(res.Body).Decode(&reply)
	}
	return res.StatusCode, reply.Reason, nil
}
