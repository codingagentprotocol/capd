// Package daemon is the assembly board: it wires config, adapters, sessions,
// and the server together. This hand-written wiring is the whole DI story —
// every dependency is visible right here.
package daemon

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/codingagentprotocol/capd/internal/account"
	"github.com/codingagentprotocol/capd/internal/account/secret"
	"github.com/codingagentprotocol/capd/internal/adapter"
	"github.com/codingagentprotocol/capd/internal/adapter/claudecode"
	"github.com/codingagentprotocol/capd/internal/adapter/codex"
	"github.com/codingagentprotocol/capd/internal/adapter/cursoragent"
	"github.com/codingagentprotocol/capd/internal/adapter/gemini"
	"github.com/codingagentprotocol/capd/internal/adapter/opencode"
	"github.com/codingagentprotocol/capd/internal/config"
	"github.com/codingagentprotocol/capd/internal/server"
	"github.com/codingagentprotocol/capd/internal/session"
)

// Version is stamped by goreleaser via -ldflags.
var Version = "dev"

// Registry returns the adapters compiled into this build.
func Registry() *adapter.Registry {
	return adapter.NewRegistry(
		claudecode.New(),
		codex.New(),
		cursoragent.New(),
		gemini.New(),
		opencode.New(),

		// gemini-cli forks: same headless flags, shared translator.
		gemini.NewWithCLI("qwen-code", "Qwen Code", "qwen"),
		gemini.NewWithCLI("iflow", "iFlow CLI", "iflow"),

		// claude-code-compatible headless interface.
		claudecode.NewWithCLI("codebuddy", "Tencent CodeBuddy", "codebuddy"),

		// Discovery-only until calibrated against real output.
		adapter.NewPendingCLI("kimi", "Kimi CLI", "kimi", "--version"),
	)
}

// enrichPATH merges the user's login-shell PATH into the process PATH.
// Service managers (launchd, systemd) start capd with a minimal PATH that
// misses nvm/homebrew/user-local bins — exactly where agent CLIs live.
func enrichPATH(log *slog.Logger) {
	if runtime.GOOS == "windows" {
		return
	}
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}
	out, err := exec.Command(shell, "-lc", "echo $PATH").Output()
	if err != nil {
		log.Warn("could not read login-shell PATH", "err", err)
		return
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	loginPath := strings.TrimSpace(lines[len(lines)-1]) // rc files may echo noise above
	if loginPath == "" {
		return
	}

	seen := map[string]bool{}
	var merged []string
	for _, p := range append(strings.Split(loginPath, ":"), strings.Split(os.Getenv("PATH"), ":")...) {
		if p != "" && !seen[p] {
			seen[p] = true
			merged = append(merged, p)
		}
	}
	os.Setenv("PATH", strings.Join(merged, ":"))
}

// Run assembles and starts the daemon, blocking until ctx is cancelled.
func Run(ctx context.Context, cfg config.Config, log *slog.Logger) error {
	enrichPATH(log)
	token, err := EnsureToken()
	if err != nil {
		return err
	}
	home, err := Home()
	if err != nil {
		return err
	}
	store, err := session.OpenStore(filepath.Join(home, "capd.db"))
	if err != nil {
		return err
	}
	defer store.Close()
	accountStore, err := account.OpenStore(filepath.Join(home, "accounts.db"))
	if err != nil {
		return err
	}
	defer accountStore.Close()
	secrets, err := secret.Open(filepath.Join(home, "secrets", "codex"), cfg.SecretBackend)
	if err != nil {
		return err
	}

	reg := Registry()
	sessions := session.NewManager(reg, store)

	srv := server.New(server.Options{
		Host:        cfg.Host,
		Port:        cfg.Port,
		Origins:     cfg.Origins,
		Token:       token,
		Version:     Version,
		Registry:    reg,
		Sessions:    sessions,
		Accounts:    accountStore,
		Secrets:     secrets,
		RuntimeRoot: filepath.Join(home, "runtimes"),
		Log:         log,
	})
	return srv.Run(ctx)
}
