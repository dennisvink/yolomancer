package ui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/dennisvink/yolomancer/internal/app"
	appconfig "github.com/dennisvink/yolomancer/internal/config"
	"github.com/dennisvink/yolomancer/internal/model"
	"github.com/dennisvink/yolomancer/internal/security"
	"github.com/dennisvink/yolomancer/internal/session"
	"github.com/dennisvink/yolomancer/internal/tools"
	"github.com/muesli/termenv"
)

var planWordRE = regexp.MustCompile(`(?i)(^|[^[:alnum:]_])plan([^[:alnum:]_]|$)`)

const fencedCodeBlankMarker = "\u2063"

type Model struct {
	app                *app.App
	program            *tea.Program
	composer           composer
	composerScreenY    int
	composerScroll     int
	transcriptScroll   int
	transcriptFollow   bool
	transcriptLines    int
	transcriptViewport int
	transcriptCache    transcriptRenderCache
	altScreen          bool
	width, height      int
	dark               bool
	entries            []model.TranscriptEntry
	history            []string
	historyIndex       int
	draft              string
	queued             []string
	cwdHistory         []string
	slashSelection     int
	planPrompt         int
	planNudgeDismissed bool
	permissionPrompt   int
	permissionRoot     string
	usage              *model.Usage
	running            bool
	interrupted        bool
	workingStarted     time.Time
	streaming          int
	approval           *approvalState
	approvalSelection  int
	activeExploring    int
	exploringOps       []exploringOperation
	pendingExploring   bool
	err                error
	cancel             context.CancelFunc
}
type approvalState struct {
	request  tools.ApprovalRequest
	response chan tools.ApprovalDecision
}
type deltaMsg string
type reasoningMsg string
type assistantMsg string
type doneMsg struct{}
type toolCallMsg model.ToolCall
type toolResultMsg struct {
	call   model.ToolCall
	output string
}
type infoMsg string
type debugMsg string
type usageMsg model.Usage
type turnDoneMsg struct {
	text string
	err  error
}

type compactDoneMsg struct {
	info  string
	usage *model.Usage
	err   error
}
type approvalMsg approvalState
type tickMsg time.Time

func Run(a *app.App, snapshot *model.SessionSnapshot, alt bool) error {
	m := newModel(a, snapshot)
	m.altScreen = alt
	p := tea.NewProgram(m, tea.WithColorProfile(colorprofile.TrueColor))
	m.program = p
	a.Approver = func(ctx context.Context, r tools.ApprovalRequest) (tools.ApprovalDecision, error) {
		ch := make(chan tools.ApprovalDecision, 1)
		p.Send(approvalMsg{r, ch})
		select {
		case d := <-ch:
			return d, nil
		case <-ctx.Done():
			return tools.Deny, ctx.Err()
		}
	}
	final, err := p.Run()
	if err != nil {
		return err
	}
	if completed, ok := final.(*Model); ok {
		completed.save()
	}
	fmt.Printf("Session saved. Resume with: yolomancer resume %s\n", a.SessionID)
	return nil
}

