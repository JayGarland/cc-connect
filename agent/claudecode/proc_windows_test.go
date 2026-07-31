//go:build windows

package claudecode

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/JayGarland/cc-connect/core"
)

// processAlive reports whether a Windows process with the given PID exists,
// using `tasklist /FI "PID eq N"`. Returns true when the process is present.
func processAlive(pid int) bool {
	out, err := exec.Command("tasklist", "/FI", fmt.Sprintf("PID eq %d", pid)).CombinedOutput()
	if err != nil {
		return true // assume alive if the query itself fails
	}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 {
			if p, convErr := strconv.Atoi(fields[1]); convErr == nil && p == pid {
				return true
			}
		}
	}
	return false
}

// waitForHelperPIDs waits for the spawn-child-hold-stdout helper to write
// "<directPID> <grandchildPID>" to its pidfile. Returns both PIDs.
func waitForHelperPIDs(t *testing.T, pidFile string) (directPID, grandchildPID int) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(pidFile); err == nil {
			fields := strings.Fields(string(data))
			if len(fields) == 2 {
				d, err1 := strconv.Atoi(fields[0])
				g, err2 := strconv.Atoi(fields[1])
				if err1 == nil && err2 == nil {
					return d, g
				}
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("helper did not report direct+grandchild pids (pidfile=%s)", pidFile)
	return 0, 0
}

// spawnShimTree starts a process tree shaped like claude.cmd → claude.exe on
// Windows: the direct child is a shim (spawn-child-hold-stdout) that spawns a
// grandchild holding stdout and sleeping. Returns the started command and the
// pidfile path.
func spawnShimTree() (*exec.Cmd, string) {
	pidFile := filepath.Join(os.TempDir(), fmt.Sprintf("cc-helper-pids-%d.txt", time.Now().UnixNano()))
	cmd := exec.Command(os.Args[0], "-test.run=TestHelperProcess", "--", "spawn-child-hold-stdout")
	cmd.Env = append(os.Environ(), "GO_WANT_HELPER_PROCESS=1", "CC_HELPER_PIDFILE="+pidFile)
	prepareCmdForKill(cmd)
	return cmd, pidFile
}

// TestForceKillCmd_ReapsDescendantTree is a L-0720 gate for defect ②'s kill
// primitive: forceKillCmd must reap the whole descendant tree (shim + the
// stdout-holding grandchild), not just the direct child. The grandchild is
// precisely what was orphaned in the dev-pro incident — the agent process kept
// streaming into the events channel for 8+ minutes after the turn was declared
// over. taskkill /T /F must run while the direct child is still alive, or /T
// has no parent to walk and the grandchild survives.
func TestForceKillCmd_ReapsDescendantTree(t *testing.T) {
	cmd, pidFile := spawnShimTree()
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Start(); err != nil {
		t.Fatalf("start shim tree: %v", err)
	}
	defer cmd.Process.Kill() // best-effort cleanup on failure

	directPID, grandchildPID := waitForHelperPIDs(t, pidFile)
	defer os.Remove(pidFile)

	if err := forceKillCmd(cmd); err != nil {
		t.Fatalf("forceKillCmd: %v", err)
	}
	if processAlive(directPID) {
		t.Errorf("direct child pid %d still alive after forceKillCmd", directPID)
	}
	if processAlive(grandchildPID) {
		t.Errorf("grandchild pid %d still alive after forceKillCmd — taskkill /T /F missed the tree (L-0720)", grandchildPID)
	}
}

// TestClaudeSessionClose_KillsTreeAndCompletesPromptly is the L-0720 gate for
// defect ② at the Close() level — the path /new and session cleanup actually
// call. On Windows the old Close() waited the full 120s graceful phase, then
// failed SIGTERM (taskkill without /F is refused by the CLI), then called
// cancel() BEFORE forceKillCmd — which killed only the direct child and left
// the grandchild orphaned, exactly the dev-pro incident. The fixed Close() must
// return promptly and reap the whole tree. Red on old logic on both counts:
// it exceeds the budget (graceful burn) and the grandchild survives.
func TestClaudeSessionClose_KillsTreeAndCompletesPromptly(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cmd, pidFile := spawnShimTree()
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf
	if err := cmd.Start(); err != nil {
		t.Fatalf("start shim tree: %v", err)
	}

	cs := &claudeSession{
		cmd:                 cmd,
		stdin:               stdin,
		events:              make(chan core.Event, 64),
		ctx:                 ctx,
		cancel:              cancel,
		done:                make(chan struct{}),
		gracefulStopTimeout: 120 * time.Second, // same as production spawn
		forcedReapTimeout:   5 * time.Second,
	}
	cs.alive.Store(true)
	go cs.readLoop(stdout, &stderrBuf)

	directPID, grandchildPID := waitForHelperPIDs(t, pidFile)
	defer os.Remove(pidFile)

	// Close() must return promptly (not burn the 120s graceful phase) and
	// must reap the whole tree so /new leaves no orphan.
	closeDone := make(chan error, 1)
	go func() { closeDone <- cs.Close() }()

	const closeBudget = 30 * time.Second
	var closeErr error
	select {
	case closeErr = <-closeDone:
	case <-time.After(closeBudget):
		t.Fatalf("Close() did not return within %v — graceful phase still burning time (L-0720)", closeBudget)
	}
	if closeErr != nil {
		t.Fatalf("Close() error = %v", closeErr)
	}

	if processAlive(directPID) {
		t.Errorf("direct child pid %d still alive after Close()", directPID)
	}
	if processAlive(grandchildPID) {
		t.Errorf("grandchild pid %d still alive after Close() — agent process tree not reaped (L-0720)", grandchildPID)
	}
}
