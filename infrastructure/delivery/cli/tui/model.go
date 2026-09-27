package tui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	appconversation "github.com/jahrulnr/llm-base/application/conversation"
	"github.com/jahrulnr/llm-base/infrastructure/delivery/cli/commands"
	"github.com/jahrulnr/llm-base/infrastructure/delivery/cli/term"
	"github.com/jahrulnr/llm-base/infrastructure/domain/enum"
	"github.com/jahrulnr/llm-base/infrastructure/domain/event"
	"github.com/jahrulnr/llm-base/infrastructure/domain/service"
	"github.com/jahrulnr/llm-base/infrastructure/domain/valueobject"
	apperror "github.com/jahrulnr/llm-base/infrastructure/error"
)

const composerMaxHeight = 8

// Turn events and command output reach the update loop as typed messages —
// the same dispatch shape codex uses: widgets emit intents, the loop
// applies them, core stays behind channels.
type (
	turnEventMsg struct{ ev event.TurnEvent }
	sendDoneMsg  struct {
		result appconversation.SendResult
		err    error
	}
	// jobDoneMsg ends a worker-bound command — edit, compact — which has
	// no SendResult to fold back into the REPL.
	jobDoneMsg struct{ err error }
)

// replModel is the Elm-style model for the REPL: a live tail for the
// running turn, a status line, the composer, and the slash palette —
// committed output queues in outbox and flushes to scrollback via Println.
type replModel struct {
	repl        *REPL
	commands    *commands.Registry
	textarea    textarea.Model
	palette     slashPalette
	history     []string
	historyIdx  int    // -1 while editing the live draft
	draft       string // live draft stashed while browsing history
	queued      []string
	live        *strings.Builder // pointer: the model is copied per Update
	toolSeen    map[string]struct{}
	toolPreview map[string]int
	turnActive  bool
	turnCancel  context.CancelFunc
	spin        spinner.Model
	phase       string // waiting → thinking → responding → exec <tool>
	runtime     *tuiRuntime
	outbox      []string
	interactive bool // stdin is a terminal
	width       int  // terminal columns; 0 until WindowSizeMsg arrives
	ctx         context.Context
}

func newModel(r *REPL, interactive bool) replModel {
	ta := textarea.New()
	ta.Prompt = "❯ "
	ta.Placeholder = "type a message, / for commands"
	ta.ShowLineNumbers = false
	ta.CharLimit = 0
	ta.SetHeight(1)
	ta.FocusedStyle.CursorLine = ta.FocusedStyle.CursorLine.UnsetBackground()
	ta.KeyMap.InsertNewline = key.NewBinding(key.WithKeys("ctrl+j"))
	ta.Focus()
	registry := commands.New(r.Session)
	spin := spinner.New()
	spin.Spinner = spinner.Dot
	return replModel{
		repl:        r,
		commands:    registry,
		textarea:    ta,
		spin:        spin,
		palette:     slashPalette{describe: registry.Describe, complete: registry.Complete},
		history:     loadHistory(r.historyFile),
		historyIdx:  -1,
		live:        &strings.Builder{},
		toolSeen:    make(map[string]struct{}),
		toolPreview: make(map[string]int),
		interactive: interactive,
	}
}

func (m replModel) Init() tea.Cmd {
	return textarea.Blink
}

func (m replModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.textarea.SetWidth(msg.Width)
		return m, nil
	case turnEventMsg:
		m.applyEvent(msg.ev)
		return m, nil
	case sendDoneMsg:
		return m.finishTurn(msg.result, msg.err)
	case jobDoneMsg:
		return m.finishTurn(appconversation.SendResult{}, msg.err)
	case notifyPing:
		// A session note queued from another goroutine — pull it into the
		// outbox and emit.
		m.drainNotes()
		return m, m.flush()
	case spinner.TickMsg:
		// Forward ticks only while a turn runs — dropping the chain when
		// idle is what stops the animation.
		if !m.turnActive {
			return m, nil
		}
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd
	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	var cmd tea.Cmd
	m.textarea, cmd = m.textarea.Update(msg)
	m.fitComposer()
	return m, cmd
}

