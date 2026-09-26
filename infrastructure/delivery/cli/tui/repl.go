// Package tui is the interactive terminal boundary: the Bubble Tea model,
// composer, slash palette, input history, and the Run loop that drives a
// session.Session. The REPL type is the composition point cmd/ wires to.
package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"

	tea "github.com/charmbracelet/bubbletea"
	appconversation "github.com/jahrulnr/llm-base/application/conversation"
	appgateway "github.com/jahrulnr/llm-base/application/gateway"
	"github.com/jahrulnr/llm-base/infrastructure/delivery/cli/session"
	"github.com/jahrulnr/llm-base/infrastructure/delivery/cli/term"
)

// Options wires the REPL to the use cases, its workspace, and the terminal.
type Options struct {
	Conversations *appconversation.ConversationApp
	Gateways      *appgateway.GatewayApp
	Workspace     string
	HistoryDir    string
	Stdin         io.ReadCloser
	Stdout        io.Writer
	Stderr        io.Writer
}

// REPL composes the session state with the terminal handles the update
// loop needs — history persistence plus stdin/stdout. Session is embedded
// so the model and tests reach session fields directly.
type REPL struct {
	*session.Session
	historyFile string
	stdin       io.ReadCloser
	stdout      io.Writer
}

// NewREPL binds a REPL to the use cases, workspace root, and private
// history file. Relative attachment paths are confined to the workspace.
func NewREPL(opts Options) (*REPL, error) {
	if opts.HistoryDir == "" {
		return nil, errors.New("history directory is required")
	}
	sess, err := session.New(session.Options{
		Conversations: opts.Conversations,
		Gateways:      opts.Gateways,
		Workspace:     opts.Workspace,
		Stderr:        opts.Stderr,
	})
	if err != nil {
		return nil, err
	}
	historyFile, err := privateHistoryFile(opts.HistoryDir)
	if err != nil {
		_ = sess.Close()
		return nil, err
	}
	if opts.Stdin == nil {
		opts.Stdin = os.Stdin
	}
	if opts.Stdout == nil {
		opts.Stdout = os.Stdout
	}
	return &REPL{
		Session:     sess,
		historyFile: historyFile,
		stdin:       opts.Stdin,
		stdout:      opts.Stdout,
	}, nil
}

// Run serves the interactive terminal UI until /exit, Ctrl+D at an empty
// composer, or parent cancellation. Ctrl+C follows the codex ladder:
// dismiss the palette, clear the draft, cancel the running turn, quit.
func (r *REPL) Run(ctx context.Context) error {
	if ctx == nil {
		return errors.New("context is required")
	}
	m := newModel(r, isTerminal(r.stdin))
	m.ctx = ctx
	r.Interactive = m.interactive
	runtime := &tuiRuntime{}
	m.runtime = runtime
	if m.interactive {
		r.Notify = func(s string) { runtime.note(term.Dim + s + term.Reset) }
		r.printWelcome(ctx)
	}
	// Piped stdin ending without /exit must end the session — the input
	// read loop stops delivering messages on EOF but the program itself
	// would idle forever. EOF feeds a synthetic "\n/exit\n" through the
	// stream instead: pending text submits, queued work drains, and the
	// exit command takes the session down through the normal path.
	program := tea.NewProgram(m,
		tea.WithInput(&eofInput{rc: r.stdin, inject: []byte("\n/exit\n")}),
		tea.WithOutput(r.stdout),
		tea.WithContext(ctx),
	)
	runtime.program = program
	_, err := program.Run()
	if ctx.Err() != nil {
		return nil
	}
	if err != nil && (errors.Is(err, tea.ErrProgramKilled) || errors.Is(err, tea.ErrInterrupted)) {
		// ErrProgramKilled = programmatic quit; ErrInterrupted = an
		// external SIGINT — same ladder Ctrl+C walks, exit cleanly.
		return nil
	}
	return err
}

// eofInput translates input EOF into an injected suffix — the TUI passes
// "\n/exit\n" so a piped session that ends mid-queue still submits any
// unterminated line, drains its work, and exits through the same path a
// typed /exit would take.
//
// It must satisfy term.File: bubbletea only calls MakeRaw on inputs with
// a Fd(). Without it the tty stays cooked — the kernel echoes each key
// and the renderer wipes it, so typing looks swallowed letter by letter.
type eofInput struct {
	rc      io.ReadCloser
	inject  []byte
	pending []byte
	eofed   bool
}

