package server

import (
	"strings"

	"rsc.io/qr"
)

// QRQuiet is the margin, in modules, around a QR code: the standard's four.
const QRQuiet = 4

// QR renders text as a QR code for a terminal, two rows of modules per
// line with Unicode half blocks. Light modules and the quiet zone are the
// drawn ones (the terminal's foreground color) and dark modules are left
// blank: on a dark terminal, the usual case, that is a black code on white
// whatever the color scheme, without depending on terminal colors. On a
// light terminal it shows inverted, which phone cameras read as well.
func QR(text string) ([]string, error) {
	code, err := qr.Encode(text, qr.L)
	if err != nil {
		return nil, err
	}
	return QRLines(code.Size, code.Black), nil
}

// QRLines draws a size×size module matrix (black reports a dark module)
// with the quiet zone.
func QRLines(size int, black func(x, y int) bool) []string {
	light := func(x, y int) bool {
		x, y = x-QRQuiet, y-QRQuiet
		return x < 0 || y < 0 || x >= size || y >= size || !black(x, y)
	}
	n := size + 2*QRQuiet
	var lines []string
	for y := 0; y < n; y += 2 {
		var b strings.Builder
		for x := range n {
			top, bottom := light(x, y), y+1 < n && light(x, y+1)
			switch {
			case top && bottom:
				b.WriteString("█")
			case top:
				b.WriteString("▀")
			case bottom:
				b.WriteString("▄")
			default:
				b.WriteString(" ")
			}
		}
		lines = append(lines, b.String())
	}
	return lines
}