// handleKey routes keys through the codex precedence stack: palette/modal
// first, then the interrupt ladder, then history navigation, and only then
// the composer itself.
func (m replModel) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	value := m.textarea.Value()
	// The picker is a TTY affordance — in piped mode \r, \t, and arrow
	// bytes are literal input, and an active palette would make Enter
	// submit the top fuzzy row instead of the scripted buffer.
	var rows []commands.Info
	if m.interactive {
		rows = m.palette.rows(m.ctx, value)
	}

	switch msg.Type {
	case tea.KeyCtrlC:
		switch {
		case rows != nil:
			m.palette.dismiss(value)
			return m, nil
		case value != "":
			m.textarea.Reset()
			m.fitComposer()
			return m, nil
		case m.turnActive:
			if m.turnCancel != nil {
				m.turnCancel()
			}
			return m, nil
		default:
			return m, tea.Quit
		}
	case tea.KeyEscape:
		switch {
		case rows != nil:
			m.palette.dismiss(value)
		case m.turnActive:
			if m.turnCancel != nil {
				m.turnCancel()
			}
		}
		return m, nil
	case tea.KeyCtrlD:
		if value == "" && !m.turnActive {
			return m, tea.Quit
		}
	case tea.KeyTab:
		if rows != nil {
			if m.palette.arg {
				row := rows[m.palette.selected]
				m.textarea.SetValue(row.Usage)
				if !row.FillOnly {
					// A completed leaf stays closed until the next edit;
					// FillOnly rows recompute open — "/attach dir/" lists
					// the next level, "/edit <id> " closes on its own.
					m.palette.dismiss(row.Usage)
				}
			} else {
				m.textarea.SetValue(selectedName(rows, m.palette.selected) + " ")
			}
			m.textarea.CursorEnd()
			m.fitComposer()
			return m, nil
		}
	case tea.KeyEnter:
		if rows != nil {
			if m.palette.arg {
				row := rows[m.palette.selected]
				if row.FillOnly {
					// The row completes part of the command, not all of
					// it — "/edit <id> " waits for replacement text,
					// "/attach dir/" lists the next level on recompute.
					m.textarea.SetValue(row.Usage)
					m.textarea.CursorEnd()
					m.fitComposer()
					return m, nil
				}
				return m.submit(row.Usage)
			}
			return m.submit(selectedName(rows, m.palette.selected))
		}
		return m.submit(value)
	case tea.KeyCtrlJ:
		// In a terminal ctrl+j is the newline key; on piped stdin the byte
		// \n arrives as ctrl+j too and there it means submit — no newline
		// key exists off a TTY.
		if !m.interactive {
			return m.submit(value)
		}
	case tea.KeyUp:
		if rows != nil {
			m.palette.move(-1, rows)
			return m, nil
		}
		// Inside a multi-line composer Up moves the cursor — history
		// browsing starts only from the first line.
		if m.textarea.Line() > 0 {
			var cmd tea.Cmd
			m.textarea, cmd = m.textarea.Update(msg)
			return m, cmd
		}
		return m.historyPrev()
	case tea.KeyDown:
		if rows != nil {
			m.palette.move(1, rows)
			return m, nil
		}
		if m.textarea.Line() < m.textarea.LineCount()-1 {
			var cmd tea.Cmd
			m.textarea, cmd = m.textarea.Update(msg)
			return m, cmd
		}
		return m.historyNext()
	}
	var cmd tea.Cmd
	m.textarea, cmd = m.textarea.Update(msg)
	m.fitComposer()
	return m, cmd
}

