// Package proc owns subprocess lifecycle: spawning agent CLIs, streaming
// their line-oriented JSON output, and guaranteeing cleanup. Adapters never
// touch os/exec directly — they describe a command and consume a line stream.
package proc

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os/exec"
)

// Spec describes a subprocess to run.
type Spec struct {
	Bin  string
	Args []string
	Cwd  string
	Env  []string // appended to the parent environment
}

// Proc is a running subprocess with a line-stream over its stdout.
type Proc struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser
	Lines <-chan string // one stdout line per message; closed on exit
}

// Start launches the subprocess. Cancelling ctx kills it.
func Start(ctx context.Context, spec Spec) (*Proc, error) {
	cmd := exec.CommandContext(ctx, spec.Bin, spec.Args...)
	cmd.Dir = spec.Cwd
	if len(spec.Env) > 0 {
		cmd.Env = append(cmd.Environ(), spec.Env...)
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("proc: stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("proc: stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("proc: start %s: %w", spec.Bin, err)
	}

	lines := make(chan string, 64)
	go func() {
		defer close(lines)
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024) // agent events can be large
		for sc.Scan() {
			select {
			case lines <- sc.Text():
			case <-ctx.Done():
				return
			}
		}
	}()

	return &Proc{cmd: cmd, stdin: stdin, Lines: lines}, nil
}

// Write sends data to the subprocess stdin.
func (p *Proc) Write(data []byte) error {
	_, err := p.stdin.Write(data)
	return err
}

// Wait blocks until the subprocess exits and releases its resources.
func (p *Proc) Wait() error { return p.cmd.Wait() }
