package shim

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// HTTP bridge settings, from the former Python core's mcp/http_transport.py.
const (
	AccessTokenVariable  = "AGENTCY_HTTP_TOKEN"
	minAccessTokenLength = 32
	loopbackHost         = "127.0.0.1"
	unauthorizedBody     = "Unauthorized"
	// maxRequestBody is the Python SDK's HTTP request body limit (4 MiB).
	maxRequestBody      = 4 * 1024 * 1024
	readHeaderTimeout   = 10 * time.Second
	httpShutdownTimeout = 5 * time.Second
	mcpPath             = "/mcp"
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
	if request.Method == http.MethodPost && !isJSONContentType(request.Header.Get("Content-Type")) {
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

// isJSONContentType reports whether header names the application/json media type.
func isJSONContentType(header string) bool {
	mediaType, _, err := mime.ParseMediaType(header)
	return err == nil && mediaType == "application/json"
}

// authorized compares SHA-256 digests of the bearer credential and access token in
// constant time so that token length is not leaked.
func (checks bearerGate) authorized(header string) bool {
	scheme, credential, _ := strings.Cut(header, " ")
	if !strings.EqualFold(scheme, "bearer") {
		return false
	}
	given := strings.Trim(credential, " \t\n\r\v\f")
	givenDigest := sha256.Sum256([]byte(given))
	expectedDigest := sha256.Sum256([]byte(checks.settings.AccessToken))
	return subtle.ConstantTimeCompare(givenDigest[:], expectedDigest[:]) == 1
}

// validPort reports a canonical decimal TCP port in 1..65535.
func validPort(portText string) bool {
	port, err := strconv.Atoi(portText)
	return err == nil && port >= 1 && port <= 65535 && strconv.Itoa(port) == portText
}

// allowedHost accepts the loopback names with a valid port and the tunnel host with or
// without a valid port, as the Python transport security settings do.
func allowedHost(settings HTTPSettings, host string) bool {
	if host == "" {
		return false
	}
	if settings.PublicHost != "" && host == settings.PublicHost {
		return true
	}
	name, port, err := net.SplitHostPort(host)
	if err != nil || !validPort(port) {
		return false
	}
	return name == loopbackHost || name == "localhost" || (settings.PublicHost != "" && name == settings.PublicHost)
}

// allowedOrigin accepts http origins on the loopback names with a valid port and the
// https tunnel origin.
func allowedOrigin(settings HTTPSettings, origin string) bool {
	if settings.PublicHost != "" && origin == "https://"+settings.PublicHost {
		return true
	}
	parsed, err := url.Parse(origin)
	if err != nil {
		return false
	}
	if parsed.Scheme != "http" || parsed.User != nil || parsed.Path != "" ||
		parsed.RawQuery != "" || parsed.Fragment != "" || parsed.ForceQuery {
		return false
	}
	name, port, err := net.SplitHostPort(parsed.Host)
	if err != nil || !validPort(port) {
		return false
	}
	return name == loopbackHost || name == "localhost"
}

// eraRouter sends 2026-07-28 requests to a stateless handler and older ones to a
// stateful one. The SDK serves 2026-07-28 over HTTP only statelessly, while a 2025-11-25
// elicitation needs the session of a stateful handler. Every 2026-07-28 request carries
// the MCP-Protocol-Version header; routesToModernHandler decides on it.
func eraRouter(server *mcp.Server) http.Handler {
	serverFor := func(*http.Request) *mcp.Server { return server }
	stateful := mcp.NewStreamableHTTPHandler(serverFor, &mcp.StreamableHTTPOptions{DisableLocalhostProtection: true})
	stateless := mcp.NewStreamableHTTPHandler(serverFor, &mcp.StreamableHTTPOptions{Stateless: true, DisableLocalhostProtection: true})
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if routesToModernHandler(request.Header.Get("MCP-Protocol-Version")) {
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
	serveDone := make(chan struct{})
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		select {
		case <-ctx.Done():
			shutdownCtx, cancel := context.WithTimeout(context.Background(), httpShutdownTimeout)
			defer cancel()
			if err := httpServer.Shutdown(shutdownCtx); err != nil {
				_ = httpServer.Close()
			}
		case <-serveDone:
		}
	}()
	err = httpServer.Serve(listener)
	close(serveDone)
	<-shutdownDone
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