func (e *eofInput) Read(p []byte) (int, error) {
	if !e.eofed {
		n, err := e.rc.Read(p)
		if err != io.EOF {
			return n, err
		}
		e.eofed = true
		e.pending = append([]byte{}, e.inject...)
		if n > 0 {
			// Deliver the trailing bytes now — the inject serves on the
			// next read. EOF itself is never reported while the inject
			// is outstanding: returning it would end the reader loop
			// before "/exit" reaches the parser.
			return n, nil
		}
	}
	if len(e.pending) > 0 {
		n := copy(p, e.pending)
		e.pending = e.pending[n:]
		return n, nil
	}
	return 0, io.EOF
}

func (e *eofInput) Close() error { return e.rc.Close() }

// Fd exposes the wrapped descriptor so bubbletea recognizes the input as
// a terminal file and switches it to raw mode. A non-file source reports
// an invalid descriptor — IsTerminal fails and nothing is made raw.
func (e *eofInput) Fd() uintptr {
	if f, ok := e.rc.(interface{ Fd() uintptr }); ok {
		return f.Fd()
	}
	return ^uintptr(0)
}

// Write satisfies term.File — input is never written to in practice.
func (e *eofInput) Write(p []byte) (int, error) {
	if w, ok := e.rc.(io.Writer); ok {
		return w.Write(p)
	}
	return 0, errors.New("input is not writable")
}

// printWelcome commits the startup banner to scrollback before the inline
// frame opens — hermes/openclaw-style: brand line plus the operator's
// current surface, and the setup path when no provider exists yet.
func (r *REPL) printWelcome(ctx context.Context) {
	fmt.Fprintf(r.stdout, "%score%s — provider-agnostic llm repl\n", term.Bold, term.Reset)
	gateways, err := r.Gateways.List(ctx)
	if err != nil || len(gateways) == 0 {
		fmt.Fprint(r.stdout, term.Dim+`no gateways configured yet — set one up:
  core gateway add <id> --style completion --endpoint <url> --models <m> --key-stdin
  or run the wizard: core onboard
`+term.Reset)
	} else {
		ready := 0
		for _, g := range gateways {
			if !g.Broken {
				ready++
			}
		}
		fmt.Fprintf(r.stdout, "%s%d gateway(s), %d ready%s\n", term.Dim, len(gateways), ready, term.Reset)
	}
	fmt.Fprintln(r.stdout, term.Dim+"/ for commands · /help for details · ctrl+c interrupts a turn"+term.Reset)
}

// isTerminal reports whether the input is a character device — pipe-fed
// input flips the model into non-interactive mode where \n submits.
func isTerminal(in io.ReadCloser) bool {
	f, ok := in.(*os.File)
	if !ok {
		return false
	}
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

// tuiRuntime holds the running program so handlers and the pump can reach
// it lazily — the model is constructed before the program exists. The
// notes queue lets notifications arrive from any goroutine, including the
// update loop itself.
type tuiRuntime struct {
	program *tea.Program
	mu      sync.Mutex
	notes   []string
}

// notifyPing wakes the update loop so it drains queued session notes.
type notifyPing struct{}

// note queues a scrollback line and pings the loop. It must never call
// Program.Println or Program.Send synchronously: p.msgs is unbuffered and
// only the update loop reads it — a Notify fired inside Update (the route
// auto-select in ResolveRoute runs there) would block the loop on a send
// to itself. That self-deadlock froze the REPL on the first submit.
func (rt *tuiRuntime) note(s string) {
	rt.mu.Lock()
	rt.notes = append(rt.notes, s)
	rt.mu.Unlock()
	if rt.program != nil {
		// Send is safe off a goroutine: it escapes on ctx.Done, and the
		// loop draining notes does not depend on this ping when the note
		// arrived mid-update.
		go rt.program.Send(notifyPing{})
	}
}

func (rt *tuiRuntime) takeNotes() []string {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	notes := rt.notes
	rt.notes = nil
	return notes
}