func newModel(a *app.App, s *model.SessionSnapshot) *Model {
	// The fixed C64 theme targets modern RGB terminals. Keep Lip Gloss in
	// sync with Bubble Tea and Glamour instead of approximating palette colors.
	lipgloss.SetColorProfile(termenv.TrueColor)
	m := &Model{app: a, streaming: -1, historyIndex: -1, planPrompt: -1, permissionPrompt: -1, transcriptFollow: true, activeExploring: -1, dark: true}
	if s != nil {
		m.entries = append(m.entries, s.Transcript...)
		for index := range m.entries {
			m.entries[index].Streaming = false
		}
		m.history = append(m.history, s.History...)
		if s.Usage != nil {
			usage := *s.Usage
			m.usage = &usage
		}
		m.cwdHistory = append(m.cwdHistory, s.CWDHistory...)
		if s.CWD != nil {
			m.cwdHistory = appendUnique(m.cwdHistory, *s.CWD)
		}
		if len(m.entries) == 0 {
			m.entries = append(m.entries, model.TranscriptEntry{Kind: model.EntryDebug, Text: "yolomancer interactive mode. Ctrl-C exits; Ctrl-Z suspends."})
		}
		m.entries = append(m.entries, model.TranscriptEntry{Kind: model.EntryInfo, Text: "resumed session " + a.SessionID})
	} else {
		m.entries = append(m.entries, model.TranscriptEntry{Kind: model.EntryDebug, Text: "yolomancer interactive mode. Ctrl-C or :quit exits; Ctrl-Z suspends."})
		if a.Debug {
			m.entries = append(m.entries, model.TranscriptEntry{Kind: model.EntryDebug, Text: "debug enabled; session_id=" + a.SessionID})
		}
	}
	m.refresh()
	return m
}
func (m *Model) Init() tea.Cmd {
	return tea.Batch(
		tick(),
		func() tea.Msg { return tea.RequestBackgroundColor() },
	)
}
func tick() tea.Cmd {
	return tea.Tick(100*time.Millisecond, func(t time.Time) tea.Msg { return tickMsg(t) })
}
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch x := msg.(type) {
	case tickMsg:
		return m, tick()
	case tea.WindowSizeMsg:
		m.width = x.Width
		m.height = x.Height
		m.composer.width = max(1, x.Width-3)
		m.refresh()
		return m, nil
	case tea.BackgroundColorMsg:
		m.dark = x.IsDark()
		return m, nil
	case tea.PasteMsg:
		if !m.running && m.approval == nil && m.permissionPrompt < 0 {
			m.composer.InsertPaste(x.Content)
			m.slashSelection = 0
		}
		return m, nil
	case tea.KeyPressMsg:
		return m.updateKey(x)
	case deltaMsg:
		if m.streaming < 0 {
			m.entries = append(m.entries, model.TranscriptEntry{Kind: model.EntryAssistant, Streaming: true})
			m.streaming = len(m.entries) - 1
		}
		m.entries[m.streaming].Text += string(x)
		m.refresh()
		return m, nil
	case reasoningMsg:
		if len(m.entries) > 0 && m.entries[len(m.entries)-1].Kind == model.EntryReasoning && m.entries[len(m.entries)-1].Streaming {
			m.entries[len(m.entries)-1].Text += string(x)
		} else {
			m.entries = append(m.entries, model.TranscriptEntry{Kind: model.EntryReasoning, Text: string(x), Streaming: true})
		}
		m.refresh()
		return m, nil
	case assistantMsg:
		m.add(model.EntryAssistant, string(x))
		return m, nil
	case doneMsg:
		for i := range m.entries {
			m.entries[i].Streaming = false
		}
		m.streaming = -1
		m.refresh()
		return m, nil
	case toolCallMsg:
		c := model.ToolCall(x)
		if operations := exploringOperations(c); len(operations) > 0 {
			m.pushExploring(operations)
		} else {
			m.activeExploring = -1
			m.exploringOps = nil
			m.add(model.EntryTool, "→ "+c.Name+" "+reasonOrArgs(c))
		}
		if len(m.queued) > 0 && m.running && m.cancel != nil {
			m.cancel()
			m.interrupted = true
			m.add(model.EntryStatus, "■ Conversation interrupted - tell the model what to do differently.")
		}
		return m, nil
	case toolResultMsg:
		if m.pendingExploring {
			m.pendingExploring = false
			if m.activeExploring >= 0 && m.activeExploring < len(m.entries) {
				m.entries[m.activeExploring].Text = exploringDisplay(m.exploringOps, false)
				if !successfulToolSummary(x.output) {
					m.entries[m.activeExploring].Text += "\n  └ " + summarize(x.output)
				}
			}
			m.refresh()
		} else {
			m.add(model.EntryTool, "← "+x.call.Name+" "+summarize(x.output))
		}
		return m, nil
	case infoMsg:
		m.add(model.EntryInfo, string(x))
		return m, nil
	case debugMsg:
		if m.app.Debug {
			m.add(model.EntryDebug, string(x))
		}
		return m, nil
	case usageMsg:
		u := model.Usage(x)
		m.usage = &u
		return m, nil
	case approvalMsg:
		a := approvalState(x)
		m.approval = &a
		m.refresh()
		return m, nil
	case compactDoneMsg:
		if x.usage != nil {
			m.usage = x.usage
		}
		if x.err == nil {
			m.add(model.EntryInfo, x.info)
		}
		return m.Update(turnDoneMsg{err: x.err})
	case turnDoneMsg:
		elapsed := time.Since(m.workingStarted)
		m.running = false
		m.workingStarted = time.Time{}
		m.cancel = nil
		if !m.interrupted {
			m.add(model.EntryStatus, "─ Worked for "+compactDuration(elapsed)+" ─")
		}
		m.interrupted = false
		if x.err != nil && !errors.Is(x.err, context.Canceled) {
			m.add(model.EntryError, x.err.Error())
		}
		m.save()
		if x.err == nil && m.app.Mode == model.ModePlan && m.latestAssistantHasPlan() {
			m.planPrompt = 0
			return m, nil
		}
		if len(m.queued) > 0 {
			next := m.queued[0]
			m.queued = m.queued[1:]
			m.removeQueuedEntry(next)
			return m, m.submit(next)
		}
		return m, nil
	}
	return m, nil
}

