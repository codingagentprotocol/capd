package main

import (
	"fmt"
	"net"
	"net/url"
	"strings"

	"github.com/codingagentprotocol/capd/internal/config"
)

func daemonWSURL(cfg config.Config, token string) string {
	u := url.URL{
		Scheme: "ws",
		Host:   daemonAddr(cfg),
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
		Host:   daemonAddr(cfg),
		Path:   "/console/",
	}
	q := u.Query()
	q.Set("token", token)
	u.RawQuery = q.Encode()
	return u.String()
}

func daemonAddr(cfg config.Config) string {
	return net.JoinHostPort(cfg.Host, fmt.Sprint(cfg.Port))
}

func daemonConnectError(cfg config.Config, token string, err error) error {
	return fmt.Errorf("connect to capd at %s (is 'capd start' running?): %s", daemonAddr(cfg), redactDaemonToken(err.Error(), token))
}

func redactDaemonToken(text, token string) string {
	if token == "" {
		return text
	}
	for _, value := range []string{token, url.QueryEscape(token)} {
		if value != "" {
			text = strings.ReplaceAll(text, value, "<redacted>")
		}
	}
	return text
}
