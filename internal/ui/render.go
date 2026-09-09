package ui

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/dennisvink/yolomancer/internal/model"
)

type slashCommandDef struct {
	name        string
	description string
}

var slashCommandDefs = []slashCommandDef{
	{"/allow-net", "Remember an allowed network rule, e.g. `/allow-net https://api.example.com` or `/allow-net https://*.example.com`."},
	{"/approvals", "List remembered shell and network approval rules."},
	{"/code", "Switch back to Default implementation mode."},
	{"/compact", "Compact stored chat history and reset the active context baseline."},
	{"/copy", "Copy the latest assistant output to the system clipboard."},
	{"/deny-net", "Remember a denied network rule, e.g. /deny-net https://tracker.example.com."},
	{"/login", "Show the shell command for updating AWS Bedrock credentials."},
	{"/logout", "Clear stored credentials from this machine and this session."},
	{"/permissions", "Update local model permissions for the current workspace."},
	{"/plan", "Switch to Plan mode: explore and produce a proposed plan without editing files."},
	{"/ps", "List running background terminal sessions."},
	{"/stop", "Stop one background terminal by id, or all with `/stop all`."},
	{"/trust", "Mark the current workspace as trusted and relax local shell restrictions."},
	{"/untrust", "Remove the current workspace trust profile and restore stricter defaults."},
	{"/unapprove", "Remove a remembered approval rule by index, e.g. `/unapprove cmd:2` or `/unapprove net:1`."},
}

func (m *Model) renderView() tea.View {
	if m.width <= 0 || m.height <= 0 {
		view := tea.NewView("Starting yolomancer...")
		view.AltScreen = m.altScreen
		return view
	}
	width, height := max(1, m.width), max(1, m.height)
	composerInputHeight := min(max(3, len(visualLines(m.composer.Value(), max(1, width-3)))), max(1, height-1))
	composerHeight := min(height, composerInputHeight+1)
	spaceAbove := max(0, height-composerHeight)
	statusHeight := 0
	if spaceAbove > 0 {
		statusHeight = 1
	}
	paletteHeight := 0
	if strings.HasPrefix(strings.TrimLeft(m.composer.Value(), " \t\r\n"), "/") {
		paletteHeight = max(0, spaceAbove-statusHeight)
	}
	transcriptHeight := max(0, spaceAbove-statusHeight-paletteHeight)

	lines := make([]string, 0, height)
	lines = append(lines, m.renderTranscript(width, transcriptHeight)...)
	if paletteHeight > 0 {
		lines = append(lines, m.renderSlashPalette(width, paletteHeight)...)
	}
	if statusHeight > 0 {
		lines = append(lines, fixedANSI(m.statusLine(), width, lipgloss.NewStyle()))
	}
	composerLines, cursorX, cursorY := m.renderComposer(width, composerInputHeight)
	composerY := len(lines)
	m.composerScreenY = composerY
	lines = append(lines, composerLines...)
	lines = append(lines, fixedANSI(m.composerFooter(), width, mutedStyle))
	for len(lines) < height {
		lines = append(lines, fixedANSI("", width, lipgloss.NewStyle()))
	}
	if len(lines) > height {
		lines = lines[len(lines)-height:]
		composerY = max(0, height-composerHeight)
	}

	if modal := m.modalLines(width, height); len(modal) > 0 {
		lines = overlayCentered(lines, modal, width, height)
	}
	view := tea.NewView(strings.Join(lines, "\n"))
	view.AltScreen = m.altScreen
	// Leave mouse events to the terminal for native text selection and copying.
	view.WindowTitle = m.windowTitle()
	view.BackgroundColor = c64BackgroundRGB
	view.ForegroundColor = c64ForegroundRGB
	if m.approval == nil && m.permissionPrompt < 0 && m.planPrompt < 0 {
		view.Cursor = tea.NewCursor(min(width-1, max(0, cursorX)), min(height-1, composerY+cursorY))
		view.Cursor.Color = c64BackgroundRGB
	}
	return view
}

func (m *Model) windowTitle() string {
	cwd, _ := filepath.Abs(".")
	title := "yolomancer — " + sanitizeTitle(filepath.Base(cwd))
	if m.running {
		frames := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
		return frames[int(time.Now().UnixMilli()/100)%len(frames)] + " " + title
	}
	return title
}

