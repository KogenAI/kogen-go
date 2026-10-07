package oauth

import (
	"context"
	"crypto/subtle"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"
)

const maxCallbackQueryBytes = 4096

type callbackResult struct {
	Code     string
	ClientID string
	Denied   bool
}

type callbackWaiter func() (callbackResult, error)

type callbackOutcome struct {
	result callbackResult
	err    error
}

// bindCallback listens only on IPv4 loopback. The platform-specific control
// enables SO_REUSEADDR before bind, allowing an immediate sequential login
// after the previous accepted connection entered TCP TIME_WAIT.
func bindCallback(ctx context.Context, port int) (net.Listener, string, error) {
	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	config := net.ListenConfig{Control: setReuseAddress}
	listener, err := config.Listen(ctx, "tcp4", address)
	if err != nil {
		return nil, "", err
	}
	actualPort := listener.Addr().(*net.TCPAddr).Port
	redirectURI := "http://127.0.0.1:" + strconv.Itoa(actualPort) + CallbackPath
	return listener, redirectURI, nil
}

func startCallback(ctx context.Context, listener net.Listener, expectedState string, timeout time.Duration) (callbackWaiter, func()) {
	results := make(chan callbackOutcome, 1)
	handler := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		outcome, terminal := inspectCallback(request, expectedState)
		writeCallbackResponse(writer, outcome.err == nil && terminal && !outcome.result.Denied)
		if terminal {
			select {
			case results <- outcome:
			default:
			}
		}
	})
	server := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 2 * time.Second,
		MaxHeaderBytes:    8192,
		ErrorLog:          log.New(io.Discard, "", 0),
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}
	serveDone := make(chan struct{})
	serveError := make(chan error, 1)
	go func() {
		defer close(serveDone)
		serveError <- server.Serve(listener)
	}()

	var stopOnce sync.Once
	stop := func() {
		stopOnce.Do(func() {
			shutdownContext, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := server.Shutdown(shutdownContext); err != nil {
				_ = server.Close()
			}
			<-serveDone
		})
	}
	wait := func() (callbackResult, error) {
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		select {
		case outcome := <-results:
			stop()
			return outcome.result, outcome.err
		case <-ctx.Done():
			stop()
			return callbackResult{}, ctx.Err()
		case <-timer.C:
			stop()
			return callbackResult{}, ErrCallbackTimeout
		case <-serveDone:
			_ = <-serveError
			stop()
			return callbackResult{}, ErrInvalidCallback
		}
	}
	return wait, stop
}

func inspectCallback(request *http.Request, expectedState string) (callbackOutcome, bool) {
	invalid := callbackOutcome{err: ErrInvalidCallback}
	if request.Method != http.MethodGet || request.URL.Path != CallbackPath {
		return invalid, false
	}
	if len(request.URL.RawQuery) > maxCallbackQueryBytes {
		return invalid, true
	}
	query, err := url.ParseQuery(request.URL.RawQuery)
	if err != nil {
		return invalid, true
	}
	for _, values := range query {
		if len(values) != 1 {
			return invalid, true
		}
	}
	returnedState := query.Get("state")
	if len(returnedState) != len(expectedState) || subtle.ConstantTimeCompare([]byte(returnedState), []byte(expectedState)) != 1 {
		return callbackOutcome{err: ErrStateMismatch}, true
	}
	if errorCode := query.Get("error"); errorCode != "" {
		return callbackOutcome{result: callbackResult{Denied: true}}, true
	}
	code := query.Get("code")
	if !validOpaqueText(code, 2048) {
		return invalid, true
	}
	clientID := query.Get("client_id")
	if clientID != "" && !validClientID(clientID) {
		return invalid, true
	}
	return callbackOutcome{result: callbackResult{Code: code, ClientID: clientID}}, true
}

func writeCallbackResponse(writer http.ResponseWriter, accepted bool) {
	message := "Kogen could not verify this sign-in callback."
	if accepted {
		message = "Kogen sign-in complete. You can close this tab."
	}
	body := "<!doctype html><title>Kogen</title><p>" + message + "</p>"
	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("Content-Security-Policy", "default-src 'none'")
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	writer.Header().Set("Content-Length", strconv.Itoa(len(body)))
	writer.Header().Set("Connection", "close")
	writer.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(writer, body)
}
