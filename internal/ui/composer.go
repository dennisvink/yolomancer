package ui

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	collapsedPasteCharThreshold = 800
	collapsedPasteLineThreshold = 8
)

type pastedBlock struct {
	marker  string
	content string
}

type visualLine struct {
	start int
	end   int
}

type markerRange struct {
	start  int
	end    int
	marker string
}

type composer struct {
	input  string
	cursor int
	pastes []pastedBlock
	width  int
}

func (c *composer) Value() string { return c.input }

func (c *composer) SetValue(value string) {
	c.input = value
	c.cursor = len(value)
	c.pastes = nil
}

func (c *composer) Reset() {
	c.input = ""
	c.cursor = 0
	c.pastes = nil
}

func (c *composer) CursorEnd() { c.cursor = len(c.input) }

func (c *composer) InsertString(value string) {
	if value == "" {
		return
	}
	c.cursor = clampCursor(c.input, c.cursor)
	c.input = c.input[:c.cursor] + value + c.input[c.cursor:]
	c.cursor += len(value)
}

func (c *composer) InsertPaste(value string) {
	value = strings.ReplaceAll(strings.ReplaceAll(value, "\r\n", "\n"), "\r", "\n")
	charCount := utf8.RuneCountInString(value)
	lineCount := strings.Count(value, "\n") + 1
	if charCount < collapsedPasteCharThreshold && lineCount < collapsedPasteLineThreshold {
		c.InsertString(value)
		return
	}
	base := "[Pasted Content " + itoa(charCount) + " chars]"
	marker := base
	for suffix := 2; c.hasMarker(marker); suffix++ {
		marker = base + " #" + itoa(suffix)
	}
	c.pastes = append(c.pastes, pastedBlock{marker: marker, content: value})
	c.InsertString(marker)
}

func (c *composer) hasMarker(marker string) bool {
	if strings.Contains(c.input, marker) {
		return true
	}
	for _, block := range c.pastes {
		if block.marker == marker {
			return true
		}
	}
	return false
}

func (c *composer) Expanded() string {
	value := c.input
	for _, block := range c.pastes {
		value = strings.ReplaceAll(value, block.marker, block.content)
	}
	return value
}

func (c *composer) ClearPastesNotInInput() {
	kept := c.pastes[:0]
	for _, block := range c.pastes {
		if strings.Contains(c.input, block.marker) {
			kept = append(kept, block)
		}
	}
	c.pastes = kept
}

func (c *composer) Backspace() {
	if c.cursor <= 0 {
		return
	}
	if start, end, marker, ok := c.markerBeforeOrContaining(c.cursor); ok {
		c.input = c.input[:start] + c.input[end:]
		c.cursor = start
		c.removePaste(marker)
		return
	}
	previous := previousBoundary(c.input, c.cursor)
	c.input = c.input[:previous] + c.input[c.cursor:]
	c.cursor = previous
}

func (c *composer) Delete() {
	if c.cursor >= len(c.input) {
		return
	}
	if start, end, marker, ok := c.markerAfterOrContaining(c.cursor); ok {
		c.input = c.input[:start] + c.input[end:]
		c.cursor = start
		c.removePaste(marker)
		return
	}
	next := nextBoundary(c.input, c.cursor)
	c.input = c.input[:c.cursor] + c.input[next:]
}

func (c *composer) MoveLeft() {
	if c.cursor <= 0 {
		return
	}
	if start, _, _, ok := c.markerBeforeOrContaining(c.cursor); ok {
		c.cursor = start
		return
	}
	c.cursor = previousBoundary(c.input, c.cursor)
	if start, _, _, ok := c.markerContaining(c.cursor); ok {
		c.cursor = start
	}
}

func (c *composer) MoveRight() {
	if c.cursor >= len(c.input) {
		return
	}
	if _, end, _, ok := c.markerAfterOrContaining(c.cursor); ok {
		c.cursor = end
		return
	}
	c.cursor = nextBoundary(c.input, c.cursor)
	if _, end, _, ok := c.markerContaining(c.cursor); ok {
		c.cursor = end
	}
}

func (c *composer) MoveWordLeft()  { c.cursor = previousWordBoundary(c.input, c.cursor) }
func (c *composer) MoveWordRight() { c.cursor = nextWordBoundary(c.input, c.cursor) }

func (c *composer) Home() {
	lines, row := cursorVisualLine(c.input, c.cursor, c.wrapWidth())
	if row >= 0 {
		c.cursor = lines[row].start
	}
}

func (c *composer) End() {
	lines, row := cursorVisualLine(c.input, c.cursor, c.wrapWidth())
	if row >= 0 {
		c.cursor = lines[row].end
	}
}