// submit parses the line: slash commands dispatch to the registry — /edit
// runs as a turn — and text goes to the running conversation. Input typed
// mid-turn queues and drains when the worker frees up.
func (m replModel) submit(text string) (tea.Model, tea.Cmd) {
	text = strings.TrimSpace(text)
	m.textarea.Reset()
	m.palette.dismissed = "" // a submitted line frees the Esc'd buffer
	m.fitComposer()
	if text == "" {
		return m, nil
	}
	m.pushHistory(text)
	// /cancel is an interrupt, not work — it must reach the registry while
	// a turn is active; everything else waits for the drain boundary.
	if m.turnActive {
		if input := parseInput(text); input.command == "cancel" {
			cmd := m.runCommand("cancel", input.args)
			return m, tea.Sequence(cmd, m.flush())
		}
		m.queued = append(m.queued, text)
		m.println(term.Dim + "queued — runs after this turn" + term.Reset)
		return m, m.flush()
	}
	m.queued = append(m.queued, text)
	cmd := m.drain() // drain before returning — it mutates the queue
	return m, cmd
}

// drain dispatches queued submissions in order until a turn claims the
// worker — commands run inline, a text submission or /edit launches the
// turn and the rest stay queued for the next finishTurn boundary.
func (m *replModel) drain() tea.Cmd {
	var cmds []tea.Cmd
	for len(m.queued) > 0 && !m.turnActive && !m.repl.Exit {
		next := m.queued[0]
		m.queued = m.queued[1:]
		cmd := m.dispatch(next)
		// Output the item produced (command rows, "❯" echo) flushes to
		// scrollback before its own cmd runs — a turn cmd blocks on the
		// provider, so the echo must not wait behind it.
		cmds = append(cmds, m.flush(), cmd)
	}
	cmds = append(cmds, m.flush())
	if m.repl.Exit {
		cmds = append(cmds, tea.Quit)
	}
	return tea.Sequence(cmds...)
}

func (m *replModel) dispatch(text string) tea.Cmd {
	input := parseInput(strings.TrimSpace(text))
	// A bare completable command on a TTY parks at the argument
	// position — the palette becomes the picker (conversations,
	// gateways, models, messages, workspace paths). No candidates
	// falls through to the command's own bare behavior.
	if input.args == "" && m.interactive && m.commands.Completable(input.command) {
		if rows := m.commands.Complete(m.ctx, "/"+input.command+" "); len(rows) > 0 {
			// A queued picker command dispatching at the drain boundary
			// must not destroy an in-progress draft — stash it in
			// history, where ↑ recovers it.
			if draft := m.textarea.Value(); draft != "" {
				m.pushHistory(draft)
			}
			m.textarea.SetValue("/" + input.command + " ")
			m.textarea.CursorEnd()
			m.fitComposer()
			return nil
		}
	}
	switch {
	case input.command == "edit":
		return m.beginEdit(input.args)
	case input.command == "compact":
		return m.beginCompact()
	case input.command != "":
		return m.runCommand(input.command, input.args)
	default:
		// People type "exit" bare at least once; a hint beats a failed turn.
		if input.text == "exit" || input.text == "quit" {
			m.println("type /exit to leave the REPL")
			return nil
		}
		return m.beginTurn(input.text)
	}
}

// println queues output for scrollback — Program.Println blocks inside
// Update (it channel-sends into the event loop), so lines collect in the
// outbox and flush through a Cmd, which runs off the loop. Session notes
// queued since the last line drain first, preserving causal order.
func (m *replModel) println(s string) {
	m.drainNotes()
	m.outbox = append(m.outbox, strings.TrimRight(s, "\n"))
}

// drainNotes pulls queued session notifications into the outbox. The
// session Notify hook can fire on the update loop itself (route
// auto-select inside ResolveRoute) — queueing on the runtime keeps that
// path off the program's unbuffered msg channel.
func (m *replModel) drainNotes() {
	if m.runtime == nil {
		return
	}
	if notes := m.runtime.takeNotes(); len(notes) > 0 {
		m.outbox = append(m.outbox, notes...)
	}
}

func (m *replModel) flush() tea.Cmd {
	if len(m.outbox) == 0 {
		return nil
	}
	batch := m.wrap(strings.Join(m.outbox, "\n"))
	m.outbox = nil
	return tea.Println(batch)
}