func (m *Model) updateKey(message tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key, stroke := message.Key(), message.Keystroke()
	if stroke == "ctrl+c" {
		if m.cancel != nil {
			m.cancel()
		}
		m.save()
		return m, tea.Quit
	}
	if stroke == "ctrl+z" {
		m.save()
		return m, tea.Suspend
	}
	if m.planPrompt >= 0 {
		switch stroke {
		case "up", "left":
			m.planPrompt = max(0, m.planPrompt-1)
		case "down", "right", "tab":
			m.planPrompt = min(2, m.planPrompt+1)
		case "1":
			m.planPrompt = 0
			return m.acceptPlanPrompt()
		case "2", "esc":
			m.planPrompt = -1
		case "3":
			m.planPrompt = 2
			return m.acceptPlanPrompt()
		case "enter":
			return m.acceptPlanPrompt()
		}
		return m, nil
	}
	if m.permissionPrompt >= 0 {
		switch stroke {
		case "up":
			m.permissionPrompt = (m.permissionPrompt + 3) % 4
		case "down":
			m.permissionPrompt = (m.permissionPrompt + 1) % 4
		case "1", "2", "3", "4":
			m.permissionPrompt = int(stroke[0] - '1')
			m.applyPermissionPrompt()
		case "enter":
			m.applyPermissionPrompt()
		case "esc":
			m.permissionPrompt = -1
		}
		return m, nil
	}
	if m.approval != nil {
		return m.handleApprovalKey(message)
	}
	if m.running && stroke == "esc" {
		if m.cancel != nil {
			m.cancel()
		}
		m.interrupted = true
		m.add(model.EntryStatus, "■ Conversation interrupted - tell the model what to do differently.")
		if value := strings.TrimSpace(m.composer.Expanded()); value != "" {
			m.composer.Reset()
			m.queued = append([]string{value}, m.queued...)
		}
		return m, nil
	}
	if !m.running && stroke == "shift+tab" {
		if m.app.Mode == model.ModePlan {
			m.app.SetMode(model.ModeDefault)
			m.add(model.EntryInfo, "Switched to Default mode.")
		} else {
			m.app.SetMode(model.ModePlan)
			m.add(model.EntryInfo, "Switched to Plan mode.")
		}
		m.planNudgeDismissed = false
		return m, nil
	}
	if matches := m.slashMatches(); len(matches) > 0 {
		if stroke == "up" {
			m.slashSelection = (m.slashSelection + len(matches) - 1) % len(matches)
			return m, nil
		}
		if stroke == "down" || stroke == "tab" {
			m.slashSelection = (m.slashSelection + 1) % len(matches)
			return m, nil
		}
	}
	switch stroke {
	case "pgup":
		m.scrollPage(-1)
		return m, nil
	case "pgdown":
		m.scrollPage(1)
		return m, nil
	case "ctrl+u":
		m.scrollHalfPage(-1)
		return m, nil
	case "ctrl+d":
		m.scrollHalfPage(1)
		return m, nil
	case "ctrl+home":
		m.transcriptScroll, m.transcriptFollow = 0, false
		return m, nil
	case "ctrl+end":
		m.scrollEnd()
		return m, nil
	case "left":
		m.composer.MoveLeft()
	case "right":
		m.composer.MoveRight()
	case "alt+left":
		m.composer.MoveWordLeft()
	case "alt+right":
		m.composer.MoveWordRight()
	case "home":
		m.composer.Home()
	case "end":
		m.composer.End()
	case "backspace":
		m.composer.Backspace()
		m.slashSelection = 0
	case "delete":
		m.composer.Delete()
		m.slashSelection = 0
	case "up":
		if !m.composer.MoveVisual(-1) {
			m.historyUp()
		}
	case "down":
		if !m.composer.MoveVisual(1) {
			m.historyDown()
		}
	case "shift+enter", "ctrl+j":
		m.composer.InsertString("\n")
	case "enter":
		return m.submitComposer()
	case "esc":
		if m.planNudgeVisible() {
			m.planNudgeDismissed = true
		} else if !m.running {
			m.composer.Reset()
			m.historyIndex, m.draft = -1, ""
		}
	default:
		if key.Text != "" {
			m.composer.InsertString(key.Text)
			m.slashSelection = 0
		}
	}
	if !planWordRE.MatchString(m.composer.Value()) {
		m.planNudgeDismissed = false
	}
	return m, nil
}

func (m *Model) submitComposer() (tea.Model, tea.Cmd) {
	value := strings.TrimSpace(m.composer.Expanded())
	if value == "" {
		return m, nil
	}
	if matches := m.slashMatches(); len(matches) > 0 {
		trimmed := strings.TrimSpace(m.composer.Value())
		_, suffix, hasArgs := strings.Cut(trimmed, " ")
		value = matches[min(m.slashSelection, len(matches)-1)]
		if hasArgs && strings.TrimSpace(suffix) != "" {
			value += " " + strings.TrimSpace(suffix)
		}
	}
	m.composer.Reset()
	m.planNudgeDismissed = false
	m.historyIndex, m.draft = -1, ""
	if value == ":quit" || value == ":exit" {
		m.save()
		return m, tea.Quit
	}
	if m.running && !strings.HasPrefix(value, "/") {
		m.history = append(m.history, value)
		m.queued = append(m.queued, value)
		m.add(model.EntryQueued, value+"\n\n(queued; press Esc to interrupt and send immediately)")
		return m, nil
	}
	return m, m.submit(value)
}

func (m *Model) removeQueuedEntry(prompt string) {
	for index, entry := range m.entries {
		if entry.Kind == model.EntryQueued && strings.HasPrefix(entry.Text, prompt) {
			m.entries = append(m.entries[:index], m.entries[index+1:]...)
			return
		}
	}
}

func (m *Model) View() tea.View {
	return m.renderView()
}

func (m *Model) acceptPlanPrompt() (tea.Model, tea.Cmd) {
	selected := m.planPrompt
	m.planPrompt = -1
	switch selected {
	case 0:
		m.app.SetMode(model.ModeDefault)
		return m, m.submit("Implement the plan.")
	case 2:
		m.app.SetMode(model.ModeDefault)
		m.add(model.EntryInfo, "Switched to Default mode.")
	}
	return m, nil
}

func (m *Model) latestAssistantHasPlan() bool {
	for i := len(m.entries) - 1; i >= 0; i-- {
		if m.entries[i].Kind == model.EntryAssistant {
			return strings.Contains(m.entries[i].Text, "<proposed_plan>") && strings.Contains(m.entries[i].Text, "</proposed_plan>")
		}
	}
	return false
}

