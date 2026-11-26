package shim

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

const testAccessToken = "e2e-access-token-00000000000000000000000000000000"

// reached is the handler behind the gate in these tests.
var reached = http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
	writer.WriteHeader(http.StatusTeapot)
})

// serveGated sends one request through the bearer gate and transport checks.
func serveGated(settings HTTPSettings, request *http.Request) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	bearerGate{settings: settings, next: transportChecks{settings: settings, next: reached}}.ServeHTTP(recorder, request)
	return recorder
}

// newPost builds a JSON POST to the MCP path.
func newPost(host, authorization string) *http.Request {
	request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8765/mcp", strings.NewReader("{}"))
	request.Host = host
	request.Header.Set("Content-Type", "application/json")
	if authorization != "" {
		request.Header.Set("Authorization", authorization)
	}
	return request
}

func TestHTTPGateRejectsRequestsWithoutTheAccessToken(t *testing.T) {
	settings := HTTPSettings{Port: 8765, AccessToken: testAccessToken}
	for _, authorization := range []string{"", "Bearer wrong-token-00000000000000000000000000000000", "Basic " + testAccessToken, "Bearer"} {
		response := serveGated(settings, newPost("127.0.0.1:8765", authorization))
		if response.Code != http.StatusUnauthorized || response.Body.String() != "Unauthorized" || response.Header().Get("WWW-Authenticate") != "Bearer" {
			t.Errorf("authorization %q: %d %q", authorization, response.Code, response.Body.String())
		}
	}
}

func TestHTTPGatePassesTheAccessTokenWithAnySchemeCase(t *testing.T) {
	settings := HTTPSettings{Port: 8765, AccessToken: testAccessToken}
	for _, authorization := range []string{"Bearer " + testAccessToken, "bearer " + testAccessToken, "BEARER  " + testAccessToken + " "} {
		if response := serveGated(settings, newPost("127.0.0.1:8765", authorization)); response.Code != http.StatusTeapot {
			t.Errorf("authorization %q: %d", authorization, response.Code)
		}
	}
}

func TestHTTPGateAllowsOnlyLoopbackAndTheTunnelHost(t *testing.T) {
	settings := HTTPSettings{Port: 8765, AccessToken: testAccessToken, PublicHost: "bridge.example.com"}
	authorization := "Bearer " + testAccessToken
	cases := map[string]int{
		"127.0.0.1:8765":          http.StatusTeapot,
		"localhost:8765":          http.StatusTeapot,
		"bridge.example.com":      http.StatusTeapot,
		"bridge.example.com:443":  http.StatusTeapot,
		"attacker.example.net":    http.StatusMisdirectedRequest,
		"127.0.0.1":               http.StatusMisdirectedRequest,
		"bridge.example.com.evil": http.StatusMisdirectedRequest,
		"localhost:evil.com":      http.StatusMisdirectedRequest,
		"127.0.0.1:0":             http.StatusMisdirectedRequest,
		"127.0.0.1:99999":         http.StatusMisdirectedRequest,
		"bridge.example.com:evil": http.StatusMisdirectedRequest,
	}
	for host, want := range cases {
		if response := serveGated(settings, newPost(host, authorization)); response.Code != want {
			t.Errorf("host %q: %d, want %d", host, response.Code, want)
		}
	}
	origins := map[string]int{
		"https://bridge.example.com":     http.StatusTeapot,
		"http://localhost:3000":          http.StatusTeapot,
		"http://127.0.0.1:8765":          http.StatusTeapot,
		"https://evil.example":           http.StatusForbidden,
		"http://localhost:80@evil.com":   http.StatusForbidden,
		"http://localhost:3000/extra":    http.StatusForbidden,
		"http://localhost:3000?q=1":      http.StatusForbidden,
		"http://localhost:evil":          http.StatusForbidden,
		"https://localhost:3000":         http.StatusForbidden,
	}
	for origin, want := range origins {
		request := newPost("127.0.0.1:8765", authorization)
		request.Header.Set("Origin", origin)
		if response := serveGated(settings, request); response.Code != want {
			t.Errorf("origin %q: %d, want %d", origin, response.Code, want)
		}
	}
	for _, contentType := range []string{"text/plain", "application/json-evil", ""} {
		request := newPost("127.0.0.1:8765", authorization)
		request.Header.Set("Content-Type", contentType)
		if response := serveGated(settings, request); response.Code != http.StatusBadRequest {
			t.Errorf("content-type %q POST: %d, want 400", contentType, response.Code)
		}
	}
	request := newPost("127.0.0.1:8765", authorization)
	request.Header.Set("Content-Type", "APPLICATION/JSON; charset=utf-8")
	if response := serveGated(settings, request); response.Code != http.StatusTeapot {
		t.Errorf("application/json with charset POST: %d, want 418", response.Code)
	}
}

func TestReadHTTPSettingsRejectsAWeakAccessToken(t *testing.T) {
	for _, token := range []string{"", "short", strings.Repeat("é", 32)} {
		if _, err := ReadHTTPSettings(8765, "", false, token); err == nil || !strings.Contains(err.Error(), AccessTokenVariable) {
			t.Errorf("token %q: err = %v", token, err)
		}
	}
	settings, err := ReadHTTPSettings(8765, "bridge.example.com", true, testAccessToken)
	if err != nil || settings != (HTTPSettings{8765, testAccessToken, "bridge.example.com"}) {
		t.Errorf("settings %+v, err %v", settings, err)
	}
}

func TestReadHTTPSettingsRejectsInvalidOptions(t *testing.T) {
	cases := []struct {
		port int
		host string
		set  bool
	}{
		{0, "", false}, {65536, "", false},
		{8765, "https://bridge.example.com", true}, {8765, "bridge.example.com:443", true},
		{8765, "bridge.example.com/mcp", true}, {8765, "*", true}, {8765, "", true},
	}
	for _, test := range cases {
		if _, err := ReadHTTPSettings(test.port, test.host, test.set, testAccessToken); err == nil || !strings.Contains(err.Error(), "--http") {
			t.Errorf("%+v: err = %v", test, err)
		}
	}
}

func TestHTTPGateComparesInConstantTime(t *testing.T) {
	// The gate uses crypto/subtle; a token that shares a prefix is still refused.
	settings := HTTPSettings{AccessToken: testAccessToken}
	if (bearerGate{settings: settings}).authorized("Bearer " + testAccessToken[:len(testAccessToken)-1]) {
		t.Error("a prefix of the token was accepted")
	}
}

func TestHTTPBindsLoopbackOnly(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- ServeHTTP(ctx, newTestServer(t, &fakeCore{}), HTTPSettings{Port: port, AccessToken: testAccessToken})
	}()
	deadline := time.Now().Add(5 * time.Second)
	var connection net.Conn
	for time.Now().Before(deadline) {
		if connection, err = net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port))); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if connection == nil {
		t.Fatalf("no listener on 127.0.0.1:%d: %v", port, err)
	}
	local := connection.RemoteAddr().(*net.TCPAddr)
	connection.Close()
	if !local.IP.IsLoopback() {
		t.Errorf("listening on %v", local)
	}
	cancel()
	if err := <-done; err != nil {
		t.Errorf("ServeHTTP: %v", err)
	}
}