func (m *replModel) runCommand(command, args string) tea.Cmd {
	var block strings.Builder
	emitter := service.CommandEmitter(func(chunk string) {
		block.WriteString(chunk)
	})
	err := m.commands.Execute(m.ctx, command, emitter, args)
	// Partial output first, then the error — the command's output is the
	// context the error describes.
	if block.Len() > 0 {
		m.println(block.String())
	}
	if err != nil {
		m.println("error: " + publicError(err))
	}
	if m.repl.ClearQueued {
		m.repl.ClearQueued = false
		return tea.ClearScreen
	}
	return nil
}

// startTurn echoes the user input optimistically — committed before the
// turn resolves — then runs Send off the update loop, pumping turn events
// back in as messages.
func (m *replModel) beginTurn(text string) tea.Cmd {
	if text == "" && len(m.repl.Attachments) == 0 {
		return nil
	}
	// Route/model resolve here on the update loop — auto-selection writes
	// r.route/r.modelID, and a worker-goroutine write would race the
	// status line's reads. The turn itself takes them as arguments.
	route, modelID, err := m.repl.ResolveRoute(m.ctx)
	if err != nil {
		m.println("error: " + publicError(err))
		return nil
	}
	m.println("❯ " + text)
	m.live.Reset()
	m.toolSeen = make(map[string]struct{})
	m.toolPreview = make(map[string]int)
	m.turnActive = true
	m.phase = "waiting"

	turnCtx, cancel := signal.NotifyContext(m.ctx, os.Interrupt)
	m.turnCancel = cancel
	m.repl.CancelTurn = cancel

	pump := turnPump{runtime: m.runtime}
	repl := m.repl
	turn := func() tea.Msg {
		result, err := repl.Send(turnCtx, text, route, modelID, pump.Handle)
		return sendDoneMsg{result: result, err: err}
	}
	// The spinner ticks concurrently — Sequence would hold the first tick
	// hostage behind the provider call.
	return tea.Batch(turn, m.spin.Tick)
}

// beginEdit runs an edit turn on the update loop. Picker UX for bare /edit
// lives here; a full "/edit <id> <text>" line goes through cmdedit via runCommand.
func (m *replModel) beginEdit(args string) tea.Cmd {
	if m.repl.ConversationID == "" {
		m.println("error: no active conversation; use /resume <id>")
		return nil
	}
	messageID, content, ok := valueobject.NewCommand(args).SplitFirst()
	if !ok || strings.TrimSpace(content) == "" {
		m.println("error: usage: /edit <message-id> <replacement text>")
		return nil
	}
	m.println("❯ /edit " + messageID)
	m.live.Reset()
	m.toolSeen = make(map[string]struct{})
	m.toolPreview = make(map[string]int)
	m.turnActive = true
	m.phase = "waiting"

	turnCtx, cancel := signal.NotifyContext(m.ctx, os.Interrupt)
	m.turnCancel = cancel
	m.repl.CancelTurn = cancel

	pump := turnPump{runtime: m.runtime}
	repl, convID := m.repl, m.repl.ConversationID
	turn := func() tea.Msg {
		return jobDoneMsg{err: repl.Edit(turnCtx, convID, messageID, content, pump.Handle)}
	}
	return tea.Batch(turn, m.spin.Tick)
}

// beginCompact runs a manual fold on the room's worker — it produces no
// turn events, so the live region just carries a marker line.
func (m *replModel) beginCompact() tea.Cmd {
	if m.repl.ConversationID == "" {
		m.println("error: no active conversation; use /resume <id>")
		return nil
	}
	m.live.Reset()
	m.toolSeen = make(map[string]struct{})
	m.toolPreview = make(map[string]int)
	m.turnActive = true
	m.phase = "compacting"
	m.println(term.Dim + "compacting…" + term.Reset)

	turnCtx, cancel := signal.NotifyContext(m.ctx, os.Interrupt)
	m.turnCancel = cancel
	m.repl.CancelTurn = cancel

	repl, convID := m.repl, m.repl.ConversationID
	turn := func() tea.Msg {
		return jobDoneMsg{err: repl.Conversations.Compact(turnCtx, convID)}
	}
	return tea.Batch(turn, m.spin.Tick)
}

