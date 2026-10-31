package runner

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestSSHArgsBatchMode(t *testing.T) {
	args := SSHArgs(Target{Destination: "claw1", Port: 2222, Identity: "/keys/id", Timeout: 3 * time.Second})
	got := strings.Join(args, " ")
	for _, want := range []string{"-T", "BatchMode=yes", "RequestTTY=no", "ConnectTimeout=3", "-p 2222", "-i /keys/id", "IdentitiesOnly=yes", "claw1", "sh -s"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in %s", want, got)
		}
	}
	if strings.Contains(got, "StrictHostKeyChecking=no") {
		t.Fatal("host key checking was disabled")
	}
	if args[len(args)-4] != "--" || args[len(args)-3] != "claw1" || args[len(args)-2] != "sh" || args[len(args)-1] != "-s" {
		t.Fatalf("tail %v", args[len(args)-4:])
	}
	dial := DialArgs(Target{Destination: "claw1"})
	if dial[len(dial)-2] != "--" || dial[len(dial)-1] != "claw1" {
		t.Fatalf("dial %v", dial)
	}
}

func TestTimeoutSaysWhetherTheScriptStarted(t *testing.T) {
	ctx := context.Background()
	start := time.Now()
	_, _, err := Exec{}.Run(ctx, Target{Name: "omarchy", Local: true, Timeout: time.Second}, "sleep 5\n")
	if err == nil || !strings.Contains(err.Error(), "timed out after 1s") || !strings.Contains(err.Error(), "no clifleet output") {
		t.Fatal(err)
	}
	_, _, err = Exec{}.Run(ctx, Target{Local: true, Timeout: time.Second}, "printf '%s\\n' clifleet-script/1 >&2\nsleep 5\n")
	if err == nil || !strings.Contains(err.Error(), "probe script started") {
		t.Fatal(err)
	}
	_, _, err = Exec{}.Run(ctx, Target{Local: true, Timeout: time.Second}, "printf '%s\\n' clifleet-probe/1\nsleep 5\n")
	if err == nil || !strings.Contains(err.Error(), "started but did not finish") {
		t.Fatal(err)
	}
	_, _, err = Exec{}.Run(ctx, Target{Local: true, Timeout: time.Second}, "printf '%s\\n' clifleet-script/1 >&2\nprintf '%s\\n' clifleet-fetch/1\nsleep 5\n")
	if err == nil || strings.Contains(err.Error(), "clifleet-script/1") {
		t.Fatalf("timeout message repeats the script marker: %v", err)
	}
	if time.Since(start) > 6*time.Second {
		t.Fatalf("timeout did not kill the process group: %s", time.Since(start))
	}
}

func TestInterruptIsNotATimeout(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(300 * time.Millisecond)
		cancel()
	}()
	_, _, err := Exec{}.Run(ctx, Target{Local: true, Timeout: time.Minute}, "printf '%s\\n' clifleet-apply/1\nsleep 5\n")
	if !errors.Is(err, ErrInterrupted) || !strings.Contains(err.Error(), "may still be running") || strings.Contains(err.Error(), "timed out") {
		t.Fatal(err)
	}
}
