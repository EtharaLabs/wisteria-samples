// Command grader scores a submitted Go test suite for server/stree against a hidden pool
// of implementations of that package.
//
// A pool member is a directory of non-test .go files that replace the package's own. The
// submission is the set of *_test.go files (plus testdata/) found directly in the submitted
// package directory. For every member the grader compiles the submission once with -race
// and runs the binary three times, each with a different -test.shuffle seed and GOMAXPROCS,
// as an unprivileged user in a fresh HOME and TMPDIR. A member is accepted when every run
// passes and rejected when every run fails; mixed outcomes are neither.
//
// Dropping to that user needs privilege: either root, which is how the verifier image runs it
// (task.toml [verifier].user), or, for a grader started as a non-root uid, the capabilities in
// isolationCaps (CHOWN, DAC_OVERRIDE, FOWNER, KILL, SETGID, SETUID). With either, the
// grader first closes the reward directory and the shared scratch filesystems to everyone but
// itself, so submitted code can write only the HOME and TMPDIR it is given. Without either,
// submitted code runs as the grader's own user and the report says so (runner_isolated false).
//
// The score is a dependent chain: rejections only count for a sound suite. Correct members
// are graded first (the upstream fix, then the rest); if any of them is not accepted the
// reward is 0. Otherwise reward = incorrect members rejected / incorrect members.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// isolationCaps is the effective capability set that, without root, lets the grader run
// submitted code as another uid and clean up after it: CAP_CHOWN (0), CAP_DAC_OVERRIDE (1),
// CAP_FOWNER (3), CAP_KILL (5), CAP_SETGID (6) and CAP_SETUID (7).
const isolationCaps = 1<<0 | 1<<1 | 1<<3 | 1<<5 | 1<<6 | 1<<7

// privileged reports whether the grader can drop submitted code to a separate uid.
func privileged() bool {
	if os.Geteuid() == 0 {
		return true
	}
	data, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		if hex, ok := strings.CutPrefix(line, "CapEff:"); ok {
			eff, err := strconv.ParseUint(strings.TrimSpace(hex), 16, 64)
			return err == nil && eff&isolationCaps == isolationCaps
		}
	}
	return false
}

// lockDown closes the places the runner uid could otherwise write: the directory of the
// reward and log files becomes the grader's own (mode 0755), private scratch directories stay
// open to the grader's group alone (mode 1770), and /logs and the shared memory and lock
// filesystems become root's (mode 0755). Missing paths are skipped; failures are reported,
// not fatal.
func lockDown(logDirs []string) {
	uid, gid := os.Geteuid(), os.Getegid()
	type rule struct {
		path     string
		uid, gid int
		mode     os.FileMode
	}
	var rules []rule
	for _, d := range logDirs {
		rules = append(rules, rule{d, uid, gid, 0o755})
	}
	for _, d := range []string{"/tmp", "/var/tmp"} {
		rules = append(rules, rule{d, 0, gid, 0o770 | os.ModeSticky})
	}
	for _, d := range []string{"/logs", "/dev/shm", "/dev/mqueue", "/run/lock"} {
		rules = append(rules, rule{d, 0, 0, 0o755})
	}
	for _, r := range rules {
		st, err := os.Lstat(r.path)
		if err != nil || !st.IsDir() {
			continue
		}
		if err := os.Chown(r.path, r.uid, r.gid); err != nil {
			fmt.Fprintln(os.Stderr, "grader: lock down:", err)
		}
		if err := os.Chmod(r.path, r.mode); err != nil {
			fmt.Fprintln(os.Stderr, "grader: lock down:", err)
		}
	}
}

type runResult struct {
	Seed     int64   `json:"seed"`
	CPU      int     `json:"cpu"`
	Pass     bool    `json:"pass"`
	ExitCode int     `json:"exit_code"`
	Killed   bool    `json:"killed"`
	Seconds  float64 `json:"seconds"`
	Tail     string  `json:"tail,omitempty"`
}

type memberResult struct {
	Member  string      `json:"member"`
	Kind    string      `json:"kind"`
	Built   bool        `json:"built"`
	Build   string      `json:"build_output,omitempty"`
	Runs    []runResult `json:"runs"`
	Verdict string      `json:"verdict"` // accepted | rejected | mixed | build_failed | skipped
	Credit  bool        `json:"credit"`
}

