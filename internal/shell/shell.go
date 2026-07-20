// Package shell is the only package in roost that executes external
// commands. Everything else takes a Runner, so tests inject a fake and
// never touch a real Docker daemon or the network.
package shell

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Result captures a finished command's output.
type Result struct {
	Stdout string
	Stderr string
}

// Runner executes external commands.
type Runner interface {
	// Run executes a command and captures its output.
	Run(name string, args ...string) (Result, error)
	// Stream executes a command wired to the user's terminal, for
	// long-running interactive output like `logs -f`.
	Stream(name string, args ...string) error
}

// Exec is the real Runner backed by os/exec.
type Exec struct{}

func (Exec) Run(name string, args ...string) (Result, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.Command(name, args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	res := Result{Stdout: stdout.String(), Stderr: stderr.String()}
	if err != nil {
		return res, fmt.Errorf("%s %s: %w\n%s", name, strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return res, nil
}

func (Exec) Stream(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// Call records one command a Fake was asked to run.
type Call struct {
	Name string
	Args []string
}

// String renders the call as a single command line.
func (c Call) String() string {
	return c.Name + " " + strings.Join(c.Args, " ")
}

// Fake is a Runner for tests: it records every call and answers via
// the optional hooks.
type Fake struct {
	Calls []Call
	// RunFunc, when set, supplies Run results. Defaults to empty
	// success.
	RunFunc func(name string, args ...string) (Result, error)
	// StreamErr is returned from Stream.
	StreamErr error
}

func (f *Fake) Run(name string, args ...string) (Result, error) {
	f.Calls = append(f.Calls, Call{Name: name, Args: args})
	if f.RunFunc != nil {
		return f.RunFunc(name, args...)
	}
	return Result{}, nil
}

func (f *Fake) Stream(name string, args ...string) error {
	f.Calls = append(f.Calls, Call{Name: name, Args: args})
	return f.StreamErr
}
