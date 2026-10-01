package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	appconversation "github.com/jahrulnr/nusallm/application/conversation"
	"github.com/jahrulnr/nusallm/infrastructure/delivery/cli/commands"
	"github.com/jahrulnr/nusallm/infrastructure/delivery/cli/term"
	"github.com/jahrulnr/nusallm/infrastructure/domain/entity"
	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
	"github.com/jahrulnr/nusallm/infrastructure/domain/event"
	"github.com/jahrulnr/nusallm/infrastructure/domain/service"
	"github.com/jahrulnr/nusallm/infrastructure/domain/valueobject"
	apperror "github.com/jahrulnr/nusallm/infrastructure/error"
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
	// no SendResult to fold back into the REPL; out carries text the job
	// emitted (the compact confirmation) to commit ahead of the error.
	jobDoneMsg struct {
		out string
		err error
	}
)

// replModel is the Elm-style model for the REPL: a live tail for the
// running turn, a status line, the composer, and the slash palette —
// committed output queues in outbox and flushes to scrollback via Println.
type replModel struct {
	repl            *REPL
	commands        *commands.Registry
	textarea        textarea.Model
	palette         slashPalette
	history         []string
	historyIdx      int    // -1 while editing the live draft
	draft           string // live draft stashed while browsing history
	queued          []string
	live            *strings.Builder // in-flight assistant delta not yet sealed
	liveTimeline    []livePiece      // turn transcript in TurnEvent arrival order
	contextEstimate int              // footer ~estimate/total input budget
	contextTotal    int
	turnActive      bool
	turnCancel      context.CancelFunc
	spin            spinner.Model
	phase           string // waiting → thinking → responding → exec <tool> | <slash job>
	runtime         *tuiRuntime
	outbox          []string
	pendingPrints   int  // scrollback batches not yet taken by the loop
	interactive     bool // stdin is a terminal
	width           int  // terminal columns; 0 until WindowSizeMsg arrives
	ctx             context.Context
}

// livePiece is one sealed slice of the turn timeline. Order is event
// order only; render walks the slice once.
type livePiece struct {
	markdown   string
	line       string
	reasoning  string
	toolOutput string
	toolCallID string
	toolName   string
}

func newModel(r *REPL, interactive bool) replModel {
	ta := textarea.New()
	ta.SetPromptFunc(ansi.StringWidth(composerPrompt), func(line int) string {
		if line == 0 {
			return composerPrompt
		}
		return ""
	})
	ta.Placeholder = "Ask anything, / for commands"
	ta.ShowLineNumbers = false
	ta.CharLimit = 0
	ta.SetHeight(1)
	styleComposer(&ta)
	ta.KeyMap.InsertNewline = key.NewBinding(key.WithKeys("alt+enter"))
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
		history:     r.historyStore.Load(r.historyFile),
		historyIdx:  -1,
		live:        &strings.Builder{},
		interactive: interactive,
	}
}

func (m replModel) Init() tea.Cmd {
	return textarea.Blink
}

func (m replModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		// The terminal reflows the full-width composer rows on a width
		// change, so the inline renderer's cursor-up no longer reaches the
		// old frame top and stale rows stay on screen. Repaint from a clean
		// screen instead; the first size report arrives before any frame.
		var cmd tea.Cmd
		if m.width > 0 && msg.Width != m.width {
			cmd = tea.ClearScreen
		}
		m.width = msg.Width
		m.textarea.SetWidth(composerInputWidth(msg.Width))
		m.fitComposer()
		return m, cmd
	case turnEventMsg:
		m.applyEvent(msg.ev)
		return m, nil
	case sendDoneMsg:
		return m.finishTurn(msg.result, msg.err)
	case jobDoneMsg:
		if msg.out != "" {
			m.println(msg.out)
		}
		return m.finishTurn(appconversation.SendResult{}, msg.err)
	case notifyPing:
		// A session note queued from another goroutine — pull it into the
		// outbox and emit.
		m.drainNotes()
		return m, m.flush()
	case printAckMsg:
		if m.pendingPrints > 0 {
			m.pendingPrints--
		}
		// An exit waiting on scrollback: the last line landed, take the
		// session down.
		if m.repl.Exit && m.pendingPrints == 0 {
			return m, tea.Quit
		}
		return m, nil
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
	return m, m.editComposer(msg)
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
			m.repl.Exit = true
			return m, m.quitOrWait()
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
			m.repl.Exit = true
			return m, m.quitOrWait()
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
		if msg.Alt && m.interactive {
			return m, m.editComposer(msg)
		}
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
		// Piped stdin maps \n to ctrl+j — submit, not a composer newline.
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
	return m, m.editComposer(msg)
}