type report struct {
	Reward          float64        `json:"reward"`
	GateAccepted    bool           `json:"gate_accepted"`
	Sound           bool           `json:"all_correct_accepted"`
	Correct         int            `json:"correct_members"`
	CorrectAccepted int            `json:"correct_accepted"`
	Incorrect       int            `json:"incorrect_members"`
	IncorrectRej    int            `json:"incorrect_rejected"`
	Submission      []string       `json:"submission_files"`
	Isolated        bool           `json:"runner_isolated"`
	Members         []memberResult `json:"members"`
	Note            string         `json:"note,omitempty"`
}

var (
	poolDir    = flag.String("pool", "/opt/grader/pool", "directory of pool members (c*/ correct, i*/ incorrect)")
	baseDir    = flag.String("base", "/opt/grader/base", "pristine module root (go.mod, server/stree, ...)")
	pkgRel     = flag.String("pkg", "server/stree", "package directory relative to the module root")
	submission = flag.String("submission", "/app/server/stree", "submitted package directory")
	stateDir   = flag.String("state", "/var/lib/grader", "root-only working directory")
	runRoot    = flag.String("runroot", "/var/lib/grader-run", "directory the runner can traverse")
	gate       = flag.String("gate", "c01_upstream", "member that must be accepted for any credit")
	runnerUID  = flag.Int("uid", 10002, "uid that executes submitted code")
	runnerGID  = flag.Int("gid", 10002, "gid that executes submitted code")
	runCap     = flag.Duration("run-timeout", 60*time.Second, "time limit for one test-binary run")
	buildCap   = flag.Duration("build-timeout", 300*time.Second, "time limit for one compile")
	outPath    = flag.String("out", "", "result file (default: <state>/result.json)")
	rewardPath = flag.String("reward", "", "reward carrier to publish last (e.g. /logs/verifier/reward.txt)")
	logPath    = flag.String("log", "", "read-only copy of the result for the trial logs")
	only       = flag.String("only", "", "comma-separated member names to grade (development)")
	noGate     = flag.Bool("no-gate-stop", false, "grade every member even if the gate fails (development)")
)

// Every run differs in shuffle seed and GOMAXPROCS.
var runPlan = []struct {
	seed int64
	cpu  int
}{{20261030, 1}, {8765, 2}, {6619, 4}}

const maxSubmissionBytes = 32 << 20

func main() {
	flag.Parse()
	isolated := privileged()
	if !isolated {
		// Without privilege there is no second uid to drop to; keep grading in places this
		// user can write so a runner that ignores the configured user still gets scores.
		base := os.TempDir()
		*stateDir = filepath.Join(base, "grader-state")
		*runRoot = filepath.Join(base, "grader-run")
	}
	if *outPath == "" {
		*outPath = filepath.Join(*stateDir, "result.json")
	}
	if isolated {
		var logDirs []string
		for _, p := range []string{*rewardPath, *logPath} {
			if p != "" {
				logDirs = append(logDirs, filepath.Dir(p))
			}
		}
		lockDown(logDirs)
	}
	rep, gradeErr := grade(isolated)
	if gradeErr != nil {
		fmt.Fprintln(os.Stderr, "grader:", gradeErr)
		rep = &report{Reward: 0, Isolated: isolated, Note: "grader error: " + gradeErr.Error()}
	}
	data, _ := json.MarshalIndent(rep, "", "  ")
	if err := os.WriteFile(*outPath, data, 0o600); err != nil {
		fmt.Fprintln(os.Stderr, "grader: write result:", err)
	}
	if isolated {
		reap(*runnerUID)
	}
	if *logPath != "" {
		publish(*logPath, data)
	}
	if *rewardPath != "" {
		// Single carrier: drop any reward.json beside it, then replace reward.txt atomically.
		os.Remove(filepath.Join(filepath.Dir(*rewardPath), "reward.json"))
		if err := publish(*rewardPath, []byte(strconv.FormatFloat(rep.Reward, 'f', -1, 64)+"\n")); err != nil {
			fmt.Fprintln(os.Stderr, "grader: publish reward:", err)
			os.Exit(2)
		}
	}
	fmt.Printf("reward=%s gate=%v sound=%v correct=%d/%d incorrect_rejected=%d/%d isolated=%v\n",
		strconv.FormatFloat(rep.Reward, 'f', -1, 64), rep.GateAccepted, rep.Sound,
		rep.CorrectAccepted, rep.Correct, rep.IncorrectRej, rep.Incorrect, rep.Isolated)
	if gradeErr != nil {
		os.Exit(2)
	}
}

