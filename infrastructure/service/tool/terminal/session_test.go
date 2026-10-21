package toolexec

import (
	"context"
	"errors"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	dservice "github.com/jahrulnr/nusallm/infrastructure/domain/service"
)

const testConv = "c_testconv"

func open(t *testing.T, store *SessionStore, req dservice.TerminalOpenRequest) dservice.TerminalSession {
	t.Helper()
	sess, err := store.Open(context.Background(), testConv, req)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { sess.Terminate() })
	return sess
}

func collectUntil(t *testing.T, sess dservice.TerminalSession, want string, maxWait time.Duration) dservice.TerminalPoll {
	t.Helper()
	deadline := time.Now().Add(maxWait)
	var all strings.Builder
	for time.Now().Before(deadline) {
		poll := sess.Collect(context.Background(), 300*time.Millisecond, nil)
		all.WriteString(poll.Output)
		if !poll.Running {
			poll.Output = all.String()
			return poll
		}
		if strings.Contains(all.String(), want) {
			return dservice.TerminalPoll{Output: all.String(), Running: true}
		}
	}
	t.Fatalf("collect: never saw %q — got %q", want, all.String())
	return dservice.TerminalPoll{}
}

func TestSessionExitWithinYield(t *testing.T) {
	store := NewSessionStore()
	defer store.TerminateAll()
	sess := open(t, store, dservice.TerminalOpenRequest{Command: "echo hello && exit 3"})
	poll := collectUntil(t, sess, "hello", 5*time.Second)
	if poll.Running {
		t.Fatal("expected exit")
	}
	if poll.ExitCode != 3 {
		t.Fatalf("exit code = %d, want 3", poll.ExitCode)
	}
	if !strings.Contains(poll.Output, "hello") {
		t.Fatalf("output = %q", poll.Output)
	}
}

func TestSessionSurvivesCallContext(t *testing.T) {
	store := NewSessionStore()
	defer store.TerminateAll()
	sess := open(t, store, dservice.TerminalOpenRequest{Command: "sleep 0.3; echo done"})
	ctx, cancel := context.WithCancel(context.Background())
	pollDone := make(chan dservice.TerminalPoll, 1)
	go func() { pollDone <- sess.Collect(ctx, 10*time.Second, nil) }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	poll := <-pollDone
	if !poll.Running {
		t.Fatal("cancel must not kill the session")
	}
	final := collectUntil(t, sess, "done", 5*time.Second)
	if final.Running || !strings.Contains(final.Output, "done") {
		t.Fatalf("session did not finish after cancel: %+v", final)
	}
}