func (m *Model) planNudgeVisible() bool {
	trimmed := strings.TrimLeft(m.composer.Value(), " \t\r\n")
	return m.app.Mode == model.ModeDefault && !m.running && m.approval == nil && m.permissionPrompt < 0 && m.planPrompt < 0 && !m.planNudgeDismissed && !strings.HasPrefix(trimmed, "/") && !strings.HasPrefix(trimmed, "!") && planWordRE.MatchString(m.composer.Value())
}

func (m *Model) slashMatches() []string {
	value := strings.TrimSpace(m.composer.Value())
	if !strings.HasPrefix(value, "/") {
		m.slashSelection = 0
		return nil
	}
	token := strings.ToLower(strings.Fields(value)[0])
	var matches []string
	for _, command := range SlashCommands() {
		if strings.HasPrefix(command, token) {
			matches = append(matches, command)
		}
	}
	if len(matches) == 0 {
		for _, command := range SlashCommands() {
			if strings.Contains(command, token) {
				matches = append(matches, command)
			}
		}
	}
	if m.slashSelection >= len(matches) {
		m.slashSelection = 0
	}
	return matches
}

func (m *Model) submit(value string) tea.Cmd {
	if strings.HasPrefix(value, "/") && knownSlashCommand(strings.Fields(value)[0]) {
		m.slash(value)
		return nil
	}
	m.history = append(m.history, value)
	m.add(model.EntryUser, value)
	m.running = true
	m.interrupted = false
	m.workingStarted = time.Now()
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	return func() tea.Msg {
		text, err := m.app.RunTurn(ctx, value, programSink{m.program})
		return turnDoneMsg{text, err}
	}
}

func (m *Model) slash(raw string) {
	parts := strings.Fields(raw)
	cmd := parts[0]
	args := strings.TrimSpace(strings.TrimPrefix(raw, cmd))
	root, _ := os.Getwd()
	if canonical, err := security.CanonicalMissing(root); err == nil {
		root = canonical
	}
	switch cmd {
	case "/plan":
		m.app.SetMode(model.ModePlan)
		m.add(model.EntryInfo, "Switched to Plan mode.")
	case "/code":
		m.app.SetMode(model.ModeDefault)
		m.add(model.EntryInfo, "Switched to Default mode.")
	case "/trust":
		v := "yolo"
		if m.app.Config.ProjectProfiles == nil {
			m.app.Config.ProjectProfiles = map[string]model.ProjectTrustProfile{}
		}
		m.app.Config.ProjectProfiles[root] = model.ProjectTrustProfile{PermissionMode: &v}
		_ = appconfig.Save(m.app.Config)
		m.add(model.EntryInfo, "Trusted workspace `"+root+"`. Local shell approval and sandbox restrictions are relaxed for this project.")
	case "/untrust":
		if _, exists := m.app.Config.ProjectProfiles[root]; exists {
			delete(m.app.Config.ProjectProfiles, root)
			_ = appconfig.Save(m.app.Config)
			m.add(model.EntryInfo, "Removed trust profile for `"+root+"`. Default local restrictions are active again.")
		} else {
			m.add(model.EntryInfo, "No trust profile was set for `"+root+"`.")
		}
	case "/permissions":
		m.permissions(root, args)
	case "/allow-net", "/deny-net":
		t, err := security.ParseNetworkRule(args)
		if err != nil {
			m.add(model.EntryInfo, "Error: "+err.Error())
			break
		}
		action := model.NetworkAllow
		if cmd == "/deny-net" {
			action = model.NetworkDeny
		}
		rule := model.NetworkApprovalRule{Action: action, Protocol: t.Protocol, Host: t.Host}
		m.app.Config.NetworkApprovalRules = appendUniqueRule(m.app.Config.NetworkApprovalRules, rule)
		if m.app.Config.ProjectProfiles == nil {
			m.app.Config.ProjectProfiles = map[string]model.ProjectTrustProfile{}
		}
		profile := m.app.Config.ProjectProfiles[root]
		profile.NetworkApprovalRules = appendUniqueRule(profile.NetworkApprovalRules, rule)
		m.app.Config.ProjectProfiles[root] = profile
		_ = appconfig.Save(m.app.Config)
		m.add(model.EntryInfo, fmt.Sprintf("Remembered network rule: %s %s://%s", strings.ToLower(string(action)), t.Protocol, t.Host))
	case "/approvals":
		m.showApprovals(args)
	case "/unapprove":
		m.unapprove(args)
	case "/ps":
		rows := m.app.Processes.List()
		if len(rows) == 0 {
			m.add(model.EntryInfo, "No background terminal sessions are running.")
		} else {
			var b strings.Builder
			b.WriteString("Background terminal sessions:\n")
			for _, p := range rows {
				fmt.Fprintf(&b, "%d  %s  running=%s idle=%s cwd=%s\n  %s\n", p.ID, map[bool]string{true: "pty", false: "pipe"}[p.TTY], compactDuration(p.RunningFor), compactDuration(p.IdleFor), p.Workdir, p.Command)
			}
			m.add(model.EntryInfo, strings.TrimSpace(b.String()))
		}
	case "/stop":
		n := 0
		if args == "" || args == "all" {
			n = m.app.Processes.StopAll()
		} else if id, e := strconv.Atoi(args); e == nil && m.app.Processes.Stop(int32(id)) {
			n = 1
		}
		m.add(model.EntryInfo, fmt.Sprintf("Stopped %d background terminal session(s).", n))
	case "/logout":
		m.app.Config.APIKey = ""
		m.app.Config.AWSProfile = nil
		m.app.Config.AWSAccessKeyID = nil
		m.app.Config.AWSSecretAccessKey = nil
		m.app.Config.AWSSessionToken = nil
		m.app.Config.BedrockCredentials = nil
		removed, err := appconfig.Remove()
		if err != nil {
			m.add(model.EntryError, err.Error())
		} else if removed {
			m.add(model.EntryInfo, "Logged out. Stored credentials were removed.")
		} else {
			m.add(model.EntryInfo, "Logged out. No stored config file was present.")
		}
	case "/login":
		m.add(model.EntryInfo, "Run `yolomancer login --profile <aws-profile>` in a shell to update credentials.")
	case "/compact":
		if m.running {
			m.add(model.EntryInfo, "Wait for the current turn to finish, or interrupt it before compacting.")
			return
		}
		m.add(model.EntryStatus, "Compacting session context...")
		ctx, cancel := context.WithCancel(context.Background())
		m.running = true
		m.workingStarted = time.Now()
		m.cancel = cancel
		go func() {
			defer cancel()
			info, usage, err := m.app.Compact(ctx)
			m.program.Send(compactDoneMsg{info, usage, err})
		}()
	case "/copy":
		copied := false
		for i := len(m.entries) - 1; i >= 0; i-- {
			if m.entries[i].Kind == model.EntryAssistant && !m.entries[i].Streaming && strings.TrimSpace(m.entries[i].Text) != "" {
				if err := copyText(m.entries[i].Text); err != nil {
					m.add(model.EntryError, "Clipboard copy failed: "+err.Error())
				} else {
					m.add(model.EntryInfo, "Copied latest assistant response to clipboard")
				}
				copied = true
				break
			}
		}
		if !copied {
			m.add(model.EntryInfo, "No completed assistant response to copy yet")
		}
	default:
		m.add(model.EntryInfo, "Unknown slash command. Available: "+strings.Join(SlashCommands(), ", "))
	}
	m.save()
}

