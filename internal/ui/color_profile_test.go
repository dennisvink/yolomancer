package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/dennisvink/yolomancer/internal/app"
	"github.com/dennisvink/yolomancer/internal/model"
	"github.com/muesli/termenv"
)

func TestModernTerminalUsesRGBDespiteLimitedDetectedProfile(t *testing.T) {
	previous := lipgloss.ColorProfile()
	t.Cleanup(func() { lipgloss.SetColorProfile(previous) })
	for _, profile := range []termenv.Profile{termenv.ANSI, termenv.ANSI256} {
		lipgloss.SetColorProfile(profile)
		newModel(app.New(&model.Config{}, false), nil)
		if lipgloss.ColorProfile() != termenv.TrueColor {
			t.Fatal("UI did not select true color")
		}
		panel := composerPanel.Render("input")
		for _, sequence := range []string{
			termenv.RGBColor(c64BlueHex).Sequence(false),
			termenv.RGBColor(c64ForegroundHex).Sequence(true),
		} {
			if !strings.Contains(panel, sequence) {
				t.Fatalf("composer is missing C64 true-color sequence %q: %q", sequence, panel)
			}
		}
	}
}