func (c *composer) MoveVisual(delta int) bool {
	lines, row := cursorVisualLine(c.input, c.cursor, c.wrapWidth())
	if row < 0 || row+delta < 0 || row+delta >= len(lines) {
		return false
	}
	column := utf8.RuneCountInString(c.input[lines[row].start:c.cursor])
	target := lines[row+delta]
	c.cursor = byteIndexAtRuneColumn(c.input, target.start, target.end, column)
	if start, end, _, ok := c.markerContaining(c.cursor); ok {
		if delta < 0 {
			c.cursor = start
		} else {
			c.cursor = end
		}
	}
	return true
}

func (c *composer) wrapWidth() int {
	if c.width < 1 {
		return 1
	}
	return c.width
}

func (c *composer) removePaste(marker string) {
	kept := c.pastes[:0]
	for _, block := range c.pastes {
		if block.marker != marker {
			kept = append(kept, block)
		}
	}
	c.pastes = kept
}

func (c *composer) markerRanges() []markerRange {
	var ranges []markerRange
	for _, block := range c.pastes {
		for offset := 0; offset <= len(c.input); {
			index := strings.Index(c.input[offset:], block.marker)
			if index < 0 {
				break
			}
			start := offset + index
			ranges = append(ranges, markerRange{start: start, end: start + len(block.marker), marker: block.marker})
			offset = start + len(block.marker)
		}
	}
	return ranges
}

func (c *composer) markerBeforeOrContaining(cursor int) (int, int, string, bool) {
	for _, item := range c.markerRanges() {
		start, end, marker := item.start, item.end, item.marker
		if end == cursor || start < cursor && cursor < end {
			return start, end, marker, true
		}
	}
	return 0, 0, "", false
}

func (c *composer) markerAfterOrContaining(cursor int) (int, int, string, bool) {
	for _, item := range c.markerRanges() {
		start, end, marker := item.start, item.end, item.marker
		if start == cursor || start < cursor && cursor < end {
			return start, end, marker, true
		}
	}
	return 0, 0, "", false
}

func (c *composer) markerContaining(cursor int) (int, int, string, bool) {
	for _, item := range c.markerRanges() {
		start, end, marker := item.start, item.end, item.marker
		if start < cursor && cursor < end {
			return start, end, marker, true
		}
	}
	return 0, 0, "", false
}

func previousBoundary(text string, index int) int {
	index = clampCursor(text, index)
	if index == 0 {
		return 0
	}
	_, size := utf8.DecodeLastRuneInString(text[:index])
	return index - size
}

func nextBoundary(text string, index int) int {
	index = clampCursor(text, index)
	if index >= len(text) {
		return len(text)
	}
	_, size := utf8.DecodeRuneInString(text[index:])
	return index + size
}

func clampCursor(text string, index int) int {
	index = min(max(index, 0), len(text))
	for index > 0 && index < len(text) && !utf8.RuneStart(text[index]) {
		index--
	}
	return index
}

func previousWordBoundary(text string, index int) int {
	position := clampCursor(text, index)
	for position > 0 {
		previous := previousBoundary(text, position)
		r, _ := utf8.DecodeRuneInString(text[previous:position])
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' {
			break
		}
		position = previous
	}
	for position > 0 {
		previous := previousBoundary(text, position)
		r, _ := utf8.DecodeRuneInString(text[previous:position])
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' {
			break
		}
		position = previous
	}
	return position
}

func nextWordBoundary(text string, index int) int {
	position := clampCursor(text, index)
	for position < len(text) {
		next := nextBoundary(text, position)
		r, _ := utf8.DecodeRuneInString(text[position:next])
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' {
			break
		}
		position = next
	}
	for position < len(text) {
		next := nextBoundary(text, position)
		r, _ := utf8.DecodeRuneInString(text[position:next])
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' {
			break
		}
		position = next
	}
	return position
}

func visualLines(text string, width int) []visualLine {
	width = max(1, width)
	if text == "" {
		return []visualLine{{}}
	}
	var lines []visualLine
	start, column := 0, 0
	for index, r := range text {
		next := index + utf8.RuneLen(r)
		if r == '\n' {
			lines = append(lines, visualLine{start: start, end: index})
			start, column = next, 0
			continue
		}
		column++
		if column >= width {
			lines = append(lines, visualLine{start: start, end: next})
			start, column = next, 0
		}
	}
	lines = append(lines, visualLine{start: start, end: len(text)})
	return lines
}

func cursorVisualLine(text string, cursor, width int) ([]visualLine, int) {
	lines := visualLines(text, width)
	cursor = clampCursor(text, cursor)
	for index, line := range lines {
		last := index == len(lines)-1
		if cursor == line.start || cursor >= line.start && (cursor < line.end || last && cursor <= line.end) {
			return lines, index
		}
	}
	return lines, len(lines) - 1
}

func byteIndexAtRuneColumn(text string, start, end, column int) int {
	position := start
	for i := 0; i < column && position < end; i++ {
		position = nextBoundary(text, position)
	}
	return min(position, end)
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	var digits [20]byte
	position := len(digits)
	for value > 0 {
		position--
		digits[position] = byte('0' + value%10)
		value /= 10
	}
	return string(digits[position:])
}