func knownSlashCommand(command string) bool {
	for _, candidate := range SlashCommands() {
		if candidate == command {
			return true
		}
	}
	return false
}

func copyText(value string) error {
	type candidate struct {
		program string
		args    []string
	}
	var candidates []candidate
	switch runtime.GOOS {
	case "darwin":
		candidates = []candidate{{program: "pbcopy"}}
	case "windows":
		candidates = []candidate{{program: "clip"}}
	default:
		candidates = []candidate{{program: "wl-copy"}, {program: "xclip", args: []string{"-selection", "clipboard"}}, {program: "xsel", args: []string{"--clipboard", "--input"}}}
	}
	var last error
	for _, option := range candidates {
		cmd := exec.Command(option.program, option.args...)
		cmd.Stdin = strings.NewReader(value)
		if output, err := cmd.CombinedOutput(); err == nil {
			return nil
		} else {
			last = fmt.Errorf("%s: %s", err, strings.TrimSpace(string(output)))
		}
	}
	if last == nil {
		last = fmt.Errorf("clipboard unavailable")
	}
	return last
}

func SlashCommands() []string {
	return []string{"/allow-net", "/approvals", "/code", "/compact", "/copy", "/deny-net", "/login", "/logout", "/permissions", "/plan", "/ps", "/stop", "/trust", "/untrust", "/unapprove"}
}
func (m *Model) permissions(root, args string) {
	if args == "" {
		m.permissionRoot = root
		m.permissionPrompt = permissionModeIndex(string(security.BuildPolicy(m.app.Config, root).PermissionMode))
		return
	}
	valid := map[string]bool{"default": true, "gapped": true, "automatic-arbitrage": true, "yolo": true}
	if !valid[args] {
		m.add(model.EntryInfo, "Error: unknown permission mode `"+args+"`")
		return
	}
	p := m.app.Config.ProjectProfiles[root]
	p.PermissionMode = ptr(args)
	if m.app.Config.ProjectProfiles == nil {
		m.app.Config.ProjectProfiles = map[string]model.ProjectTrustProfile{}
	}
	m.app.Config.ProjectProfiles[root] = p
	_ = appconfig.Save(m.app.Config)
	m.add(model.EntryInfo, "Updated workspace permissions to "+args+".")
}

func permissionModeIndex(mode string) int {
	switch mode {
	case "gapped":
		return 1
	case "automatic-arbitrage":
		return 2
	case "yolo":
		return 3
	default:
		return 0
	}
}