// publish writes data next to path and renames it into place, leaving it read-only.
func publish(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".publish-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o444); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func grade(isolated bool) (*report, error) {
	rep := &report{Isolated: isolated}
	if !isolated {
		rep.Note = "grader is not privileged: submitted code runs as the grader's own user"
	}
	for _, d := range []string{*stateDir, *runRoot} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return nil, err
		}
	}
	if err := os.Chmod(*stateDir, 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(*runRoot, 0o711); err != nil {
		return nil, err
	}

	// 1. Harvest the submission: *_test.go directly in the package dir, plus testdata/.
	sub := filepath.Join(*stateDir, "submission")
	os.RemoveAll(sub)
	files, err := harvest(*submission, sub)
	if err != nil {
		return nil, fmt.Errorf("harvest: %w", err)
	}
	rep.Submission = files

	// 2. Members: gate first, the rest in a shuffled order.
	members, err := listMembers()
	if err != nil {
		return nil, err
	}
	for _, m := range members {
		if strings.HasPrefix(m, "c") {
			rep.Correct++
		} else {
			rep.Incorrect++
		}
	}

	sound := true
	for i, m := range members {
		mr, err := gradeMember(m, sub, isolated)
		if err != nil {
			// The grader could not run the submission at all: an infrastructure fault, not
			// a verdict. Abort so that it can never turn into credit or a rejection.
			return nil, fmt.Errorf("member %d: %w", i, err)
		}
		rep.Members = append(rep.Members, mr)
		if m == *gate {
			rep.GateAccepted = mr.Verdict == "accepted"
		}
		if mr.Kind == "correct" && mr.Verdict != "accepted" {
			sound = false
			if !*noGate {
				for _, rest := range members[i+1:] {
					rep.Members = append(rep.Members, memberResult{Member: rest, Kind: kind(rest), Verdict: "skipped"})
				}
				break
			}
		}
	}
	for _, mr := range rep.Members {
		if mr.Credit && mr.Kind == "correct" {
			rep.CorrectAccepted++
		}
		if mr.Credit && mr.Kind == "incorrect" {
			rep.IncorrectRej++
		}
	}
	rep.Sound = sound && rep.CorrectAccepted == rep.Correct
	if rep.GateAccepted && rep.Sound && rep.Incorrect > 0 {
		// A ratio of integers, so a perfect score is exactly 1.
		rep.Reward = float64(rep.IncorrectRej) / float64(rep.Incorrect)
	}
	return rep, nil
}

func kind(m string) string {
	if strings.HasPrefix(m, "c") {
		return "correct"
	}
	return "incorrect"
}

func listMembers() ([]string, error) {
	ents, err := os.ReadDir(*poolDir)
	if err != nil {
		return nil, err
	}
	want := map[string]bool{}
	for _, s := range strings.Split(*only, ",") {
		if s = strings.TrimSpace(s); s != "" {
			want[s] = true
		}
	}
	var correct, incorrect []string
	haveGate := false
	for _, e := range ents {
		n := e.Name()
		if !e.IsDir() || !(strings.HasPrefix(n, "c") || strings.HasPrefix(n, "i")) {
			continue
		}
		if len(want) > 0 && !want[n] && n != *gate {
			continue
		}
		if n == *gate {
			haveGate = true
			continue
		}
		if strings.HasPrefix(n, "c") {
			correct = append(correct, n)
		} else {
			incorrect = append(incorrect, n)
		}
	}
	if !haveGate {
		return nil, fmt.Errorf("gate member %q missing from pool", *gate)
	}
	sort.Strings(correct)
	sort.Strings(incorrect)
	r := rand.New(rand.NewPCG(uint64(time.Now().UnixNano()), 0x5eed))
	r.Shuffle(len(correct), func(i, j int) { correct[i], correct[j] = correct[j], correct[i] })
	r.Shuffle(len(incorrect), func(i, j int) { incorrect[i], incorrect[j] = incorrect[j], incorrect[i] })
	return append(append([]string{*gate}, correct...), incorrect...), nil
}

// harvest copies regular *_test.go files and the testdata tree from src into dst.
func harvest(src, dst string) ([]string, error) {
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return nil, err
	}
	var names []string
	var total int64
	ents, err := os.ReadDir(src)
	if err != nil {
		return nil, err
	}
	for _, e := range ents {
		n := e.Name()
		if !strings.HasSuffix(n, "_test.go") || !e.Type().IsRegular() {
			continue
		}
		sz, err := copyRegular(filepath.Join(src, n), filepath.Join(dst, n))
		if err != nil {
			return nil, err
		}
		total += sz
		names = append(names, n)
	}
	td := filepath.Join(src, "testdata")
	if st, err := os.Lstat(td); err == nil && st.IsDir() {
		err = filepath.WalkDir(td, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(src, p)
			out := filepath.Join(dst, rel)
			switch {
			case d.IsDir():
				return os.MkdirAll(out, 0o755)
			case d.Type().IsRegular():
				sz, err := copyRegular(p, out)
				total += sz
				if total > maxSubmissionBytes {
					return errors.New("submission larger than 32 MiB")
				}
				names = append(names, rel)
				return err
			}
			return nil // symlinks, sockets, devices: ignored
		})
		if err != nil {
			return nil, err
		}
	}
	if total > maxSubmissionBytes {
		return nil, errors.New("submission larger than 32 MiB")
	}
	sort.Strings(names)
	return names, nil
}

