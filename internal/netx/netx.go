package netx

import (
	"context"
	"net"
	"net/http"
	"time"

	"golang.org/x/net/proxy"
)

type DialContextFunc func(ctx context.Context, network, addr string) (net.Conn, error)

func NewSOCKS5Dialer(addr, user, password string) DialContextFunc {
	dialer := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	if addr == "" {
		return dialer.DialContext
	}

	var auth *proxy.Auth
	if user != "" || password != "" {
		auth = &proxy.Auth{User: user, Password: password}
	}
	socksDialer, err := proxy.SOCKS5("tcp", addr, auth, dialer)
	if err != nil {
		return dialer.DialContext
	}

	contextDialer, ok := socksDialer.(proxy.ContextDialer)
	if ok {
		return contextDialer.DialContext
	}

	return func(ctx context.Context, network, target string) (net.Conn, error) {
		result := make(chan struct {
			conn net.Conn
			err  error
		}, 1)
		go func() {
			conn, dialErr := socksDialer.Dial(network, target)
			result <- struct {
				conn net.Conn
				err  error
			}{conn: conn, err: dialErr}
		}()

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case outcome := <-result:
			return outcome.conn, outcome.err
		}
	}
}

func NewHTTPClient(dialer DialContextFunc) *http.Client {
	transport := &http.Transport{
		Proxy:                 nil,
		DialContext:           dialer,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		ExpectContinueTimeout: time.Second,
	}
	return &http.Client{Transport: transport}
}
