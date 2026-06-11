package main

import (
	"fmt"
	"net"
	"net/url"

	"github.com/codingagentprotocol/capd/internal/config"
)

func daemonWSURL(cfg config.Config, token string) string {
	u := url.URL{
		Scheme: "ws",
		Host:   net.JoinHostPort(cfg.Host, fmt.Sprint(cfg.Port)),
		Path:   "/ws",
	}
	q := u.Query()
	q.Set("token", token)
	u.RawQuery = q.Encode()
	return u.String()
}

func consoleURL(cfg config.Config, token string) string {
	u := url.URL{
		Scheme: "http",
		Host:   net.JoinHostPort(cfg.Host, fmt.Sprint(cfg.Port)),
		Path:   "/console/",
	}
	q := u.Query()
	q.Set("token", token)
	u.RawQuery = q.Encode()
	return u.String()
}