func copyRegular(src, dst string) (int64, error) {
	in, err := os.Open(src)
	if err != nil {
		return 0, err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return 0, err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(out, io.LimitReader(in, maxSubmissionBytes+1))
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	return n, err
}

func copyTree(src, dst string, mode fs.FileMode) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		out := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(out, 0o755)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		if _, err := copyRegular(p, out); err != nil {
			return err
		}
		return os.Chmod(out, mode)
	})
}

func gradeMember(m, sub string, isolated bool) (memberResult, error) {
	mr := memberResult{Member: m, Kind: kind(m)}
	ws := filepath.Join(*stateDir, "ws")
	os.RemoveAll(ws)
	pkg := filepath.Join(ws, *pkgRel)
	// Pristine module, then the member's sources, then the submission's tests.
	if err := copyTree(*baseDir, ws, 0o600); err != nil {
		return mr, fmt.Errorf("workspace: %w", err)
	}
	if err := copyTree(filepath.Join(*poolDir, m), pkg, 0o600); err != nil {
		return mr, fmt.Errorf("member sources: %w", err)
	}
	if err := copyTree(sub, pkg, 0o644); err != nil {
		return mr, fmt.Errorf("submission: %w", err)
	}

	// Compile once, as root, into a neutral path the runner can execute.
	bin := filepath.Join(*runRoot, "pkg.test")
	os.Remove(bin)
	ctx, cancel := context.WithTimeout(context.Background(), *buildCap)
	cmd := exec.CommandContext(ctx, "go", "test", "-c", "-race", "-vet=off", "-o", bin, "./"+*pkgRel)
	cmd.Dir = ws
	cmd.Env = buildEnv()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	out, err := cmd.CombinedOutput()
	cancel()
	if err != nil {
		mr.Verdict, mr.Build = "build_failed", tail(string(out), 4000)
		// A submission that does not compile fails every run.
		for _, rp := range runPlan {
			mr.Runs = append(mr.Runs, runResult{Seed: rp.seed, CPU: rp.cpu, Pass: false, ExitCode: -1})
		}
		mr.Credit = mr.Kind == "incorrect"
		return mr, nil
	}
	mr.Built = true
	os.Chmod(bin, 0o755)

	for _, rp := range runPlan {
		rr, err := runOnce(bin, pkg, rp.seed, rp.cpu, isolated)
		if err != nil {
			return mr, err
		}
		mr.Runs = append(mr.Runs, rr)
	}
	os.Remove(bin)

	passes := 0
	for _, r := range mr.Runs {
		if r.Pass {
			passes++
		}
	}
	switch passes {
	case len(mr.Runs):
		mr.Verdict = "accepted"
	case 0:
		mr.Verdict = "rejected"
	default:
		mr.Verdict = "mixed"
	}
	mr.Credit = (mr.Kind == "correct" && mr.Verdict == "accepted") || (mr.Kind == "incorrect" && mr.Verdict == "rejected")
	return mr, nil
}

