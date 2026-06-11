package proc

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestStartLinesAndExit(t *testing.T) {
	p, err := Start(context.Background(), Spec{Bin: "/bin/sh", Args: []string{"-c", "echo one; echo two"}})
	if err != nil {
		t.Fatal(err)
	}
	p.CloseStdin()

	var lines []string
	for line := range p.Lines {
		lines = append(lines, line)
	}
	if err := p.Wait(); err != nil {
		t.Fatal(err)
	}
	if len(lines) != 2 || lines[0] != "one" || lines[1] != "two" {
		t.Fatalf("lines = %v", lines)
	}
}

func TestStdinRoundTrip(t *testing.T) {
	p, err := Start(context.Background(), Spec{Bin: "/bin/cat"})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Write([]byte("hello\n")); err != nil {
		t.Fatal(err)
	}
	select {
	case line := <-p.Lines:
		if line != "hello" {
			t.Fatalf("line = %q", line)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no echo from cat")
	}
	p.CloseStdin()
	p.Wait()
}

func TestCancelKillsProcess(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	p, err := Start(ctx, Spec{Bin: "/bin/sleep", Args: []string{"60"}})
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	done := make(chan error, 1)
	go func() { done <- p.Wait() }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("killed process should report an error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("process did not die after cancel")
	}
}

func TestLargeLine(t *testing.T) {
	// Agent events can be megabytes; the scanner buffer must cope.
	p, err := Start(context.Background(), Spec{Bin: "/bin/sh", Args: []string{"-c", `printf 'x%.0s' $(seq 1 200000); echo`}})
	if err != nil {
		t.Fatal(err)
	}
	p.CloseStdin()
	var got string
	for line := range p.Lines {
		got = line
	}
	p.Wait()
	if len(got) != 200000 || !strings.HasPrefix(got, "xxx") {
		t.Fatalf("len = %d", len(got))
	}
}

func TestMissingBinary(t *testing.T) {
	if _, err := Start(context.Background(), Spec{Bin: "/no/such/binary"}); err == nil {
		t.Fatal("want error for missing binary")
	}
}

func TestCwd(t *testing.T) {
	p, err := Start(context.Background(), Spec{Bin: "/bin/pwd", Cwd: "/tmp"})
	if err != nil {
		t.Fatal(err)
	}
	p.CloseStdin()
	line := <-p.Lines
	p.Wait()
	if line != "/tmp" && line != "/private/tmp" { // macOS symlinks /tmp
		t.Fatalf("pwd = %q", line)
	}
}