// TestSessionWriteRoundtrip feeds stdin on a pty — the only backend that
// keeps stdin open.
func TestSessionWriteRoundtrip(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("pty allocation is linux-only for now")
	}
	store := NewSessionStore()
	defer store.TerminateAll()
	sess := open(t, store, dservice.TerminalOpenRequest{Command: "read line; echo got:$line", TTY: true})
	if err := sess.Write([]byte("ping\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	poll := collectUntil(t, sess, "got:ping", 5*time.Second)
	if !strings.Contains(poll.Output, "got:ping") {
		t.Fatalf("output = %q", poll.Output)
	}
}

// TestSessionPipesStdinClosed pins codex's stdin_open=false contract: a
// pipes child reads EOF immediately (a blocked read can never be fed),
// and Write reports ErrTerminalStdinClosed — only the interrupt path
// remains.
func TestSessionPipesStdinClosed(t *testing.T) {
	store := NewSessionStore()
	defer store.TerminateAll()
	fed := open(t, store, dservice.TerminalOpenRequest{Command: "read line; echo eof"})
	poll := collectUntil(t, fed, "eof", 5*time.Second)
	if !strings.Contains(poll.Output, "eof") {
		t.Fatalf("read did not hit closed stdin EOF: %q", poll.Output)
	}
	sess := open(t, store, dservice.TerminalOpenRequest{Command: "sleep 60"})
	if err := sess.Write([]byte("x\n")); !errors.Is(err, dservice.ErrTerminalStdinClosed) {
		t.Fatalf("write on pipes = %v, want ErrTerminalStdinClosed", err)
	}
}

// TestWriteStdinPipesRejectsChars covers the tool-level error the model
// sees: actionable guidance, not just a closed-fd failure.
func TestWriteStdinPipesRejectsChars(t *testing.T) {
	store := NewSessionStore()
	defer store.TerminateAll()
	sess := open(t, store, dservice.TerminalOpenRequest{Command: "sleep 60"})
	tool, err := NewWriteStdinTool(store)
	if err != nil {
		t.Fatal(err)
	}
	ctx := dservice.WithConversationID(context.Background(), testConv)
	call := mustCall(t, "c2", `{"session_id":`+itoa(sess.ID())+`,"chars":"hello"}`, 1<<20)
	if _, err := tool.Execute(ctx, call, nil); err == nil || !strings.Contains(err.Error(), "tty=true") {
		t.Fatalf("expected tty guidance error, got %v", err)
	}
}

// TestInteractQueuedCallSeesReap queues an interaction behind the held
// semaphore, reaps the session, then releases — the queued call must
// observe the registry change and report ErrTerminalSessionGone without
// running fn. Same outcome if the queued call's first lookup lands after
// the reap: either way fn never runs on a stale session.
func TestInteractQueuedCallSeesReap(t *testing.T) {
	store := NewSessionStore()
	defer store.TerminateAll()
	sess := open(t, store, dservice.TerminalOpenRequest{Command: "sleep 60"})
	concrete := store.rooms[testConv][sess.ID()]

	concrete.interaction <- struct{}{}
	fnRan := make(chan struct{}, 1)
	done := make(chan error, 1)
	go func() {
		done <- store.Interact(context.Background(), testConv, sess.ID(), func(dservice.TerminalSession) error {
			fnRan <- struct{}{}
			return nil
		})
	}()
	time.Sleep(50 * time.Millisecond)
	store.Terminate(testConv, sess.ID())
	<-concrete.interaction

	if err := <-done; !errors.Is(err, dservice.ErrTerminalSessionGone) {
		t.Fatalf("interact = %v, want ErrTerminalSessionGone", err)
	}
	select {
	case <-fnRan:
		t.Fatal("fn ran on a reaped session")
	default:
	}
}

// TestInteractQueueHonorsCtx cancels a call parked on the interaction
// semaphore — an interrupted turn must not wait out a long poll.
func TestInteractQueueHonorsCtx(t *testing.T) {
	store := NewSessionStore()
	defer store.TerminateAll()
	sess := open(t, store, dservice.TerminalOpenRequest{Command: "sleep 60"})
	concrete := store.rooms[testConv][sess.ID()]

	concrete.interaction <- struct{}{}
	defer func() { <-concrete.interaction }()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	err := store.Interact(ctx, testConv, sess.ID(), func(dservice.TerminalSession) error { return nil })
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("interact = %v, want ctx deadline", err)
	}
}

// TestInteractSerializes proves the semaphore is real mutual exclusion —
// two concurrent interactions on one session never overlap inside fn.
func TestInteractSerializes(t *testing.T) {
	store := NewSessionStore()
	defer store.TerminateAll()
	sess := open(t, store, dservice.TerminalOpenRequest{Command: "sleep 60"})

	var inFlight atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := store.Interact(context.Background(), testConv, sess.ID(), func(dservice.TerminalSession) error {
				if n := inFlight.Add(1); n != 1 {
					t.Errorf("fn overlapped — %d interactions in flight", n)
				}
				time.Sleep(10 * time.Millisecond)
				inFlight.Add(-1)
				return nil
			})
			if err != nil {
				t.Errorf("interact: %v", err)
			}
		}()
	}
	wg.Wait()
}

