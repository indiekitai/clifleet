package runner

import (
	"strings"
	"testing"
	"time"
)

func TestSSHArgsBatchMode(t *testing.T) {
	args := SSHArgs(Target{Destination: "claw1", Port: 2222, Identity: "/keys/id", Timeout: 3 * time.Second})
	got := strings.Join(args, " ")
	for _, want := range []string{"BatchMode=yes", "ConnectTimeout=3", "-p 2222", "-i /keys/id", "IdentitiesOnly=yes", "claw1", "sh -s"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in %s", want, got)
		}
	}
	if strings.Contains(got, "StrictHostKeyChecking=no") {
		t.Fatal("host key checking was disabled")
	}
	if args[len(args)-3] != "claw1" || args[len(args)-2] != "sh" || args[len(args)-1] != "-s" {
		t.Fatalf("tail %v", args[len(args)-3:])
	}
	dial := DialArgs(Target{Destination: "claw1"})
	if dial[len(dial)-1] != "claw1" {
		t.Fatalf("dial %v", dial)
	}
}