func buildEnv() []string {
	keep := []string{"PATH", "GOROOT", "GOPATH", "GOMODCACHE", "GOCACHE", "GOFLAGS", "GOPROXY", "GOSUMDB", "GONOSUMDB", "GOTOOLCHAIN", "HOME", "CGO_ENABLED", "CC"}
	var env []string
	for _, k := range keep {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	return env
}

// runOnce executes the compiled test binary once, as the runner, in a fresh directory.
func runOnce(bin, pkg string, seed int64, cpu int, isolated bool) (runResult, error) {
	rr := runResult{Seed: seed, CPU: cpu}
	dir := filepath.Join(*runRoot, "run")
	os.RemoveAll(dir)
	work := filepath.Join(dir, "pkg")
	home := filepath.Join(dir, "home")
	tmp := filepath.Join(dir, "tmp")
	// The runner sees its own tests and testdata, never the implementation's sources.
	for _, d := range []string{work, home, tmp} {
		os.MkdirAll(d, 0o755)
	}
	os.Chmod(dir, 0o755)
	_ = filepath.WalkDir(pkg, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(pkg, p)
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(work, rel), 0o755)
		}
		if strings.HasSuffix(rel, "_test.go") || strings.HasPrefix(rel, "testdata"+string(filepath.Separator)) {
			_, err := copyRegular(p, filepath.Join(work, rel))
			return err
		}
		return nil
	})
	if isolated {
		os.Chown(home, *runnerUID, *runnerGID)
		os.Chown(tmp, *runnerUID, *runnerGID)
		os.Chmod(home, 0o700)
		os.Chmod(tmp, 0o700)
	}

	ctx, cancel := context.WithTimeout(context.Background(), *runCap+20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin,
		"-test.count=1",
		"-test.shuffle="+strconv.FormatInt(seed, 10),
		"-test.cpu="+strconv.Itoa(cpu),
		"-test.timeout="+runCap.String(),
		"-test.v=false")
	cmd.Dir = work
	cmd.Env = []string{"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=" + home, "TMPDIR=" + tmp, "GOTRACEBACK=single"}
	attr := &syscall.SysProcAttr{Setsid: true}
	if isolated {
		// An empty supplementary group list: the runner must not keep the grader's groups.
		attr.Credential = &syscall.Credential{Uid: uint32(*runnerUID), Gid: uint32(*runnerGID), Groups: []uint32{}}
	}
	cmd.SysProcAttr = attr
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	var buf bytes.Buffer
	cmd.Stdout = &limitWriter{w: &buf, n: 1 << 20}
	cmd.Stderr = cmd.Stdout
	cmd.WaitDelay = 5 * time.Second
	start := time.Now()
	if err := cmd.Start(); err != nil {
		os.RemoveAll(dir)
		return rr, fmt.Errorf("cannot start the test binary: %w", err)
	}
	err := cmd.Wait()
	rr.Seconds = time.Since(start).Seconds()
	if ctx.Err() != nil {
		rr.Killed = true
	}
	if err == nil {
		rr.ExitCode = 0
	} else if ee, ok := err.(*exec.ExitError); ok {
		rr.ExitCode = ee.ExitCode()
	} else if !rr.Killed && !errors.Is(err, exec.ErrWaitDelay) {
		os.RemoveAll(dir)
		return rr, fmt.Errorf("waiting for the test binary: %w", err)
	} else {
		rr.ExitCode = -1
	}
	rr.Pass = err == nil && !rr.Killed
	if !rr.Pass {
		rr.Tail = tail(buf.String(), 1500)
	}
	if isolated {
		reap(*runnerUID)
		scrubRunnerFiles(*runnerUID)
	}
	os.RemoveAll(dir)
	return rr, nil
}

type limitWriter struct {
	w io.Writer
	n int
}

func (l *limitWriter) Write(p []byte) (int, error) {
	if l.n <= 0 {
		return len(p), nil
	}
	q := p
	if len(q) > l.n {
		q = q[:l.n]
	}
	l.n -= len(q)
	l.w.Write(q)
	return len(p), nil
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "..." + s[len(s)-n:]
}

// reap kills every process owned by uid, repeating until none is left.
func reap(uid int) int {
	killed := 0
	for pass := 0; pass < 50; pass++ {
		pids := pidsOf(uid)
		if len(pids) == 0 {
			return killed
		}
		for _, pid := range pids {
			if syscall.Kill(pid, syscall.SIGKILL) == nil {
				killed++
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	return killed
}

func pidsOf(uid int) []int {
	ents, _ := os.ReadDir("/proc")
	var pids []int
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		var st syscall.Stat_t
		if syscall.Stat(filepath.Join("/proc", e.Name()), &st) == nil && int(st.Uid) == uid {
			pids = append(pids, pid)
		}
	}
	return pids
}

// scrubRunnerFiles deletes anything the runner managed to leave in shared writable places,
// so no state survives from one run to the next.
func scrubRunnerFiles(uid int) {
	for _, root := range []string{"/tmp", "/var/tmp", "/dev/shm", "/run/lock", "/var/lock"} {
		_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil || p == root {
				return nil
			}
			info, err := os.Lstat(p)
			if err != nil {
				return nil
			}
			if st, ok := info.Sys().(*syscall.Stat_t); ok && int(st.Uid) == uid {
				os.RemoveAll(p)
				if d.IsDir() {
					return filepath.SkipDir
				}
			}
			return nil
		})
	}
}
