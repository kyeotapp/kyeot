package hermesbridge

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLocalOnlyBlocksBrowsersAndNonJSON(t *testing.T) {
	ok := localOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	cases := []struct {
		name, method, host, origin, ctype string
		want                              int
	}{
		{"agent curl", "POST", "127.0.0.1:7701", "", "application/json", http.StatusOK},
		{"agent get", "GET", "localhost:7701", "", "", http.StatusOK},
		{"web page", "POST", "127.0.0.1:7701", "https://evil.example", "application/json", http.StatusForbidden},
		{"text/plain form", "POST", "127.0.0.1:7701", "", "text/plain", http.StatusUnsupportedMediaType},
		{"dns rebinding", "GET", "evil.example:7701", "", "", http.StatusForbidden},
	}
	for _, c := range cases {
		req := httptest.NewRequest(c.method, "http://"+c.host+"/ui", strings.NewReader("{}"))
		req.Host = c.host
		if c.origin != "" {
			req.Header.Set("Origin", c.origin)
		}
		if c.ctype != "" {
			req.Header.Set("Content-Type", c.ctype)
		}
		rec := httptest.NewRecorder()
		ok.ServeHTTP(rec, req)
		if rec.Code != c.want {
			t.Errorf("%s: %d, want %d", c.name, rec.Code, c.want)
		}
	}
}