func (m *Model) applyPermissionPrompt() {
	modes := []string{"default", "gapped", "automatic-arbitrage", "yolo"}
	labels := []string{"Default", "Gapped", "Automatic Arbitrage", "Yolo mode"}
	if m.app.Config.ProjectProfiles == nil {
		m.app.Config.ProjectProfiles = map[string]model.ProjectTrustProfile{}
	}
	profile := m.app.Config.ProjectProfiles[m.permissionRoot]
	profile.PermissionMode = ptr(modes[m.permissionPrompt])
	m.app.Config.ProjectProfiles[m.permissionRoot] = profile
	_ = appconfig.Save(m.app.Config)
	m.add(model.EntryInfo, "Updated model permissions: "+labels[m.permissionPrompt])
	m.permissionPrompt = -1
}
func (m *Model) showApprovals(filter string) {
	if filter != "" && filter != "all" && filter != "cmd" && filter != "net" {
		m.add(model.EntryError, "usage: /approvals [all|cmd|net]")
		return
	}
	var b strings.Builder
	if filter == "" || filter == "all" || filter == "cmd" {
		for i, r := range m.app.Config.CommandApprovalRules {
			fmt.Fprintf(&b, "cmd:%d %s\n", i+1, strings.Join(r.Prefix, " "))
		}
	}
	if filter == "" || filter == "all" || filter == "net" {
		for i, r := range m.app.Config.NetworkApprovalRules {
			fmt.Fprintf(&b, "net:%d %s %s://%s\n", i+1, strings.ToLower(string(r.Action)), r.Protocol, r.Host)
		}
	}
	if b.Len() == 0 {
		b.WriteString("No remembered approval rules.")
	}
	m.add(model.EntryInfo, strings.TrimSpace(b.String()))
}
func (m *Model) unapprove(raw string) {
	kind, indexRaw, ok := strings.Cut(raw, ":")
	if !ok {
		kind = "cmd"
		indexRaw = raw
	}
	idx, err := strconv.Atoi(indexRaw)
	if err != nil || idx < 1 {
		m.add(model.EntryInfo, "Error: usage: /unapprove cmd:<index> or net:<index>")
		return
	}
	if kind == "cmd" && idx <= len(m.app.Config.CommandApprovalRules) {
		m.app.Config.CommandApprovalRules = append(m.app.Config.CommandApprovalRules[:idx-1], m.app.Config.CommandApprovalRules[idx:]...)
	} else if kind == "net" && idx <= len(m.app.Config.NetworkApprovalRules) {
		m.app.Config.NetworkApprovalRules = append(m.app.Config.NetworkApprovalRules[:idx-1], m.app.Config.NetworkApprovalRules[idx:]...)
	} else {
		m.add(model.EntryInfo, "Error: approval index out of range")
		return
	}
	_ = appconfig.Save(m.app.Config)
	m.add(model.EntryInfo, "Removed remembered approval rule.")
}
func (m *Model) handleApprovalKey(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	var d tools.ApprovalDecision
	choices := m.approvalChoices()
	switch k.Keystroke() {
	case "up", "left":
		m.approvalSelection = (m.approvalSelection + len(choices) - 1) % len(choices)
		return m, nil
	case "down", "right":
		m.approvalSelection = (m.approvalSelection + 1) % len(choices)
		return m, nil
	case "pgup", "ctrl+u":
		m.scrollHalfPage(-1)
		return m, nil
	case "pgdown", "ctrl+d":
		m.scrollHalfPage(1)
		return m, nil
	case "enter":
		d = choices[min(m.approvalSelection, len(choices)-1)].decision
	case "y", "Y":
		d = tools.ApproveOnce
	case "a", "A":
		d = tools.ApproveRemember
	case "w", "W":
		if m.approval.request.Kind != "network access" {
			return m, nil
		}
		d = tools.ApproveRememberWildcard
	case "d", "D":
		if m.approval.request.Kind == "network access" {
			d = tools.DenyRemember
		} else {
			d = tools.Deny
		}
	case "n", "N", "esc":
		d = tools.Deny
	default:
		return m, nil
	}
	m.approval.response <- d
	m.approval = nil
	m.approvalSelection = 0
	m.refresh()
	return m, nil
}

type approvalChoice struct {
	hotkey   string
	label    string
	decision tools.ApprovalDecision
}

func (m *Model) approvalChoices() []approvalChoice {
	if m.approval != nil && m.approval.request.Kind == "network access" {
		return []approvalChoice{{"Y", "Allow Once", tools.ApproveOnce}, {"A", "Allow Always", tools.ApproveRemember}, {"W", "Allow *.domain", tools.ApproveRememberWildcard}, {"D", "Deny Always", tools.DenyRemember}, {"N", "Deny Once", tools.Deny}}
	}
	return []approvalChoice{{"Y", "Allow Once", tools.ApproveOnce}, {"A", "Allow Always", tools.ApproveRemember}, {"N", "Deny Once", tools.Deny}}
}
func (m *Model) add(k model.EntryKind, text string) {
	if k != model.EntryTool {
		m.activeExploring = -1
		m.exploringOps = nil
	}
	m.entries = append(m.entries, model.TranscriptEntry{Kind: k, Text: text})
	m.refresh()
}

func (m *Model) pushExploring(operations []exploringOperation) {
	if m.activeExploring >= 0 && m.activeExploring < len(m.entries) && strings.HasPrefix(m.entries[m.activeExploring].Text, "• Explor") {
		m.exploringOps = append(m.exploringOps, operations...)
		m.entries[m.activeExploring].Text = exploringDisplay(m.exploringOps, true)
	} else {
		m.exploringOps = append([]exploringOperation(nil), operations...)
		m.entries = append(m.entries, model.TranscriptEntry{Kind: model.EntryTool, Text: exploringDisplay(m.exploringOps, true)})
		m.activeExploring = len(m.entries) - 1
	}
	m.pendingExploring = true
	m.refresh()
}
func (m *Model) refresh() {
	if m.transcriptFollow {
		m.scrollEnd()
	}
}