// TestInteractIdentityReplaced pins the ptr-equality half of the re-check:
// a call queued on the old session's semaphore must observe the registry
// swap and refuse to run — not just a missing entry, a *different* one.
func TestInteractIdentityReplaced(t *testing.T) {
	store := NewSessionStore()
	defer store.TerminateAll()
	sess := open(t, store, dservice.TerminalOpenRequest{Command: "sleep 60"})
	concrete := store.rooms[testConv][sess.ID()]

	concrete.interaction <- struct{}{}
	fnRan := make(chan struct{}, 1)
	done := make(chan error, 1)
	go func() {
		done <- store.Interact(context.Background(), testConv, sess.ID(), func(dservice.TerminalSession) error {
			fnRan <- struct{}{}
			return nil
		})
	}()
	time.Sleep(50 * time.Millisecond)
	impostor := &terminalSession{id: sess.ID(), interaction: make(chan struct{}, 1)}
	store.mu.Lock()
	store.rooms[testConv][sess.ID()] = impostor
	store.mu.Unlock()
	<-concrete.interaction

	if err := <-done; !errors.Is(err, dservice.ErrTerminalSessionGone) {
		t.Fatalf("interact = %v, want ErrTerminalSessionGone", err)
	}
	select {
	case <-fnRan:
		t.Fatal("fn ran on a session whose registry entry was swapped")
	default:
	}
	// The impostor is a registry fake — drop it before TerminateAll runs.
	store.mu.Lock()
	delete(store.rooms[testConv], sess.ID())
	store.mu.Unlock()
}

// TestPruneSkipsHeldInteraction mirrors codex: eviction leaves a session
// whose interaction semaphore is held — a write/poll in flight is never
// SIGKILLed by the room cap.
func TestPruneSkipsHeldInteraction(t *testing.T) {
	store := NewSessionStore()
	room := map[int]*terminalSession{}
	held := &terminalSession{id: 900, notify: make(chan struct{}, 1), interaction: make(chan struct{}, 1)}
	held.lastUsed.Store(0) // oldest — first eviction candidate
	held.interaction <- struct{}{}
	defer func() { <-held.interaction }()
	room[held.id] = held
	for i := 0; i < maxSessionsPerRoom; i++ {
		s := &terminalSession{id: 1000 + i, notify: make(chan struct{}, 1), interaction: make(chan struct{}, 1)}
		s.lastUsed.Store(int64(i + 1))
		room[s.id] = s
	}
	evicted := store.pruneLocked(room)
	for _, sess := range evicted {
		if sess == held {
			t.Fatal("held session evicted mid-interaction")
		}
	}
	if len(evicted) == 0 {
		t.Fatal("nothing evicted — cap enforcement died with the guard")
	}
	if _, ok := room[held.id]; !ok {
		t.Fatal("held session removed from registry")
	}
}

// TestWriteStdinCtrlCOnExitedCollects pins the codex swallow: an
// interrupt landing on a just-dead session is not an error — the call
// still returns the final screenful.
func TestWriteStdinCtrlCOnExitedCollects(t *testing.T) {
	store := NewSessionStore()
	defer store.TerminateAll()
	sess := open(t, store, dservice.TerminalOpenRequest{Command: "echo bye"})
	deadline := time.Now().Add(5 * time.Second)
	for {
		if exit, _ := sess.Exited(); exit {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("session did not exit")
		}
		time.Sleep(20 * time.Millisecond)
	}
	tool, err := NewWriteStdinTool(store)
	if err != nil {
		t.Fatal(err)
	}
	ctx := dservice.WithConversationID(context.Background(), testConv)
	call := mustCall(t, "c2", `{"session_id":`+itoa(sess.ID())+`,"chars":"\u0003"}`, 1<<20)
	res, err := tool.Execute(ctx, call, nil)
	if err != nil {
		t.Fatalf("interrupt on exited session errored: %v", err)
	}
	if !strings.Contains(res.Text, "Process exited") || !strings.Contains(res.Text, "bye") {
		t.Fatalf("expected final output, got %q", res.Text)
	}
}