// finishTurn commits the accumulated live block to scrollback, reports the
// outcome, and drains the queued input exactly one message at a time.
func (m replModel) finishTurn(result appconversation.SendResult, err error) (tea.Model, tea.Cmd) {
	m.turnActive = false
	m.phase = ""
	if m.turnCancel != nil {
		// The job landed — cancel releases the signal.NotifyContext
		// registration instead of leaking one per turn.
		m.turnCancel()
	}
	m.turnCancel = nil
	m.repl.CancelTurn = nil
	if result.UserMessagePersisted {
		m.repl.ConversationID = result.ConversationID
		m.repl.Attachments = nil
	}
	if block := strings.TrimRight(m.live.String(), "\n"); block != "" {
		m.println(block)
	}
	m.live.Reset()
	m.toolSeen = make(map[string]struct{})
	m.toolPreview = make(map[string]int)
	if err != nil {
		m.println("error: " + publicError(err))
	}
	cmd := m.drain()
	return m, cmd
}

// applyEvent folds a turn event into the live region — text deltas append,
// tool calls render as bounded blocks, and a usage line closes the turn.
func (m *replModel) applyEvent(e event.TurnEvent) {
	switch e.Kind() {
	case enum.TurnEventDelta:
		m.phase = "responding"
		m.live.WriteString(term.Sanitize(e.Delta()))
	case enum.TurnEventReasoning:
		// Thinking text renders dimmed inline — the user should see the
		// model is alive during long reasoning, not a frozen frame.
		if m.phase == "waiting" {
			m.phase = "thinking"
		}
		m.live.WriteString(term.Dim + term.Sanitize(e.Delta()) + term.Reset)
	case enum.TurnEventToolCall:
		m.phase = "exec " + e.ToolCall().Name()
		call := e.ToolCall()
		input := call.Input()
		var pretty bytes.Buffer
		if err := json.Indent(&pretty, []byte(call.Input()), "", "  "); err == nil {
			input = pretty.String()
		}
		fmt.Fprintf(m.live, "\n%s[tool %s %s]%s\n%s\n", term.Dim, term.Sanitize(call.Name()), term.Sanitize(call.ID()), term.Reset, term.Sanitize(input))
	case enum.TurnEventToolOutput:
		call := e.ToolCall()
		if _, ok := m.toolSeen[call.ID()]; !ok {
			m.toolSeen[call.ID()] = struct{}{}
			fmt.Fprintf(m.live, "%s[tool output %s]%s\n", term.Dim, term.Sanitize(call.ID()), term.Reset)
		}
		const previewCap = 2048
		if m.toolPreview[call.ID()] >= previewCap {
			return
		}
		delta := e.Delta()
		m.toolPreview[call.ID()] += len(delta)
		if m.toolPreview[call.ID()] > previewCap {
			delta = delta[:previewCap-(m.toolPreview[call.ID()]-len(delta))]
			fmt.Fprintf(m.live, "%s%s…%s", term.Sanitize(delta), term.Dim, term.Reset)
			return
		}
		m.live.WriteString(term.Sanitize(delta))
	case enum.TurnEventFinished:
		usage := e.Usage()
		fmt.Fprintf(m.live,
			"\n%s[usage input=%d cached_read=%d cached_write=%d output=%d total=%d]%s\n",
			term.Dim,
			usage.InputTokens(), usage.CacheReadTokens(), usage.CacheWriteTokens(),
			usage.OutputTokens(), usage.Total(), term.Reset)
	}
}

func (m replModel) historyPrev() (tea.Model, tea.Cmd) {
	if m.textarea.Line() != 0 || m.historyIdx+1 >= len(m.history) {
		return m, nil
	}
	if m.historyIdx == -1 {
		m.draft = m.textarea.Value()
	}
	m.historyIdx++
	m.textarea.SetValue(m.history[len(m.history)-1-m.historyIdx])
	m.textarea.CursorEnd()
	m.fitComposer()
	return m, nil
}