// submit parses the line: slash commands dispatch to the registry —
// worker-bound ones (/edit, /compact) run as turns — and text goes to
// the running conversation. Input typed mid-turn queues and drains when
// the worker frees up.
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
// worker — commands run inline, a text submission or a job command
// (/edit, /compact) launches the turn and the rest stay queued for the
// next finishTurn boundary.
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
		// An exit waits for scrollback still in flight — quitOrWait
		// returns nil and printAckMsg finishes the quit once the last
		// line landed.
		cmds = append(cmds, m.quitOrWait())
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
	case input.command != "":
		// Worker-bound commands (/edit, /compact) resolve through the
		// registry like every other slash command — Job validates and
		// binds the closure, this loop runs it as a turn so events
		// stream and ctrl+c interrupts. Inline and unknown commands go
		// to Execute.
		job, isJob, err := m.commands.Job(input.command, input.args)
		if err != nil {
			m.println("error: " + publicError(err))
			return nil
		}
		if isJob {
			return m.beginJob(input.command, input.args, job)
		}
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

// printAckMsg reports that a scrollback batch reached the event loop. The
// flush cmd emits it after tea.Println's own message, which the program
// takes in order — so an exit can wait for the last line instead of racing
// it (a quit that overtakes a pending print drops it: the loop returns on
// QuitMsg and never handles the print).
type printAckMsg struct{}

// flush hands the outbox to the renderer as scrollback and tracks the
// batch until the loop has taken it; an exit requested in the same breath
// is finished by the ack, not by tea.Quit.
func (m *replModel) flush() tea.Cmd {
	if len(m.outbox) == 0 {
		return nil
	}
	batch := m.wrap(strings.Join(m.outbox, "\n"))
	m.outbox = nil
	m.pendingPrints++
	return tea.Sequence(tea.Println(batch), func() tea.Msg { return printAckMsg{} })
}

