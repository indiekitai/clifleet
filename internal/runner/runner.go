// Package runner executes a clifleet script on a host.
//
// Remote hosts go through the system ssh binary so ProxyJump, agent
// forwarding, Include, Match, and known_hosts keep working. clifleet does
// not disable StrictHostKeyChecking.
package runner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Target is one place to run a script.
type Target struct {
	Name        string
	Local       bool
	Destination string
	Port        int
	Identity    string
	Timeout     time.Duration
}

// Runner executes a script. Stdout is the protocol; stderr is diagnostics.
type Runner interface {
	Run(ctx context.Context, target Target, script string) (stdout, stderr string, err error)
}

// Exec runs scripts with the local sh or the system ssh client.
type Exec struct{}

func (Exec) Run(ctx context.Context, target Target, script string) (string, string, error) {
	if target.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, target.Timeout)
		defer cancel()
	}
	var cmd *exec.Cmd
	if target.Local {
		cmd = exec.CommandContext(ctx, "sh", "-s")
	} else {
		if strings.TrimSpace(target.Destination) == "" {
			return "", "", fmt.Errorf("host %s: empty ssh destination", target.Name)
		}
		cmd = exec.CommandContext(ctx, "ssh", SSHArgs(target)...)
	}
	cmd.Stdin = strings.NewReader(script)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	// A new process group so the timeout kills ssh/sh and the children that
	// inherited their pipes. Otherwise a stuck `sleep` or CLI keeps Run
	// blocked until that child exits, and the host looks stuck past the budget.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = 200 * time.Millisecond
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if err != nil && !errors.Is(err, syscall.ESRCH) {
			return cmd.Process.Kill()
		}
		return nil
	}
	err := cmd.Run()
	out, errOut := stdout.String(), stderr.String()
	if ctx.Err() != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
		msg := timeoutMessage(target, out, errOut)
		if tail := tail(errOut, 8); tail != "" && !strings.Contains(msg, tail) {
			msg += ": " + tail
		}
		return out, errOut, errors.New(msg)
	}
	if err != nil {
		msg := err.Error()
		if tail := tail(errOut, 12); tail != "" {
			msg = tail
		}
		return out, errOut, fmt.Errorf("host %s: %s", target.Name, oneLine(msg))
	}
	return out, errOut, nil
}

// DialArgs is the OpenSSH argv up to and including the destination.
// It does not include a remote command. "--" ends the options, so a
// destination that starts with "-" cannot be read as one (-oProxyCommand=…).
func DialArgs(target Target) []string {
	connect := 15 * time.Second
	if target.Timeout > 0 && target.Timeout < connect {
		connect = target.Timeout
	}
	if connect < time.Second {
		connect = time.Second
	}
	sec := int((connect + time.Second - 1) / time.Second)
	// -T and RequestTTY=no override a host config that forces a TTY.
	// With a TTY, `sh -s` can sit at an interactive prompt while doctor’s
	// `true` still returns, so status looks stuck for the whole budget.
	args := []string{
		"-T",
		"-o", "BatchMode=yes",
		"-o", "RequestTTY=no",
		"-o", "ConnectTimeout=" + strconv.Itoa(sec),
		"-o", "LogLevel=ERROR",
	}
	if target.Port > 0 {
		args = append(args, "-p", strconv.Itoa(target.Port))
	}
	if target.Identity != "" {
		args = append(args, "-i", target.Identity, "-o", "IdentitiesOnly=yes")
	}
	return append(args, "--", target.Destination)
}

// SSHArgs is the OpenSSH argv for a script on stdin (`sh -s`).
func SSHArgs(target Target) []string {
	return append(DialArgs(target), "sh", "-s")
}

func timeoutMessage(target Target, stdout, stderr string) string {
	msg := fmt.Sprintf("timed out after %s", target.Timeout.Round(time.Second))
	script := strings.Contains(stdout, "clifleet-script/1") || strings.Contains(stderr, "clifleet-script/1")
	proto := strings.Contains(stdout, "clifleet-probe/1") ||
		strings.Contains(stdout, "clifleet-upgrade/1") ||
		strings.Contains(stdout, "clifleet-fetch/1") ||
		strings.Contains(stdout, "clifleet-apply/1")
	switch {
	case proto:
		msg += "; the remote command started but did not finish"
	case script:
		msg += "; the probe script started but did not finish"
	case target.Local:
		msg += "; no clifleet output"
	default:
		msg += "; no clifleet output (SSH accepted the session; the shell did not start the script)"
	}
	return msg
}

func tail(s string, n int) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func oneLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = strings.TrimSpace(s[:i]) + "…"
	}
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}