func (m *Model) scrollEnd() {
	m.transcriptScroll = max(0, m.transcriptLines-m.transcriptViewport)
	m.transcriptFollow = true
}

func (m *Model) scrollBy(delta int) {
	maximum := max(0, m.transcriptLines-m.transcriptViewport)
	m.transcriptScroll = min(max(0, m.transcriptScroll+delta), maximum)
	m.transcriptFollow = m.transcriptScroll >= maximum
}

func (m *Model) scrollPage(direction int) {
	m.scrollBy(direction * max(1, m.transcriptViewport))
	if direction < 0 {
		m.transcriptFollow = false
	}
}

func (m *Model) scrollHalfPage(direction int) {
	m.scrollBy(direction * max(1, (m.transcriptViewport+1)/2))
	if direction < 0 {
		m.transcriptFollow = false
	}
}
func (m *Model) renderMarkdown(text string) string {
	if start := strings.Index(text, "<proposed_plan>"); start >= 0 {
		if end := strings.Index(text, "</proposed_plan>"); end > start {
			text = "## Proposed Plan\n\n" + strings.TrimSpace(text[start+len("<proposed_plan>"):end])
		}
	}
	markdownStyle := c64MarkdownStyle()
	// Glamour's stock document style adds leading/trailing rows and its
	// preserved-newline mode keeps source line endings in addition to Markdown's
	// own block layout. Together those made normal model-authored lists appear
	// double-spaced. Render semantic Markdown blocks compactly instead.
	zero := uint(0)
	markdownStyle.Document.BlockPrefix = ""
	markdownStyle.Document.BlockSuffix = ""
	markdownStyle.Document.Margin = &zero
	markdownStyle.Heading.BlockSuffix = ""
	renderer, err := glamour.NewTermRenderer(glamour.WithColorProfile(termenv.TrueColor), glamour.WithStyles(markdownStyle), glamour.WithWordWrap(max(20, m.width-2)), glamour.WithTableWrap(true))
	if err != nil {
		return assistantStyle.Render(text)
	}
	rendered, err := renderer.Render(protectFencedCodeBlankRows(text))
	if err != nil {
		return assistantStyle.Render(text)
	}
	return compactMarkdownRows(rendered)
}

func compactMarkdownRows(rendered string) string {
	rows := strings.Split(strings.TrimRight(rendered, "\n"), "\n")
	compact := rows[:0]
	for _, row := range rows {
		// Markdown blank lines separate source blocks; discard empty display rows
		// so Glamour does not turn them into visible gaps.
		if strings.TrimSpace(ansi.Strip(row)) == "" {
			continue
		}
		// Glamour pads every line with ANSI-colored trailing spaces to the
		// word-wrap width. When transcriptBodyLines later applies
		// ansi.Hardwrap at a narrower width (adjusted for the entry label),
		// those padding spaces overflow onto a new line, producing spurious
		// blank rows. Strip the cosmetic trailing whitespace here so that
		// Hardwrap sees only the actual content.
		compact = append(compact, stripTrailingANSIPadding(row))
	}
	return strings.ReplaceAll(strings.Join(compact, "\n"), fencedCodeBlankMarker, "")
}

// stripTrailingANSIPadding computes the visual width of the line without
// trailing whitespace and truncates the ANSI string to that width. Glamour
// pads every line to the configured word-wrap width with individually colored
// spaces; this removes them while preserving all other ANSI styling.
func stripTrailingANSIPadding(row string) string {
	stripped := ansi.Strip(row)
	trimmed := strings.TrimRight(stripped, " \t")
	contentWidth := lipgloss.Width(trimmed)
	if contentWidth >= lipgloss.Width(row) {
		return row
	}
	return ansi.Truncate(row, contentWidth, "")
}

func protectFencedCodeBlankRows(markdown string) string {
	rows := strings.Split(markdown, "\n")
	fence := byte(0)
	for index, row := range rows {
		trimmed := strings.TrimSpace(row)
		if len(trimmed) >= 3 && (strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~")) {
			marker := trimmed[0]
			if fence == 0 {
				fence = marker
			} else if fence == marker {
				fence = 0
			}
			continue
		}
		if fence != 0 && trimmed == "" {
			rows[index] = fencedCodeBlankMarker
		}
	}
	return strings.Join(rows, "\n")
}
func (m *Model) save() {
	cwd, _ := os.Getwd()
	m.cwdHistory = appendUnique(m.cwdHistory, cwd)
	s := model.SessionSnapshot{Version: 1, SessionID: m.app.SessionID, UpdatedAtUnix: uint64(time.Now().Unix()), CWD: &cwd, CWDHistory: m.cwdHistory, BedrockMessages: m.app.RawMessages(), Transcript: m.entries, History: m.history, Usage: m.usage, ContextBudget: m.app.ContextBudget(), CollaborationMode: m.app.Mode}
	_ = session.Write(&s)
}