func (m *Model) statusLine() string {
	state := "idle"
	if m.approval != nil {
		state = "approval pending"
	} else if m.running {
		frames := []string{"⠁", "⠂", "⠄", "⠂"}
		state = frames[int(time.Now().UnixMilli()/100)%len(frames)] + " thinking"
	}
	line := fmt.Sprintf("%s  mode=%s  model=Opus", state, m.app.Mode)
	if m.usage != nil {
		line += fmt.Sprintf("  tokens in=%d out=%d", m.usage.InputTokens, m.usage.OutputTokens)
		if m.usage.CacheReadInputTokens > 0 {
			line += fmt.Sprintf(" cache_read=%d", m.usage.CacheReadInputTokens)
		}
		if m.usage.CacheWriteInputTokens > 0 {
			line += fmt.Sprintf(" cache_write=%d", m.usage.CacheWriteInputTokens)
		}
		if m.usage.ReasoningTokens != nil {
			line += fmt.Sprintf(" reasoning=%d", *m.usage.ReasoningTokens)
		}
		line += fmt.Sprintf(" total=%d", m.usage.TotalTokens)
	}
	if !m.transcriptFollow {
		line += "  scroll=manual"
	}
	if m.approval != nil {
		line += "  respond with y/a/n/d/w  arrows+enter to choose"
	}
	return mutedStyle.Render(line)
}

func (m *Model) composerFooter() string {
	if m.planNudgeVisible() {
		return planStyle.Render("Create a plan?") + "  shift + tab use Plan mode   esc dismiss"
	}
	mode := mutedStyle.Render(string(m.app.Mode) + " mode")
	if m.app.Mode == model.ModePlan {
		mode = planStyle.Render("Plan mode")
	}
	return "  " + mode + mutedStyle.Render(" (shift + tab to change)    enter send")
}

func (m *Model) renderComposer(width, height int) ([]string, int, int) {
	textWidth := max(1, width-3)
	m.composer.width = textWidth
	lines, cursorRow := cursorVisualLine(m.composer.Value(), m.composer.cursor, textWidth)
	scroll := max(0, cursorRow-height+1)
	m.composerScroll = scroll
	visible := lines[scroll:min(len(lines), scroll+height)]
	out := make([]string, 0, height)
	for index, line := range visible {
		prefix := "   "
		if index == 0 {
			prefix = composerPanel.Bold(true).Render("›") + composerPanel.Render("  ")
		} else {
			prefix = composerPanel.Render(prefix)
		}
		body := m.renderComposerLine(line.start, line.end)
		out = append(out, fixedANSI(prefix+body, width, composerPanel))
	}
	for len(out) < height {
		out = append(out, fixedANSI("", width, composerPanel))
	}
	cursorCol := utf8.RuneCountInString(m.composer.input[lines[cursorRow].start:m.composer.cursor])
	return out, 3 + cursorCol, cursorRow - scroll
}

func (m *Model) renderComposerLine(start, end int) string {
	if start >= end {
		return ""
	}
	var builder strings.Builder
	position := start
	for position < end {
		var next *markerRange
		for _, candidate := range m.composer.markerRanges() {
			candidate := candidate
			if candidate.end > position && candidate.start < end && (next == nil || candidate.start < next.start) {
				next = &candidate
			}
		}
		if next == nil {
			builder.WriteString(composerPanel.Render(m.composer.input[position:end]))
			break
		}
		if next.start > position {
			plainEnd := min(next.start, end)
			builder.WriteString(composerPanel.Render(m.composer.input[position:plainEnd]))
			position = plainEnd
			continue
		}
		styledEnd := min(next.end, end)
		builder.WriteString(composerPanel.Foreground(c64Black).Bold(true).Render(m.composer.input[position:styledEnd]))
		position = styledEnd
	}
	return builder.String()
}