func (m replModel) historyNext() (tea.Model, tea.Cmd) {
	if m.historyIdx == -1 || m.textarea.Line() != m.textarea.LineCount()-1 {
		return m, nil
	}
	m.historyIdx--
	value := m.draft
	if m.historyIdx >= 0 {
		value = m.history[len(m.history)-1-m.historyIdx]
	}
	m.textarea.SetValue(value)
	m.textarea.CursorEnd()
	m.fitComposer()
	return m, nil
}

func (m *replModel) pushHistory(text string) {
	m.history = append(m.history, text)
	m.historyIdx = -1
	_ = appendHistory(m.repl.historyFile, text)
}

func (m *replModel) fitComposer() {
	h := m.textarea.LineCount()
	if h > composerMaxHeight {
		h = composerMaxHeight
	}
	if h < 1 {
		h = 1
	}
	m.textarea.SetHeight(h)
}

// View composes the inline frame: live turn tail, status line, composer,
// palette — committed history lives in scrollback above, never in View.
func (m replModel) View() string {
	var b strings.Builder
	if m.live.Len() > 0 {
		live := m.wrap(m.live.String())
		b.WriteString(live)
		if !strings.HasSuffix(live, "\n") {
			b.WriteByte('\n')
		}
	}
	b.WriteString(m.wrap(m.statusLine()))
	b.WriteByte('\n')
	b.WriteString(m.textarea.View())
	var paletteRows []commands.Info
	if m.interactive {
		paletteRows = m.palette.rows(m.ctx, m.textarea.Value())
	}
	if paletteRows != nil {
		b.WriteByte('\n')
		b.WriteString(m.wrap(m.palette.view(paletteRows)))
	}
	return b.String()
}

// wrap fits text to the terminal width with real newlines. The inline
// renderer counts \n-separated lines to place the frame — a logical line
// the terminal wraps visually is counted once but takes several rows, so
// the next redraw under-clears and the status line lands mid-text. Wrap
// inserts the breaks the renderer's accounting needs. Width 0 (piped, or
// no WindowSizeMsg yet) passes through — nothing is rendering frames.
func (m replModel) wrap(s string) string {
	if m.width <= 0 {
		return s
	}
	return ansi.Wrap(s, m.width, " \t")
}

func (m replModel) statusLine() string {
	var parts []string
	if m.repl.ConversationID != "" {
		parts = append(parts, "conv "+m.repl.ConversationID)
	}
	route, modelID := m.repl.Route, m.repl.ModelID
	if route != "" || modelID != "" {
		parts = append(parts, strings.TrimSpace(route+" / "+modelID))
	}
	if n := len(m.repl.Attachments); n > 0 {
		parts = append(parts, strconv.Itoa(n)+" attached")
	}
	if m.turnActive {
		phase := m.phase
		if phase == "" {
			phase = "running"
		}
		parts = append(parts, m.spin.View()+" "+phase+" — ctrl+c to cancel")
	}
	return term.Dim + strings.Join(parts, " · ") + term.Reset
}

// turnPump forwards synchronous turn events into the message loop —
// Send applies backpressure, matching the TurnEventHandler contract.
type turnPump struct {
	runtime *tuiRuntime
}

func (p turnPump) Handle(e event.TurnEvent) {
	if p.runtime != nil && p.runtime.program != nil {
		p.runtime.program.Send(turnEventMsg{e})
	}
}

// publicError renders an app error for the transcript: the public
// message, plus the internal cause when there is one — "invalid gateway"
// is no help without the reason. Cause-less errors stay clean.
func publicError(err error) string {
	if appErr, ok := errors.AsType[*apperror.AppError](err); ok {
		if appErr.Cause != nil {
			return fmt.Sprintf("%s: %v", appErr.Message, appErr.Cause)
		}
		return appErr.Error()
	}
	return err.Error()
}
