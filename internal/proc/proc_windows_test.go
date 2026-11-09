//go:build windows

package proc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The test binary re-executes itself in these modes, so a test can start a process in a job object
// the way an agent's MCP server would, without a second program to build.
const (
	helperStart  = "proc-helper-start"  // args: log path, PowerShell path ("" for the real one)
	helperSleep  = "proc-helper-sleep"  // args: optionally --log FILE, as a WMI start passes it
	helperArgv   = "proc-helper-argv"   // args: arguments..., --log FILE; writes what it received to FILE
	readyMarker  = "proc helper: ready" // followed by the sleeper's PID
	envFindFails = "MURMUR_PROC_TEST_FIND_FAILS"
	envProbe     = "MURMUR_PROC_TEST_PROBE"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case helperStart:
			helperStartMain(os.Args[2:])
		case helperSleep:
			helperSleepMain(os.Args[2:])
		case helperArgv:
			helperArgvMain(os.Args[2:])
		}
	}
	os.Exit(m.Run())
}

// helperStartMain stands in for an agent's MCP server. It puts itself in a job that kills its
// members when the job closes and forbids breakaway, starts a sleeper with StartDetached, prints
// the sleeper's PID, and closes the job. Closing the job ends this process too.
func helperStartMain(args []string) {
	fail := func(err error) {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if len(args) != 2 {
		fail(errors.New("usage: proc-helper-start LOG PSPATH"))
	}
	logPath, psExe := args[0], args[1]

	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		fail(err)
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		fail(err)
	}
	if err := windows.AssignProcessToJobObject(job, windows.CurrentProcess()); err != nil {
		fail(err)
	}
	if psExe != "" {
		powershellPath = psExe
	}
	if os.Getenv(envFindFails) != "" {
		findProcess = func(int) (*os.Process, error) { return nil, errors.New("process has exited") }
	}

	logf, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		fail(err)
	}
	exe, err := os.Executable()
	if err != nil {
		fail(err)
	}
	p, err := StartDetached(exe, []string{helperSleep}, filepath.Dir(logPath), logf, nil)
	if err != nil {
		fail(err)
	}
	if p == nil {
		fmt.Println("gone")
		windows.CloseHandle(job)
		os.Exit(0)
	}
	fmt.Println(p.Pid)
	windows.CloseHandle(job)
	time.Sleep(time.Minute) // not reached: the job's close ends this process
	os.Exit(0)
}

// helperSleepMain stands in for the daemon. Like the daemon, it takes --log FILE and writes there.
func helperSleepMain(args []string) {
	if len(args) == 2 && args[0] == "--log" {
		f, err := os.OpenFile(args[1], os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			os.Exit(1)
		}
		os.Stdout, os.Stderr = f, f
	}
	fmt.Printf("%s %d\n", readyMarker, os.Getpid())
	time.Sleep(10 * time.Minute)
	os.Exit(0)
}

// argvReport is what helperArgvMain writes: the arguments it got, its working directory and its
// environment.
type argvReport struct {
	Args []string
	Cwd  string
	Env  []string
}

// helperArgvMain writes what it received to the file named after the last --log, so a test can
// compare it with what it sent.
func helperArgvMain(args []string) {
	idx := -1
	for i, a := range args {
		if a == "--log" {
			idx = i
		}
	}
	if idx < 0 || idx+1 >= len(args) {
		os.Exit(2)
	}
	cwd, _ := os.Getwd()
	b, err := json.Marshal(argvReport{Args: args[:idx], Cwd: cwd, Env: os.Environ()})
	if err != nil {
		os.Exit(3)
	}
	if err := os.WriteFile(args[idx+1], b, 0o600); err != nil {
		os.Exit(4)
	}
	os.Exit(0)
}

// TestStartDetachedSurvivesJobClose is the reproduction for #28. A daemon started from a job that
// forbids breakaway must outlive that job, and its output must still reach the log file.
func TestStartDetachedSurvivesJobClose(t *testing.T) {
	pid, logPath := startFromJobHelper(t, "")
	time.Sleep(time.Second)
	if !alive(pid) {
		t.Fatalf("daemon %d died when the job that started it closed; log:\n%s", pid, readFile(logPath))
	}
	waitForLog(t, logPath, readyMarker)
}