func (m *Model) renderTranscript(width, height int) []string {
	if height <= 0 {
		m.transcriptLines, m.transcriptViewport = 0, 0
		return nil
	}
	title := "yolomancer"
	if m.app.Mode == model.ModePlan {
		title = planStyle.Render("yolomancer [Plan mode]")
	}
	innerHeight := max(0, height-2)
	all := m.transcriptBodyLines(max(8, width))
	m.transcriptLines, m.transcriptViewport = len(all), innerHeight
	maxScroll := max(0, len(all)-innerHeight)
	if m.transcriptFollow {
		m.transcriptScroll = maxScroll
	} else {
		m.transcriptScroll = min(m.transcriptScroll, maxScroll)
	}
	visible := all[m.transcriptScroll:min(len(all), m.transcriptScroll+innerHeight)]
	out := []string{borderTitle(title, width)}
	for _, line := range visible {
		out = append(out, fixedANSI(line, width, lipgloss.NewStyle()))
	}
	for len(out) < height-1 {
		out = append(out, fixedANSI("", width, lipgloss.NewStyle()))
	}
	out = append(out, strings.Repeat("─", width))
	return out
}

type renderedTranscriptEntry struct {
	entry model.TranscriptEntry
	start int
	lines []string
}

type transcriptRenderCache struct {
	width, markdownWidth int
	entries              []renderedTranscriptEntry
	lines                []string
}

func (m *Model) transcriptBodyLines(width int) []string {
	cache := &m.transcriptCache
	if cache.width != width || cache.markdownWidth != m.width {
		*cache = transcriptRenderCache{width: width, markdownWidth: m.width}
	}
	// Unchanged transcript entries survive keystrokes, timer ticks, and scrolling.
	// Streaming only rerenders the changed entry; appending retains the prefix.
	firstChanged := min(len(cache.entries), len(m.entries))
	for i := 0; i < firstChanged; i++ {
		if cache.entries[i].entry != m.entries[i] {
			firstChanged = i
			break
		}
	}
	if firstChanged < len(cache.entries) {
		cache.lines = cache.lines[:cache.entries[firstChanged].start]
	}
	for i := firstChanged; i < len(m.entries); i++ {
		entry := m.entries[i]
		var rendered renderedTranscriptEntry
		if i < len(cache.entries) && cache.entries[i].entry == entry {
			rendered = cache.entries[i]
		} else {
			rendered = renderedTranscriptEntry{entry: entry, lines: m.renderTranscriptEntry(entry, width)}
		}
		rendered.start = len(cache.lines)
		cache.lines = append(cache.lines, rendered.lines...)
		if i < len(cache.entries) {
			cache.entries[i] = rendered
		} else {
			cache.entries = append(cache.entries, rendered)
		}
	}
	clear(cache.entries[len(m.entries):])
	cache.entries = cache.entries[:len(m.entries)]
	lines := cache.lines
	if m.running {
		lines = append(lines, mutedStyle.Render("• Working ("+compactDuration(time.Since(m.workingStarted))+" • esc to interrupt)"))
	}
	return lines
}

func (m *Model) renderTranscriptEntry(entry model.TranscriptEntry, width int) []string {
	var lines []string
	label, style := entryPresentation(entry.Kind)
	body := m.entryBody(entry)
	bodyLines := strings.Split(ansi.Hardwrap(body, max(8, width-len(label)), true), "\n")
	if len(bodyLines) == 0 {
		bodyLines = []string{""}
	}
	for index, line := range bodyLines {
		prefix := strings.Repeat(" ", len(label))
		if index == 0 {
			prefix = style.Render(label)
		}
		lines = append(lines, prefix+line)
	}
	return lines
}

func entryPresentation(kind model.EntryKind) (string, lipgloss.Style) {
	switch kind {
	case model.EntryUser:
		return "You: ", lipgloss.NewStyle().Foreground(c64Cyan).Bold(true)
	case model.EntryAssistant:
		return "yolomancer: ", lipgloss.NewStyle().Foreground(c64Green).Bold(true)
	case model.EntryReasoning:
		return "Thinking: ", mutedStyle.Bold(true)
	case model.EntryInfo:
		return "System: ", infoStyle.Bold(true)
	case model.EntryQueued:
		return "Queued: ", planStyle
	case model.EntryDebug:
		return "Debug: ", mutedStyle.Bold(true)
	case model.EntryError:
		return "Error: ", errorStyle.Bold(true)
	default:
		return "", mutedStyle
	}
}

func (m *Model) entryBody(entry model.TranscriptEntry) string {
	switch entry.Kind {
	case model.EntryAssistant:
		return m.renderMarkdown(entry.Text)
	case model.EntryTool:
		return renderTool(entry.Text)
	case model.EntryReasoning, model.EntryStatus, model.EntryDebug:
		return mutedStyle.Render(entry.Text)
	case model.EntryError:
		return errorStyle.Render(entry.Text)
	default:
		return entry.Text
	}
}

