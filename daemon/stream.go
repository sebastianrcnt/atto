package daemon

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// Marker is the OSC number atto in a pane uses to talk to the daemon
// through its own output: ESC ] 7337 ; <command> [; args] BEL. The daemon
// takes these out of the stream; a terminal never sees them.
const Marker = 7337

// MarkerSeq is the escape sequence for a marker command.
func MarkerSeq(fields ...string) string {
	return fmt.Sprintf("\x1b]%d;%s\x07", Marker, strings.Join(fields, ";"))
}

// decModes are the DEC private modes worth restoring on a terminal that
// attaches midway: cursor keys, cursor visibility, the alternate screen,
// mouse reporting, focus events and bracketed paste.
var decModes = []int{1, 25, 47, 1000, 1002, 1003, 1004, 1005, 1006, 1015, 1047, 1049, 2004}

// altScreens are the modes that switch to the alternate screen.
var altScreens = []int{47, 1047, 1049}

// stream follows a pane's output: it passes bytes through, takes out
// markers, and tracks the terminal modes the program has set, so a
// terminal that attaches later can be put in the same state and one that
// leaves can be put back. Escape sequences split across writes are held
// until complete.
type stream struct {
	state   int // ground, esc, csi, osc, oscEsc
	pending []byte
	modes   map[int]bool // DEC private modes; true = set
	mok     int          // xterm modifyOtherKeys level
	kitty   []int        // kitty keyboard flags pushed, innermost last
}

const (
	ground = iota
	esc
	csi
	osc
	oscEsc // ESC seen inside an OSC: maybe the ST terminator
)

// maxSeq caps a held sequence; longer ones pass through untouched.
const maxSeq = 4096

func newStream() *stream { return &stream{modes: map[int]bool{25: true}} }

// feed returns b without markers, and the markers' contents.
func (s *stream) feed(b []byte) (out []byte, markers []string) {
	out = make([]byte, 0, len(b))
	for _, c := range b {
		switch s.state {
		case ground:
			if c == 0x1b {
				s.state, s.pending = esc, append(s.pending[:0], c)
				continue
			}
			out = append(out, c)
		case esc:
			s.pending = append(s.pending, c)
			switch c {
			case '[':
				s.state = csi
			case ']':
				s.state = osc
			default:
				out = append(out, s.pending...)
				s.state = ground
			}
		case csi:
			s.pending = append(s.pending, c)
			if c >= 0x40 && c <= 0x7e {
				s.csi(string(s.pending[2:len(s.pending)-1]), c)
				out = append(out, s.pending...)
				s.state = ground
			} else if len(s.pending) > maxSeq {
				out = append(out, s.pending...)
				s.state = ground
			}
		case osc, oscEsc:
			s.pending = append(s.pending, c)
			end := 0
			switch {
			case c == 0x07:
				end = 1
			case s.state == oscEsc && c == '\\':
				end = 2
			}
			if end > 0 {
				body := string(s.pending[2 : len(s.pending)-end])
				if m, ok := strings.CutPrefix(body, strconv.Itoa(Marker)+";"); ok {
					markers = append(markers, m)
				} else {
					out = append(out, s.pending...)
				}
				s.state = ground
				continue
			}
			if c == 0x1b {
				s.state = oscEsc
			} else {
				s.state = osc
			}
			if len(s.pending) > maxSeq {
				out = append(out, s.pending...)
				s.state = ground
			}
		}
	}
	return out, markers
}

// csi notes the modes a control sequence sets: params are the bytes
// between "ESC [" and the final byte.
func (s *stream) csi(params string, final byte) {
	switch {
	case strings.HasPrefix(params, "?") && (final == 'h' || final == 'l'):
		for p := range strings.SplitSeq(params[1:], ";") {
			if n, err := strconv.Atoi(p); err == nil && slices.Contains(decModes, n) {
				s.modes[n] = final == 'h'
			}
		}
	case strings.HasPrefix(params, ">4") && final == 'm':
		s.mok = 0
		if _, v, ok := strings.Cut(params, ";"); ok {
			s.mok, _ = strconv.Atoi(v)
		}
	case strings.HasPrefix(params, ">") && final == 'u':
		f, _ := strconv.Atoi(params[1:])
		s.kitty = append(s.kitty, f)
	case strings.HasPrefix(params, "<") && final == 'u':
		n := 1
		if params != "<" {
			n, _ = strconv.Atoi(params[1:])
		}
		s.kitty = s.kitty[:max(0, len(s.kitty)-max(n, 1))]
	case strings.HasPrefix(params, "=") && final == 'u':
		f, _ := strconv.Atoi(strings.SplitN(params[1:], ";", 2)[0])
		if len(s.kitty) == 0 {
			s.kitty = append(s.kitty, f)
		} else {
			s.kitty[len(s.kitty)-1] = f
		}
	}
}

// alt reports whether the program is on the alternate screen.
func (s *stream) alt() bool {
	return slices.ContainsFunc(altScreens, func(m int) bool { return s.modes[m] })
}

// restore is what puts a fresh terminal in the modes the program set: the
// alternate screen first (entering it clears it), then the rest.
func (s *stream) restore() string {
	var b strings.Builder
	for _, m := range altScreens {
		if s.modes[m] {
			fmt.Fprintf(&b, "\x1b[?%dh", m)
			break
		}
	}
	for _, m := range decModes {
		if slices.Contains(altScreens, m) {
			continue
		}
		if on, ok := s.modes[m]; ok && on != (m == 25) {
			fmt.Fprintf(&b, "\x1b[?%d%c", m, map[bool]byte{true: 'h', false: 'l'}[on])
		}
	}
	if s.mok != 0 {
		fmt.Fprintf(&b, "\x1b[>4;%dm", s.mok)
	}
	for _, f := range s.kitty {
		fmt.Fprintf(&b, "\x1b[>%du", f)
	}
	return b.String()
}

// reset undoes restore: what a terminal needs when it stops showing the
// program, so the shell it returns to works as before.
func (s *stream) reset() string {
	var b strings.Builder
	b.WriteString("\x1b[0m")
	if n := len(s.kitty); n > 0 {
		fmt.Fprintf(&b, "\x1b[<%du", n)
	}
	if s.mok != 0 {
		b.WriteString("\x1b[>4m")
	}
	for _, m := range decModes {
		if m != 25 && !slices.Contains(altScreens, m) && s.modes[m] {
			fmt.Fprintf(&b, "\x1b[?%dl", m)
		}
	}
	for _, m := range altScreens {
		if s.modes[m] {
			fmt.Fprintf(&b, "\x1b[?%dl", m)
			break
		}
	}
	b.WriteString("\x1b[?25h")
	return b.String()
}
