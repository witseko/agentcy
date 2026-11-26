package shim

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// HTTP bridge settings, from src/agentcy/mcp/http_transport.py.
const (
	AccessTokenVariable  = "AGENTCY_HTTP_TOKEN"
	minAccessTokenLength = 32
	loopbackHost         = "127.0.0.1"
	unauthorizedBody     = "Unauthorized"
	// maxRequestBody is the Python SDK's HTTP request body limit (4 MiB).
	maxRequestBody    = 4 * 1024 * 1024
	readHeaderTimeout = 10 * time.Second
	mcpPath           = "/mcp"
)

// publicHostPattern accepts a host name without scheme, port or path.
var publicHostPattern = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9.-]{0,251}[A-Za-z0-9])?$`)

// HTTPSettings holds the port, the access token, and the tunnel host name of one HTTP
// bridge.
type HTTPSettings struct {
	Port        int
	AccessToken string
	PublicHost  string
}

// ReadHTTPSettings validates the HTTP options. publicHostSet tells whether
// --http-public-host was given, so that an empty value is rejected. accessToken is the
// value of
// AGENTCY_HTTP_TOKEN, which the caller has already removed from the environment so that
// neither the core nor launched agents inherit it. The error texts are the Python shim's.
func ReadHTTPSettings(port int, publicHost string, publicHostSet bool, accessToken string) (HTTPSettings, error) {
	if port < 1 || port > 65535 {
		return HTTPSettings{}, errors.New("--http needs a port from 1 to 65535")
	}
	if publicHostSet && !publicHostPattern.MatchString(publicHost) {
		return HTTPSettings{}, errors.New("--http-public-host needs a host name without scheme, port, or path")
	}
	if len(accessToken) < minAccessTokenLength || !isASCII(accessToken) {
		return HTTPSettings{}, fmt.Errorf("Set %s to a random ASCII secret of at least %d characters, for example from 'openssl rand -hex 32'",
			AccessTokenVariable, minAccessTokenLength)
	}
	return HTTPSettings{Port: port, AccessToken: accessToken, PublicHost: publicHost}, nil
}

// isASCII reports whether text holds only ASCII characters.
func isASCII(text string) bool {
	for index := 0; index < len(text); index++ {
		if text[index] >= 0x80 {
			return false
		}
	}
	return true
}

// bearerGate passes a request on only when it carries the access token, on every path,
// and answers 401 otherwise, like BearerTokenGate in the Python bridge.
type bearerGate struct {
	settings HTTPSettings
	next     http.Handler
}

// ServeHTTP answers 401 to a request without the token.
func (checks bearerGate) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if !checks.authorized(request.Header.Get("Authorization")) {
		writer.Header().Set("WWW-Authenticate", "Bearer")
		writer.Header().Set("Content-Type", "text/plain; charset=utf-8")
		writer.Header().Set("Content-Length", strconv.Itoa(len(unauthorizedBody)))
		writer.WriteHeader(http.StatusUnauthorized)
		_, _ = writer.Write([]byte(unauthorizedBody))
		return
	}
	checks.next.ServeHTTP(writer, request)
}

// transportChecks guards the MCP endpoint like the Python SDK's transport security: for
// a POST the JSON content type (400), then the Host header (421) and the Origin header
// (403) against the loopback names and the tunnel host, and a 4 MiB body limit. It
// replaces the SDK's own localhost check, which would refuse the tunnel host.
type transportChecks struct {
	settings HTTPSettings
	next     http.Handler
}

// ServeHTTP applies the checks in the order of the Python bridge.
func (checks transportChecks) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if request.Method == http.MethodPost && !strings.HasPrefix(strings.ToLower(request.Header.Get("Content-Type")), "application/json") {
		http.Error(writer, "Invalid Content-Type header", http.StatusBadRequest)
		return
	}
	if !allowedHost(checks.settings, request.Host) {
		http.Error(writer, "Invalid Host header", http.StatusMisdirectedRequest)
		return
	}
	if origin := request.Header.Get("Origin"); origin != "" && !allowedOrigin(checks.settings, origin) {
		http.Error(writer, "Invalid Origin header", http.StatusForbidden)
		return
	}
	request.Body = http.MaxBytesReader(writer, request.Body, maxRequestBody)
	checks.next.ServeHTTP(writer, request)
}

// authorized compares the bearer credential with the access token in constant time.
func (checks bearerGate) authorized(header string) bool {
	scheme, credential, _ := strings.Cut(header, " ")
	if !strings.EqualFold(scheme, "bearer") {
		return false
	}
	given := strings.Trim(credential, " \t\n\r\v\f")
	return subtle.ConstantTimeCompare([]byte(given), []byte(checks.settings.AccessToken)) == 1
}

// allowedHost accepts the loopback names with any port and the tunnel host with or
// without a port, as the Python transport security settings do.
func allowedHost(settings HTTPSettings, host string) bool {
	if host == "" {
		return false
	}
	if strings.HasPrefix(host, loopbackHost+":") || strings.HasPrefix(host, "localhost:") {
		return true
	}
	public := settings.PublicHost
	return public != "" && (host == public || strings.HasPrefix(host, public+":"))
}

// allowedOrigin accepts http origins on the loopback names and the https tunnel origin.
func allowedOrigin(settings HTTPSettings, origin string) bool {
	if strings.HasPrefix(origin, "http://"+loopbackHost+":") || strings.HasPrefix(origin, "http://localhost:") {
		return true
	}
	return settings.PublicHost != "" && origin == "https://"+settings.PublicHost
}

// eraRouter sends 2026-07-28 requests to a stateless handler and older ones to a
// stateful one. The SDK serves 2026-07-28 over HTTP only statelessly, while a 2025-11-25
// elicitation needs the session of a stateful handler. Every 2026-07-28 request carries
// the MCP-Protocol-Version header.
func eraRouter(server *mcp.Server) http.Handler {
	serverFor := func(*http.Request) *mcp.Server { return server }
	stateful := mcp.NewStreamableHTTPHandler(serverFor, &mcp.StreamableHTTPOptions{DisableLocalhostProtection: true})
	stateless := mcp.NewStreamableHTTPHandler(serverFor, &mcp.StreamableHTTPOptions{Stateless: true, DisableLocalhostProtection: true})
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("MCP-Protocol-Version") >= modernEra {
			stateless.ServeHTTP(writer, request)
			return
		}
		stateful.ServeHTTP(writer, request)
	})
}

// NewHTTPHandler returns the gated MCP handler served at /mcp.
func NewHTTPHandler(server *mcp.Server, settings HTTPSettings) http.Handler {
	mux := http.NewServeMux()
	mux.Handle(mcpPath, transportChecks{settings: settings, next: eraRouter(server)})
	return bearerGate{settings: settings, next: mux}
}

// ServeHTTP serves the tools at /mcp on the loopback interface until ctx ends.
func ServeHTTP(ctx context.Context, server *mcp.Server, settings HTTPSettings) error {
	listener, err := net.Listen("tcp", net.JoinHostPort(loopbackHost, strconv.Itoa(settings.Port)))
	if err != nil {
		return err
	}
	httpServer := &http.Server{Handler: NewHTTPHandler(server, settings), ReadHeaderTimeout: readHeaderTimeout}
	go func() {
		<-ctx.Done()
		_ = httpServer.Close()
	}()
	err = httpServer.Serve(listener)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
