package ui

import (
	"image/color"

	glamouransi "github.com/charmbracelet/glamour/ansi"
	"github.com/charmbracelet/glamour/styles"
	"github.com/charmbracelet/lipgloss"
)

// C64 palette adapted from Jan T. Sott's C64 iTerm2 scheme, distributed by
// mbadolato/iTerm2-Color-Schemes:
// https://github.com/mbadolato/iTerm2-Color-Schemes/blob/master/schemes/C64.itermcolors
const (
	c64BlackHex      = "#090300"
	c64RedHex        = "#883932"
	c64GreenHex      = "#55A049"
	c64YellowHex     = "#BFD072"
	c64BlueHex       = "#40318D"
	c64MagentaHex    = "#8B3F96"
	c64CyanHex       = "#67B6BD"
	c64WhiteHex      = "#FFFFFF"
	c64ForegroundHex = "#7869C4"
)

var (
	c64Black      = lipgloss.Color(c64BlackHex)
	c64Red        = lipgloss.Color(c64RedHex)
	c64Green      = lipgloss.Color(c64GreenHex)
	c64Yellow     = lipgloss.Color(c64YellowHex)
	c64Blue       = lipgloss.Color(c64BlueHex)
	c64Magenta    = lipgloss.Color(c64MagentaHex)
	c64Cyan       = lipgloss.Color(c64CyanHex)
	c64White      = lipgloss.Color(c64WhiteHex)
	c64Foreground = lipgloss.Color(c64ForegroundHex)

	// Bubble Tea applies these as the terminal's default colors for the whole
	// view, including otherwise unstyled transcript text and modal surfaces.
	c64BackgroundRGB color.Color = color.RGBA{R: 0x40, G: 0x31, B: 0x8d, A: 0xff}
	c64ForegroundRGB color.Color = color.RGBA{R: 0x78, G: 0x69, B: 0xc4, A: 0xff}

	assistantStyle = lipgloss.NewStyle().Foreground(c64Foreground)
	toolStyle      = lipgloss.NewStyle().Foreground(c64Foreground)
	infoStyle      = lipgloss.NewStyle().Foreground(c64Cyan)
	errorStyle     = lipgloss.NewStyle().Foreground(c64White).Background(c64Red)
	composerPanel  = lipgloss.NewStyle().Foreground(c64Blue).Background(c64Foreground)
	mutedStyle     = lipgloss.NewStyle().Foreground(c64Foreground)
	selectedStyle  = lipgloss.NewStyle().Foreground(c64Yellow).Bold(true)
	planStyle      = lipgloss.NewStyle().Foreground(c64Magenta).Bold(true)
)

func c64MarkdownStyle() glamouransi.StyleConfig {
	style := styles.DarkStyleConfig
	style.Document.Color = colorPointer(c64ForegroundHex)
	style.Document.BackgroundColor = colorPointer(c64BlueHex)
	style.Heading.Color = colorPointer(c64CyanHex)
	style.H1.Color = colorPointer(c64BlueHex)
	style.H1.BackgroundColor = colorPointer(c64ForegroundHex)
	style.H6.Color = colorPointer(c64GreenHex)
	style.HorizontalRule.Color = colorPointer(c64ForegroundHex)
	style.Link.Color = colorPointer(c64CyanHex)
	style.LinkText.Color = colorPointer(c64YellowHex)
	style.Image.Color = colorPointer(c64MagentaHex)
	style.ImageText.Color = colorPointer(c64ForegroundHex)
	style.Code.Color = colorPointer(c64YellowHex)
	style.Code.BackgroundColor = colorPointer(c64BlackHex)
	style.CodeBlock.Color = colorPointer(c64ForegroundHex)
	style.CodeBlock.BackgroundColor = colorPointer(c64BlackHex)
	style.Table.Color = colorPointer(c64ForegroundHex)

	if style.CodeBlock.Chroma != nil {
		chroma := *style.CodeBlock.Chroma
		style.CodeBlock.Chroma = &chroma
		setChromaColor(&chroma.Text, c64ForegroundHex, "")
		setChromaColor(&chroma.Error, c64WhiteHex, c64RedHex)
		setChromaColor(&chroma.Comment, c64ForegroundHex, "")
		setChromaColor(&chroma.CommentPreproc, c64YellowHex, "")
		setChromaColor(&chroma.Keyword, c64CyanHex, "")
		setChromaColor(&chroma.KeywordReserved, c64MagentaHex, "")
		setChromaColor(&chroma.KeywordNamespace, c64MagentaHex, "")
		setChromaColor(&chroma.KeywordType, c64GreenHex, "")
		setChromaColor(&chroma.Operator, c64YellowHex, "")
		setChromaColor(&chroma.Punctuation, c64ForegroundHex, "")
		setChromaColor(&chroma.Name, c64WhiteHex, "")
		setChromaColor(&chroma.NameBuiltin, c64CyanHex, "")
		setChromaColor(&chroma.NameTag, c64YellowHex, "")
		setChromaColor(&chroma.NameAttribute, c64CyanHex, "")
		setChromaColor(&chroma.NameClass, c64YellowHex, "")
		setChromaColor(&chroma.NameConstant, c64MagentaHex, "")
		setChromaColor(&chroma.NameDecorator, c64CyanHex, "")
		setChromaColor(&chroma.NameException, c64RedHex, "")
		setChromaColor(&chroma.NameFunction, c64GreenHex, "")
		setChromaColor(&chroma.NameOther, c64ForegroundHex, "")
		setChromaColor(&chroma.Literal, c64YellowHex, "")
		setChromaColor(&chroma.LiteralNumber, c64YellowHex, "")
		setChromaColor(&chroma.LiteralDate, c64YellowHex, "")
		setChromaColor(&chroma.LiteralString, c64GreenHex, "")
		setChromaColor(&chroma.LiteralStringEscape, c64CyanHex, "")
		setChromaColor(&chroma.GenericDeleted, c64RedHex, "")
		setChromaColor(&chroma.GenericEmph, c64ForegroundHex, "")
		setChromaColor(&chroma.GenericInserted, c64GreenHex, "")
		setChromaColor(&chroma.GenericStrong, c64WhiteHex, "")
		setChromaColor(&chroma.GenericSubheading, c64CyanHex, "")
		setChromaColor(&chroma.Background, c64ForegroundHex, c64BlackHex)
	}
	return style
}

func setChromaColor(primitive *glamouransi.StylePrimitive, foreground, background string) {
	primitive.Color = colorPointer(foreground)
	if background == "" {
		primitive.BackgroundColor = nil
		return
	}
	primitive.BackgroundColor = colorPointer(background)
}

func colorPointer(value string) *string { return &value }