// quitOrWait returns tea.Quit unless scrollback output is still in flight;
// the ack of the last batch finishes the exit then.
func (m *replModel) quitOrWait() tea.Cmd {
	if m.pendingPrints > 0 {
		return nil
	}
	return tea.Quit
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

func (m *replModel) runCommand(command, args string) tea.Cmd {
	var block strings.Builder
	emitter := service.CommandEmitter(func(chunk string) {
		block.WriteString(chunk)
	})
	err := m.commands.Execute(m.ctx, command, emitter, args)
	// Partial output first, then the error — the command's output is the
	// context the error describes.
	if block.Len() > 0 {
		m.println(m.renderCommandOutput(command, block.String()))
	}
	if err != nil {
		m.println("error: " + publicError(err))
	}
	if m.repl.ClearQueued {
		m.repl.ClearQueued = false
		m.refreshContextUsage()
		return tea.ClearScreen
	}
	m.refreshContextUsage()
	return nil
}

func (m replModel) renderCommandOutput(command, output string) string {
	switch command {
	case "history":
		return renderMarkdown(output, m.width)
	case "resume":
		header, transcript, ok := strings.Cut(output, "\n")
		if !ok {
			return output
		}
		return strings.TrimRight(header, "\n") + "\n" + renderMarkdown(transcript, m.width)
	default:
		return output
	}
}

// startTurn echoes the user input optimistically — committed before the
// turn resolves — then runs Send off the update loop, pumping turn events
// back in as messages.
func (m *replModel) beginTurn(text string) tea.Cmd {
	// Route/model resolve here on the update loop — auto-selection writes
	// r.route/r.modelID, and a worker-goroutine write would race the
	// status line's reads. The turn itself takes them as arguments.
	route, modelID, err := m.repl.ResolveRoute(m.ctx)
	if err != nil {
		m.println("error: " + publicError(err))
		return nil
	}
	m.println(term.Cyan + term.Bold + "> " + term.Sanitize(text) + term.Reset)
	m.resetLive()
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

// beginJob runs a worker-bound slash command — /edit, /compact — the way
// a text turn runs: the registry's Job already validated the line and
// bound the work; this loop echoes it, then runs the closure off the
// loop with turn events pumped back in and the emitter's text committed
// when the job lands.
func (m *replModel) beginJob(command, args string, job commands.JobFunc) tea.Cmd {
	line := "/" + command
	if args != "" {
		line += " " + args
	}
	m.println(term.Cyan + term.Bold + "> " + term.Sanitize(line) + term.Reset)
	m.resetLive()
	m.turnActive = true
	m.phase = command

	turnCtx, cancel := signal.NotifyContext(m.ctx, os.Interrupt)
	m.turnCancel = cancel
	m.repl.CancelTurn = cancel

	pump := turnPump{runtime: m.runtime}
	var block strings.Builder
	emitter := service.CommandEmitter(func(chunk string) {
		block.WriteString(chunk)
	})
	turn := func() tea.Msg {
		err := job(turnCtx, emitter, pump.Handle)
		return jobDoneMsg{out: block.String(), err: err}
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
		m.refreshContextUsage()
	}
	if block := m.renderLive(); block != "" {
		m.println(block)
	}
	m.resetLive()
	if err != nil {
		m.println("error: " + publicError(err))
	}
	cmd := m.drain()
	m.refreshContextUsage()
	return m, cmd
}

func (m *replModel) refreshContextUsage() {
	m.contextEstimate = 0
	m.contextTotal = 0
	if m.repl == nil || m.repl.Conversations == nil || m.repl.ConversationID == "" {
		return
	}
	ctx := m.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	route, modelID := m.repl.EffectiveRouteModel(ctx)
	stats, err := m.repl.Conversations.ContextFooterStats(ctx, m.repl.ConversationID, route, modelID)
	if err != nil {
		return
	}
	m.contextEstimate = stats.Estimate
	m.contextTotal = stats.Total
}

func (m *replModel) resetLive() {
	m.live.Reset()
	m.liveTimeline = nil
}

func (m *replModel) sealLiveMarkdown() {
	if m.live.Len() == 0 {
		m.live.Reset()
		return
	}
	m.liveTimeline = append(m.liveTimeline, livePiece{markdown: m.live.String()})
	m.live.Reset()
}

func (m *replModel) appendLiveLine(line string) {
	if line == "" {
		return
	}
	m.liveTimeline = append(m.liveTimeline, livePiece{line: line})
}

// renderLive walks the turn timeline in event order.
func (m replModel) renderLive() string {
	var b strings.Builder
	write := func(block string) {
		if block == "" {
			return
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(block)
	}
	for _, piece := range m.liveTimeline {
		if piece.markdown != "" {
			write(renderMarkdown(piece.markdown, m.width))
		}
		if piece.line != "" {
			write(piece.line)
		}
		if piece.reasoning != "" {
			write(piece.reasoning)
		}
		if piece.toolOutput != "" {
			write(term.Dim + "↳ " + term.Sanitize(piece.toolName) + ": " + piece.toolOutput + term.Reset)
		}
	}
	if m.live.Len() > 0 {
		write(renderMarkdown(m.live.String(), m.width))
	}
	return b.String()
}

// applyEvent folds a turn event into the live region in arrival order.
func (m *replModel) applyEvent(e event.TurnEvent) {
	switch e.Kind() {
	case enum.TurnEventDelta:
		m.phase = "responding"
		m.live.WriteString(term.Sanitize(e.Delta()))
	case enum.TurnEventReasoning, enum.TurnEventReasoningSummary:
		m.phase = "thinking"
		m.sealLiveMarkdown()
		text := term.Sanitize(e.Delta())
		if text != "" {
			if len(m.liveTimeline) > 0 && m.liveTimeline[len(m.liveTimeline)-1].reasoning != "" {
				m.liveTimeline[len(m.liveTimeline)-1].reasoning += text
			} else {
				m.liveTimeline = append(m.liveTimeline, livePiece{reasoning: text})
			}
		}
	case enum.TurnEventToolCall:
		m.phase = "exec " + e.ToolCall().Name()
		m.sealLiveMarkdown()
		m.appendLiveLine(formatToolActivityRow(e.ToolCall()))
	case enum.TurnEventToolOutput:
		m.sealLiveMarkdown()
		call := e.ToolCall()
		text := term.Sanitize(e.Delta())
		if text != "" {
			if len(m.liveTimeline) > 0 {
				last := &m.liveTimeline[len(m.liveTimeline)-1]
				if last.toolCallID == call.ID() && last.toolOutput != "" {
					last.toolOutput += text
					break
				}
			}
			m.liveTimeline = append(m.liveTimeline, livePiece{
				toolOutput: text, toolCallID: call.ID(), toolName: call.Name(),
			})
		}
	case enum.TurnEventFinished:
		m.sealLiveMarkdown()
		m.appendLiveLine(term.Dim + "↳ " + formatUsage(e.Usage()) + term.Reset)
	}
}

func formatToolActivityRow(call entity.ToolCall) string {
	row := fmt.Sprintf("%s%s⏺ %s%s", term.Yellow, term.Bold, term.Sanitize(call.Name()), term.Reset)
	row += " " + term.Dim + term.Sanitize(call.Input()) + term.Reset
	return row
}

func truncateInline(text string, max int) string {
	if max < 2 {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= max {
		return text
	}
	return string(runes[:max-1]) + "…"
}

func formatUsage(usage valueobject.Usage) string {
	summary := fmt.Sprintf("%d in · %d out · %d total", usage.InputTokens(), usage.OutputTokens(), usage.Total())
	if usage.CacheReadTokens() != 0 {
		summary += fmt.Sprintf(" · %d cached read", usage.CacheReadTokens())
	}
	if usage.CacheWriteTokens() != 0 {
		summary += fmt.Sprintf(" · %d cached write", usage.CacheWriteTokens())
	}
	return summary
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
	_ = m.repl.historyStore.Append(m.repl.historyFile, text)
}

// editComposer forwards an edit to the textarea at full height, then fits
// it. The textarea scrolls its viewport during Update using the current
// height; editing at the fitted height would scroll a new line into a
// one-row view and leave the first line hidden after the height grows.
func (m *replModel) editComposer(msg tea.Msg) tea.Cmd {
	m.textarea.SetHeight(composerMaxHeight)
	var cmd tea.Cmd
	m.textarea, cmd = m.textarea.Update(msg)
	m.fitComposer()
	return cmd
}

func (m *replModel) fitComposer() {
	h := composerDisplayRows(m.textarea)
	if h > composerMaxHeight {
		h = composerMaxHeight
	}
	if h < 1 {
		h = 1
	}
	m.textarea.SetHeight(h)
}

// View composes the inline frame: live turn tail, composer card, footer
// badges, palette — committed history lives in scrollback above, never in
// View.
func (m replModel) View() string {
	var b strings.Builder
	if live := m.renderLive(); live != "" {
		live := m.wrap(live)
		b.WriteString(live)
		if !strings.HasSuffix(live, "\n") {
			b.WriteByte('\n')
		}
		b.WriteByte('\n')
	}
	var paletteRows []commands.Info
	if m.interactive {
		paletteRows = m.palette.rows(m.ctx, m.textarea.Value())
	}
	if status := m.renderTurnStatus(); status != "" {
		b.WriteString(status)
		b.WriteByte('\n')
	}
	b.WriteString(m.renderComposer())
	b.WriteByte('\n')
	b.WriteString(m.renderFooter(paletteRows != nil))
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
