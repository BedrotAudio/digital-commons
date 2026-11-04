package render

import (
	"image/color"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
)

// Paint fills every cell of a width by height region that has no background or
// foreground of its own, which lipgloss styles and glamour cannot do. Run it as
// the last step for a region. At ASCII or lower it returns content unchanged, so
// that no color is emitted.
func Paint(content string, width, height int, bg, fg color.Color, profile colorprofile.Profile) string {
	if profile <= colorprofile.ASCII || width <= 0 || height <= 0 {
		return content
	}
	canvas := lipgloss.NewCanvas(width, height)
	canvas.Compose(lipgloss.NewLayer(content))
	for y := range height {
		for x := range width {
			cell := canvas.CellAt(x, y)
			if cell == nil {
				continue
			}
			if cell.Style.Bg == nil {
				cell.Style.Bg = bg
			}
			if cell.Style.Fg == nil {
				cell.Style.Fg = fg
			}
		}
	}
	return canvas.Render()
}