// TestSessionEnvScrubs proves the interactive-unfriendly vars are
// overwritten, not merely appended — a parent LANG/PAGER must not leak.
func TestSessionEnvScrubs(t *testing.T) {
	t.Setenv("LANG", "fr_FR.UTF-8")
	t.Setenv("PAGER", "less")
	t.Setenv("TERM", "xterm-256color")
	got := map[string]string{}
	for _, kv := range sessionEnv() {
		k, v, _ := strings.Cut(kv, "=")
		got[k] = v
	}
	for k, want := range map[string]string{
		"TERM": "dumb", "LANG": "C.UTF-8", "LC_ALL": "C.UTF-8",
		"PAGER": "cat", "NO_COLOR": "1", "NUSALLM_CI": "1", "COLORTERM": "",
	} {
		if got[k] != want {
			t.Fatalf("env[%s] = %q, want %q", k, got[k], want)
		}
	}
}

func TestSessionInterruptBySignal(t *testing.T) {
	store := NewSessionStore()
	defer store.TerminateAll()
	sess := open(t, store, dservice.TerminalOpenRequest{Command: "sleep 60"})
	if err := sess.Interrupt(); err != nil {
		t.Fatalf("interrupt: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if exit, _ := sess.Exited(); exit {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("session still alive after SIGINT")
}

func TestTerminateConversationReaps(t *testing.T) {
	store := NewSessionStore()
	defer store.TerminateAll()
	sess := open(t, store, dservice.TerminalOpenRequest{Command: "sleep 60"})
	store.TerminateConversation(testConv)
	store.mu.Lock()
	_, registered := store.rooms[testConv][sess.ID()]
	store.mu.Unlock()
	if registered {
		t.Fatal("session still registered after room reap")
	}
	if exit, _ := sess.Exited(); !exit {
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if exit, _ := sess.Exited(); exit {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatal("process still alive after TerminateConversation")
	}
}

func TestSessionBufferOmission(t *testing.T) {
	sess := &terminalSession{notify: make(chan struct{}, 1)}
	big := strings.Repeat("x", sessionBufferCap+10)
	sess.append([]byte(big))
	out, flowed := sess.drain()
	if !strings.Contains(out, "10 bytes omitted") {
		t.Fatalf("missing omission marker in %q", out[:80])
	}
	if len(out) > sessionBufferCap+128 {
		t.Fatalf("drain exceeded cap: %d", len(out))
	}
	if flowed != sessionBufferCap+10 {
		t.Fatalf("flowed = %d, want retained+omitted = %d", flowed, sessionBufferCap+10)
	}
}

func TestPrunePrefersExited(t *testing.T) {
	store := NewSessionStore()
	room := map[int]*terminalSession{}
	// 63 dead old sessions + one live new one must not evict the live.
	for i := 0; i < maxSessionsPerRoom; i++ {
		s := &terminalSession{id: 1000 + i, notify: make(chan struct{}, 1), interaction: make(chan struct{}, 1)}
		s.exited.Store(true)
		s.lastUsed.Store(int64(i))
		room[s.id] = s
	}
	fresh := &terminalSession{id: 2000, notify: make(chan struct{}, 1), interaction: make(chan struct{}, 1)}
	fresh.lastUsed.Store(int64(maxSessionsPerRoom + 1))
	room[fresh.id] = fresh
	evicted := store.pruneLocked(room)
	if len(evicted) != 1 || evicted[0].id != 1000 {
		t.Fatalf("evicted = %v, want the oldest dead session", evicted)
	}
	if _, ok := room[fresh.id]; !ok {
		t.Fatal("fresh live session evicted")
	}
}

// TestExecSessionDeliversRunningID exercises the tool contract: a
// long-lived command yields the session-id header the model keys on.
func TestExecSessionDeliversRunningID(t *testing.T) {
	store := NewSessionStore()
	defer store.TerminateAll()
	tool, err := NewExecSessionTool(store)
	if err != nil {
		t.Fatal(err)
	}
	ctx := dservice.WithConversationID(context.Background(), testConv)
	call := mustCall(t, "c1", `{"command":"sleep 60"}`, 1<<20)
	res, err := tool.Execute(ctx, call, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Text, "Process running with session ID") {
		t.Fatalf("output = %q", res.Text)
	}
}

// TestExecSessionReportsOriginalBytes pins the pre-truncation count —
// the model must see the scale of what was collected before windowing.
func TestExecSessionReportsOriginalBytes(t *testing.T) {
	store := NewSessionStore()
	defer store.TerminateAll()
	tool, err := NewExecSessionTool(store)
	if err != nil {
		t.Fatal(err)
	}
	ctx := dservice.WithConversationID(context.Background(), testConv)
	call := mustCall(t, "c1", `{"command":"printf hi"}`, 1<<20)
	res, err := tool.Execute(ctx, call, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Text, "Original output: 2 bytes") {
		t.Fatalf("output = %q", res.Text)
	}
}

// TestExecSessionUndeliveredIDKills covers the orphan guard: a canceled
// ctx before the response lands must not leave a live session behind —
// the id never reached the model, so nothing could reap it later.
func TestExecSessionUndeliveredIDKills(t *testing.T) {
	store := NewSessionStore()
	defer store.TerminateAll()
	tool, err := NewExecSessionTool(store)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(dservice.WithConversationID(context.Background(), testConv))
	call := mustCall(t, "c1", `{"command":"sleep 60","yield_time_ms":30000}`, 1<<20)
	done := make(chan error, 1)
	go func() {
		_, err := tool.Execute(ctx, call, nil)
		done <- err
	}()
	time.Sleep(100 * time.Millisecond)
	cancel()
	if err := <-done; err == nil {
		t.Fatal("expected ctx error")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if n := len(store.rooms[testConv]); n != 0 {
		t.Fatalf("%d orphan sessions survived a canceled open", n)
	}
}

// TestWriteStdinCtrlC exercises the \x03 → interrupt mapping on pipes.
func TestWriteStdinCtrlC(t *testing.T) {
	store := NewSessionStore()
	defer store.TerminateAll()
	sess := open(t, store, dservice.TerminalOpenRequest{Command: "sleep 60"})
	tool, err := NewWriteStdinTool(store)
	if err != nil {
		t.Fatal(err)
	}
	ctx := dservice.WithConversationID(context.Background(), testConv)
	call := mustCall(t, "c2", `{"session_id":`+itoa(sess.ID())+`,"chars":"\u0003"}`, 1<<20)
	if _, err := tool.Execute(ctx, call, nil); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if exit, _ := sess.Exited(); exit {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("session survived Ctrl-C")
}

// TestWriteStdinUnknownSession reports a clean lookup failure.
func TestWriteStdinUnknownSession(t *testing.T) {
	store := NewSessionStore()
	defer store.TerminateAll()
	tool, err := NewWriteStdinTool(store)
	if err != nil {
		t.Fatal(err)
	}
	ctx := dservice.WithConversationID(context.Background(), testConv)
	call := mustCall(t, "c2", `{"session_id":4242}`, 1<<20)
	if _, err := tool.Execute(ctx, call, nil); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected not-found error, got %v", err)
	}
}

// TestSessionPTYIsATty proves the pty backend: isatty must answer true —
// the one thing pipes can never do.
func TestSessionPTYIsATty(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("pty allocation is linux-only for now")
	}
	store := NewSessionStore()
	defer store.TerminateAll()
	sess := open(t, store, dservice.TerminalOpenRequest{
		Command: "test -t 0 && echo istty || echo notty",
		TTY:     true,
	})
	if !sess.TTY() {
		t.Fatal("session reports non-tty")
	}
	poll := collectUntil(t, sess, "tty", 5*time.Second)
	if !strings.Contains(poll.Output, "istty") {
		t.Fatalf("expected a real tty, output = %q", poll.Output)
	}
}

// TestSessionPTYCtrlC sends the byte through the line discipline — the
// kernel, not our signal path, delivers SIGINT to the foreground group.
func TestSessionPTYCtrlC(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("pty allocation is linux-only for now")
	}
	store := NewSessionStore()
	defer store.TerminateAll()
	sess := open(t, store, dservice.TerminalOpenRequest{Command: "sleep 60", TTY: true})
	if err := sess.Write([]byte{3}); err != nil {
		t.Fatalf("write ctrl-c: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if exit, _ := sess.Exited(); exit {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("pty session survived Ctrl-C")
}

func itoa(i int) string {
	return strconv.Itoa(i)
}