func (m *Model) historyUp() {
	if len(m.history) == 0 {
		return
	}
	if m.historyIndex < 0 {
		m.draft = m.composer.Value()
		m.historyIndex = len(m.history) - 1
	} else if m.historyIndex > 0 {
		m.historyIndex--
	}
	m.composer.SetValue(m.history[m.historyIndex])
	m.composer.CursorEnd()
}
func (m *Model) historyDown() {
	if m.historyIndex < 0 {
		return
	}
	if m.historyIndex < len(m.history)-1 {
		m.historyIndex++
		m.composer.SetValue(m.history[m.historyIndex])
	} else {
		m.historyIndex = -1
		m.composer.SetValue(m.draft)
	}
	m.composer.CursorEnd()
}

type programSink struct{ p *tea.Program }

func (s programSink) ReasoningDelta(v string)   { s.p.Send(reasoningMsg(v)) }
func (s programSink) AssistantDelta(v string)   { s.p.Send(deltaMsg(v)) }
func (s programSink) AssistantMessage(v string) { s.p.Send(assistantMsg(v)) }
func (s programSink) AssistantDone()            { s.p.Send(doneMsg{}) }
func (s programSink) ToolCall(v model.ToolCall) { s.p.Send(toolCallMsg(v)) }
func (s programSink) ToolResult(c model.ToolCall, v string) {
	s.p.Send(toolResultMsg{c, toolResultSummary(v)})
}
func (s programSink) Info(v string)       { s.p.Send(infoMsg(v)) }
func (s programSink) Debug(v string)      { s.p.Send(debugMsg(v)) }
func (s programSink) Usage(v model.Usage) { s.p.Send(usageMsg(v)) }
func reasonOrArgs(c model.ToolCall) string {
	if s, ok := c.Arguments["reason"].(string); ok {
		return s
	}
	if c.Name == "write_file" {
		return fmt.Sprintf("path=%v", c.Arguments["path"])
	}
	return summarize(marshal(c.Arguments))
}
func summarize(v string) string {
	v = strings.ReplaceAll(strings.TrimSpace(v), "\n", " ")
	if len([]rune(v)) > 240 {
		return string([]rune(v)[:240]) + "…"
	}
	return v
}
func toolResultSummary(raw string) string {
	var v map[string]any
	if json.Unmarshal([]byte(raw), &v) != nil {
		return summarize(raw)
	}
	if ok, exists := v["ok"].(bool); exists && !ok {
		return "error: " + fmt.Sprint(v["error"])
	}
	if diff, ok := v["diff"].(string); ok && diff != "" {
		return fmt.Sprintf("edited %v\n%s", v["path"], diff)
	}
	if edit, ok := v["edit"].(map[string]any); ok {
		if diff, _ := edit["diff"].(string); diff != "" {
			return fmt.Sprintf("edited %v\n%s", v["path"], diff)
		}
		return fmt.Sprintf("edited %v (+%v -%v)", v["path"], edit["added"], edit["removed"])
	}
	if out, ok := v["output"].(string); ok {
		if strings.TrimSpace(out) == "" {
			return "command completed with no output"
		}
		return out
	}
	if content, ok := v["content"].(string); ok {
		return fmt.Sprintf("read %v (%d bytes)", v["path"], len(content))
	}
	return summarize(raw)
}
func renderTool(text string) string {
	var b strings.Builder
	for i, line := range strings.Split(text, "\n") {
		style := toolStyle
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "+") && !strings.HasPrefix(trim, "+++") {
			style = lipgloss.NewStyle().Foreground(c64Green)
		} else if strings.HasPrefix(trim, "-") && !strings.HasPrefix(trim, "---") {
			style = lipgloss.NewStyle().Foreground(c64Red)
		} else if strings.HasPrefix(trim, "@@") {
			style = lipgloss.NewStyle().Foreground(c64Cyan).Bold(true)
		}
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(style.Render(line))
	}
	return b.String()
}
func marshal(v any) string { return fmt.Sprintf("%v", v) }
func ptr(v string) *string { return &v }
func appendUnique(v []string, x string) []string {
	for _, s := range v {
		if s == x {
			return v
		}
	}
	return append(v, x)
}
func appendUniqueRule(rules []model.NetworkApprovalRule, rule model.NetworkApprovalRule) []model.NetworkApprovalRule {
	for _, existing := range rules {
		if existing == rule {
			return rules
		}
	}
	return append(rules, rule)
}
func sanitizeTitle(v string) string {
	var b strings.Builder
	pendingSpace := false
	count := 0
	for _, r := range v {
		if r < 32 || r == 127 || invisibleFormatRune(r) {
			continue
		}
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			pendingSpace = b.Len() > 0
			continue
		}
		if pendingSpace {
			b.WriteByte(' ')
			count++
			pendingSpace = false
		}
		b.WriteRune(r)
		count++
		if count >= 240 {
			break
		}
	}
	if b.Len() == 0 {
		return "workspace"
	}
	return b.String()
}
func invisibleFormatRune(r rune) bool {
	return r == '\u061c' || r >= '\u200b' && r <= '\u200f' || r >= '\u202a' && r <= '\u202e' || r >= '\u2060' && r <= '\u206f' || r == '\ufeff'
}
func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
func compactDuration(d time.Duration) string {
	seconds := int(d.Seconds())
	if seconds < 60 {
		return fmt.Sprintf("%ds", seconds)
	}
	if seconds < 3600 {
		return fmt.Sprintf("%dm %02ds", seconds/60, seconds%60)
	}
	return fmt.Sprintf("%dh %02dm %02ds", seconds/3600, seconds%3600/60, seconds%60)
}