func (m *Model) renderSlashPalette(width, height int) []string {
	if height <= 0 {
		return nil
	}
	matches := m.slashMatches()
	rows := []string{borderTitle("Commands", width)}
	available := max(1, height-2)
	if len(matches) == 0 {
		rows = append(rows, mutedStyle.Render("No matching slash commands."))
	} else {
		selected := min(m.slashSelection, len(matches)-1)
		start := max(0, min(selected-available/2, len(matches)-available))
		end := min(len(matches), start+available)
		if start > 0 {
			rows = append(rows, mutedStyle.Render(fmt.Sprintf("↑ %d more", start)))
		}
		for index := start; index < end && len(rows) < height-1; index++ {
			name := matches[index]
			prefix, nameStyle := "  ", lipgloss.NewStyle().Foreground(c64White).Bold(true)
			if index == selected {
				prefix, nameStyle = "› ", selectedStyle
			}
			description := slashDescription(name)
			line := prefix + nameStyle.Render(name) + " " + mutedStyle.Render(description)
			rows = append(rows, fixedANSI(line, width, lipgloss.NewStyle()))
		}
		if end < len(matches) && len(rows) < height-1 {
			rows = append(rows, mutedStyle.Render(fmt.Sprintf("↓ %d more", len(matches)-end)))
		}
	}
	for len(rows) < height-1 {
		rows = append(rows, fixedANSI("", width, lipgloss.NewStyle()))
	}
	rows = append(rows, strings.Repeat("─", width))
	return rows[:height]
}

func slashDescription(name string) string {
	for _, definition := range slashCommandDefs {
		if definition.name == name {
			return definition.description
		}
	}
	return ""
}

func (m *Model) modalLines(width, height int) []string {
	switch {
	case m.permissionPrompt >= 0:
		labels := []string{"Default", "Gapped", "Automatic Arbitrage", "Yolo mode"}
		descriptions := []string{
			"yolomancer can read and edit files in the current workspace, run commands, and access the internet. Permission is required to edit other files.",
			"yolomancer can read and edit files in the current workspace, run commands. Approval is required to access the internet or edit other files.",
			"yolomancer can read and edit files in the current workspace and run commands. Network access and other permission requests are judged by an automatic arbiter.",
			"yolomancer can edit files outside this workspace and access the internet without asking for approval. Exercise caution when using.",
		}
		current := permissionModeIndex(m.currentPermissionMode())
		rows := []string{"Current policy: " + labels[current], "Pick a mode and press Enter to apply it.", ""}
		for index, label := range labels {
			marker := " "
			if index == m.permissionPrompt {
				marker = "›"
			}
			currentLabel := ""
			if index == current {
				currentLabel = " (current)"
			}
			rows = append(rows, fmt.Sprintf("%s %d. %s%s", marker, index+1, label, currentLabel), "   "+descriptions[index], "")
		}
		rows = append(rows, "Controls: ↑/↓ move  1/2/3/4 pick  Enter apply  Esc cancel")
		return box("Update Model Permissions", rows, min(128, max(64, width-4)))
	case m.planPrompt >= 0:
		options := [][2]string{{"Yes, implement this plan", "Switch to Default and start coding."}, {"No, stay in Plan mode", "Continue planning with the model."}, {"Exit Plan mode", "Switch to Default without starting work."}}
		var rows []string
		for index, option := range options {
			marker := " "
			if index == m.planPrompt {
				marker = "›"
			}
			rows = append(rows, fmt.Sprintf("%s %d. %s  %s", marker, index+1, option[0], option[1]))
		}
		rows = append(rows, "", "Use ↑/↓ and Enter. Esc keeps Plan mode.")
		return box("Implement this plan?", rows, min(110, max(58, width-8)))
	case m.approval != nil:
		return box("[ APPROVAL REQUIRED ]", m.approvalModalBody(), min(120, max(62, width-8)))
	}
	return nil
}

func (m *Model) currentPermissionMode() string {
	root, _ := filepath.Abs(".")
	if profile, ok := m.app.Config.ProjectProfiles[root]; ok && profile.PermissionMode != nil {
		return *profile.PermissionMode
	}
	return "default"
}