// TestStartDetachedFallsBackInsideJob checks the last step of the order: with WMI blocked too, the
// daemon starts inside the job, as it did before WMI was tried, and says so in the log.
func TestStartDetachedFallsBackInsideJob(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no-such-powershell.exe")
	pid, logPath := startFromJobHelper(t, missing)
	if !strings.Contains(readFile(logPath), "inside the job") {
		t.Fatalf("log doesn't say the daemon started inside the job:\n%s", readFile(logPath))
	}
	if !waitUntil(5*time.Second, func() bool { return !alive(pid) }) {
		t.Fatalf("daemon %d survived a job close, but it started inside the job", pid)
	}
}

// TestStartDetachedToleratesExitedWMIProcess checks that a WMI start whose process has already
// exited is a success: StartDetached returns no process and no error, logs it, and does not fall
// back into the job. The process WMI created is outside the job, so it is still running here.
func TestStartDetachedToleratesExitedWMIProcess(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "daemon.log")
	exe := copyTestBinary(t, "with space")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, helperStart, logPath, "")
	cmd.Env = append(os.Environ(), envFindFails+"=1")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("helper: %v\nlog:\n%s", err, readFile(logPath))
	}
	if got := strings.TrimSpace(string(out)); got != "gone" {
		t.Fatalf("helper printed %q, want \"gone\"; log:\n%s", got, readFile(logPath))
	}
	waitForLog(t, logPath, readyMarker)
	log := readFile(logPath)
	if !strings.Contains(log, "had already exited") {
		t.Errorf("log doesn't record the exited process:\n%s", log)
	}
	if strings.Contains(log, "inside the job") {
		t.Errorf("fell back into the job after WMI had started the daemon:\n%s", log)
	}
	pid := sleeperPID(t, log)
	t.Cleanup(func() { kill(pid) })
	if !alive(pid) {
		t.Fatalf("the WMI-started process %d is not running", pid)
	}
}

// TestWMIPassesArgumentsDirectoryAndEnvironmentUnchanged starts a helper through WMI that reports
// what it received. Each argument, the working directory and the environment must arrive exactly
// as sent, whatever characters they hold.
func TestWMIPassesArgumentsDirectoryAndEnvironmentUnchanged(t *testing.T) {
	const tricky = `O'Brien & 100% (x) ’q’ ‘r‘ naïve 日本`
	dir := filepath.Join(t.TempDir(), tricky)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	exe := copyTestBinary(t, tricky)
	probe := `a & b %PATH% 'x' "y" ’ ‘ (z) ñ 日本`
	t.Setenv(envProbe, probe)

	args := []string{helperArgv, "plain", "with space", "a&b", "100%", "it's", `say "hi"`,
		"(paren)", "’curly’", "‘x‘", "naïve 日本", `C:\trailing\`, ""}
	logPath := filepath.Join(t.TempDir(), "argv.json")
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := startThroughWMI(exe, args, dir, f, os.Environ())
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { kill(pid) })

	var got argvReport
	if !waitUntil(15*time.Second, func() bool {
		b, err := os.ReadFile(logPath)
		return err == nil && json.Unmarshal(b, &got) == nil
	}) {
		t.Fatalf("helper never reported; log:\n%s", readFile(logPath))
	}

	want := args[1:]
	if len(got.Args) != len(want) {
		t.Fatalf("got %d arguments %q, want %d %q", len(got.Args), got.Args, len(want), want)
	}
	for i := range want {
		if got.Args[i] != want[i] {
			t.Errorf("argument %d: got %q, want %q", i, got.Args[i], want[i])
		}
	}
	if got.Cwd != dir {
		t.Errorf("working directory: got %q, want %q", got.Cwd, dir)
	}
	env := make(map[string]bool, len(got.Env))
	for _, kv := range got.Env {
		env[kv] = true
	}
	if !env[envProbe+"="+probe] {
		t.Errorf("environment variable %s didn't arrive unchanged", envProbe)
	}
	for _, kv := range os.Environ() {
		if !env[kv] {
			t.Errorf("environment entry %q didn't arrive", kv)
		}
	}
}

// TestParseWMIPID checks the PID the script prints: zero and anything that isn't a PID are errors.
func TestParseWMIPID(t *testing.T) {
	for _, tc := range []struct {
		out string
		pid int
		ok  bool
	}{
		{"1234", 1234, true},
		{"1234\r\n", 1234, true},
		{"0", 0, false},
		{"-5", 0, false},
		{"", 0, false},
		{"12 34", 0, false},
		{"Win32_Process.Create returned 2", 0, false},
	} {
		pid, err := parseWMIPID(tc.out)
		if (err == nil) != tc.ok || pid != tc.pid {
			t.Errorf("parseWMIPID(%q) = %d, %v; want %d, ok=%v", tc.out, pid, err, tc.pid, tc.ok)
		}
	}
}

// TestStartDetachedWithoutJob checks the ordinary case: no job, so breakaway succeeds and the
// output goes to the log file.
func TestStartDetachedWithoutJob(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "daemon.log")
	logf, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { logf.Close() })
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	p, err := StartDetached(exe, []string{helperSleep}, filepath.Dir(logPath), logf, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		kill(p.Pid)
		p.Release()
	})
	waitForLog(t, logPath, readyMarker)
}

// TestWindowsCommandLineRoundTrip checks that the command line handed to WMI splits back into the
// same argv, including a path with spaces (the Desktop path has them) and awkward quoting.
func TestWindowsCommandLineRoundTrip(t *testing.T) {
	path := `C:\Users\dev\Desktop\My Project\murmur\mur.exe`
	args := []string{
		"daemon", "--git-dir", `C:\Users\dev\Desktop\My Project\murmur\.git`,
		"--log", `C:\dir with "quotes"\ends with a slash\`,
	}
	got, err := windows.DecomposeCommandLine(windowsCommandLine(path, args))
	if err != nil {
		t.Fatal(err)
	}
	want := append([]string{path}, args...)
	if len(got) != len(want) {
		t.Fatalf("got %d arguments %q, want %d %q", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("argument %d: got %q, want %q", i, got[i], want[i])
		}
	}
}

// startFromJobHelper runs the helper and returns the sleeper's PID and the log it writes. The
// helper's job has closed by the time this returns, so the sleeper is alive only if it escaped.
func startFromJobHelper(t *testing.T, psExe string) (int, string) {
	t.Helper()
	logPath := filepath.Join(t.TempDir(), "daemon.log")
	exe := copyTestBinary(t, "with space")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	out, err := exec.CommandContext(ctx, exe, helperStart, logPath, psExe).Output()
	if err != nil {
		var ee *exec.ExitError
		stderr := ""
		if errors.As(err, &ee) {
			stderr = string(ee.Stderr)
		}
		t.Fatalf("helper: %v\n%s\nlog:\n%s", err, stderr, readFile(logPath))
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		t.Fatalf("helper printed %q, not a PID: %v", out, err)
	}
	t.Cleanup(func() { kill(pid) })
	return pid, logPath
}

// copyTestBinary copies this test binary into a directory of the given name under a temporary
// directory, so the start quotes a path with a space or other awkward characters in it.
func copyTestBinary(t *testing.T, dirName string) string {
	t.Helper()
	src, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), dirName, "proc.test.exe")
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, data, 0o700); err != nil {
		t.Fatal(err)
	}
	return dst
}

// sleeperPID reads the PID the sleeper printed after its readyMarker.
func sleeperPID(t *testing.T, log string) int {
	t.Helper()
	for _, line := range strings.Split(log, "\n") {
		if rest, ok := strings.CutPrefix(line, readyMarker+" "); ok {
			pid, err := strconv.Atoi(strings.TrimSpace(rest))
			if err == nil {
				return pid
			}
		}
	}
	t.Fatalf("no sleeper PID in log:\n%s", log)
	return 0
}

// alive reports whether the process with pid is still running.
func alive(pid int) bool {
	h, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	ev, err := windows.WaitForSingleObject(h, 0)
	return err == nil && ev == uint32(windows.WAIT_TIMEOUT)
}

// kill ends the process with pid, if it still exists, and waits for it to go, so the test's
// temporary directory can be removed afterwards.
func kill(pid int) {
	h, err := windows.OpenProcess(windows.PROCESS_TERMINATE|windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return
	}
	defer windows.CloseHandle(h)
	windows.TerminateProcess(h, 1)
	windows.WaitForSingleObject(h, 5000)
}

func readFile(path string) string {
	b, _ := os.ReadFile(path)
	return string(b)
}

func waitUntil(d time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return cond()
}

func waitForLog(t *testing.T, path, marker string) {
	t.Helper()
	if !waitUntil(15*time.Second, func() bool { return strings.Contains(readFile(path), marker) }) {
		t.Fatalf("log never got %q:\n%s", marker, readFile(path))
	}
}