func (m *Model) approvalModalBody() []string {
	r := m.approval.request
	rows := []string{
		"+------------------------------------------------------------+",
		"|                  yolomancer permission request             |",
		"+------------------------------------------------------------+",
		approvalRow("Type     : " + r.Kind),
		approvalRow("Request  : "),
		approvalRow("Target   : " + truncatePlain(firstNonempty(r.Command, r.Workdir), 46)),
		approvalRow("Reason   :"),
	}
	for _, line := range wrapPlain(r.Reason, 54) {
		rows = append(rows, approvalRow("  "+line))
	}
	rows = append(rows, "+------------------------------------------------------------+", approvalRow("Choose action (Arrow keys + Enter):"))
	for index, choice := range m.approvalChoices() {
		marker := " "
		label := choice.label
		if index == m.approvalSelection {
			marker, label = ">", strings.ToUpper(label)
		}
		rows = append(rows, approvalRow(fmt.Sprintf(" %s [%s] %s", marker, choice.hotkey, label)))
	}
	rows = append(rows, "+------------------------------------------------------------+", approvalRow("Shortcuts: Y N A D Esc"))
	if r.Kind == "network access" {
		rows = append(rows, approvalRow("Network extra: W = allow wildcard"))
	}
	return append(rows, "+------------------------------------------------------------+")
}

func approvalRow(value string) string {
	value = truncatePlain(value, 60)
	return "| " + value + strings.Repeat(" ", max(0, 59-utf8.RuneCountInString(value))) + "|"
}

func wrapPlain(value string, width int) []string {
	var out []string
	for _, source := range strings.Split(value, "\n") {
		remaining := strings.TrimSpace(source)
		if remaining == "" {
			out = append(out, "")
			continue
		}
		for utf8.RuneCountInString(remaining) > width {
			runes := []rune(remaining)
			cut := width
			if space := strings.LastIndex(string(runes[:width]), " "); space > 0 {
				cut = utf8.RuneCountInString(string(runes[:space]))
			}
			out = append(out, strings.TrimSpace(string(runes[:cut])))
			remaining = strings.TrimSpace(string(runes[cut:]))
		}
		out = append(out, remaining)
	}
	if len(out) == 0 {
		return []string{""}
	}
	return out
}

func firstNonempty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func box(title string, body []string, width int) []string {
	width = max(4, width)
	inner := width - 2
	top := "┌" + ansi.Truncate(title, max(0, inner), "")
	top += strings.Repeat("─", max(0, width-lipgloss.Width(top)-1)) + "┐"
	lines := []string{top}
	for _, line := range body {
		wrapped := strings.Split(ansi.Hardwrap(line, inner, true), "\n")
		for _, part := range wrapped {
			part = ansi.Truncate(part, inner, "")
			lines = append(lines, "│"+part+strings.Repeat(" ", max(0, inner-lipgloss.Width(part)))+"│")
		}
	}
	lines = append(lines, "└"+strings.Repeat("─", inner)+"┘")
	return lines
}

func overlayCentered(base, modal []string, width, height int) []string {
	if len(modal) == 0 {
		return base
	}
	y := max(0, (height-len(modal))/2)
	for index, line := range modal {
		if y+index >= len(base) {
			break
		}
		line = ansi.Truncate(line, width, "")
		x := max(0, (width-lipgloss.Width(line))/2)
		plain := strings.Repeat(" ", x) + line
		plain += strings.Repeat(" ", max(0, width-lipgloss.Width(plain)))
		base[y+index] = plain
	}
	return base
}

func borderTitle(title string, width int) string {
	if width <= 1 {
		return strings.Repeat("─", width)
	}
	text := "─" + title
	return text + strings.Repeat("─", max(0, width-lipgloss.Width(text)))
}

func fixedANSI(value string, width int, base lipgloss.Style) string {
	value = ansi.Truncate(value, max(0, width), "")
	return value + base.Render(strings.Repeat(" ", max(0, width-lipgloss.Width(value))))
}

func truncatePlain(value string, width int) string {
	if utf8.RuneCountInString(value) <= width {
		return value
	}
	runes := []rune(value)
	if width <= 1 {
		return "…"
	}
	return string(runes[:width-1]) + "…"
}
