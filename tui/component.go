package tui

// Component renders itself into terminal lines for a given width.
// Every returned line must fit within width columns; the renderer truncates
// lines that do not, rather than crashing.
type Component interface {
	Render(width int) []string
}

// InputHandler is implemented by components that accept keyboard input while
// focused. data is one decoded input unit (a key sequence or a paste).
type InputHandler interface {
	HandleInput(data string)
}

// Focusable components are told when they gain or lose focus. While focused
// they should emit CursorMarker at the cursor position so the renderer can
// place the hardware cursor there (needed for IME candidate windows).
type Focusable interface {
	SetFocused(bool)
}

// CursorMarker is a zero-width APC sequence that terminals ignore. The
// renderer finds it, strips it, and moves the hardware cursor to its position.
const CursorMarker = "\x1b_atto:c\x07"

// Clickable components react to mouse clicks (fullscreen mode only). line
// is relative to the component's first rendered line. Returns true if the
// click changed something.
type Clickable interface {
	Click(line int) bool
}

// Spaced components are another component with blank lines above it. A
// Container draws the blank lines itself and takes the rest from the inner
// component, so the wrapper does not copy the lines on every frame.
type Spaced interface {
	Component
	Spaced() (inner Component, blank int)
}

// Container stacks child components vertically.
type Container struct {
	Children []Component

	// Line ranges of each child from the last Render, for hit-testing.
	ranges []childRange
}

type childRange struct {
	start, end int
	c          Component
}

// Click dispatches a click at line (relative to the container) to the child
// rendered there.
func (c *Container) Click(line int) bool {
	for _, r := range c.ranges {
		if line >= r.start && line < r.end {
			if cl, ok := r.c.(Clickable); ok {
				return cl.Click(line - r.start)
			}
			return false
		}
	}
	return false
}

// Each calls fn for every child with its line range from the last Render.
func (c *Container) Each(fn func(child Component, start, end int)) {
	for _, r := range c.ranges {
		fn(r.c, r.start, r.end)
	}
}

func (c *Container) Add(children ...Component) { c.Children = append(c.Children, children...) }

func (c *Container) Remove(child Component) {
	for i, ch := range c.Children {
		if ch == child {
			c.Children = append(c.Children[:i], c.Children[i+1:]...)
			return
		}
	}
}

func (c *Container) Clear() { c.Children, c.ranges = nil, nil }

func (c *Container) Render(width int) []string {
	var lines []string
	if k := len(c.ranges); k > 0 && c.ranges[k-1].end > 0 {
		n := c.ranges[k-1].end // the last frame's height: grow once at most
		lines = make([]string, 0, n+n/8)
	}
	c.ranges = c.ranges[:0]
	for _, ch := range c.Children {
		start := len(lines)
		if s, ok := ch.(Spaced); ok {
			inner, blank := s.Spaced()
			for range blank {
				lines = append(lines, "")
			}
			lines = append(lines, inner.Render(width)...)
		} else {
			lines = append(lines, ch.Render(width)...)
		}
		c.ranges = append(c.ranges, childRange{start, len(lines), ch})
	}
	return lines
}

// Screen is a component that takes the whole terminal (TUI.Screen): it
// renders exactly height rows of width columns.
type Screen interface {
	RenderScreen(width, height int) []string
}

// CellClickable adds horizontal hit testing without changing legacy components.
type CellClickable interface{ ClickAt(column, line int) bool }

func (c *Container) ClickAt(column, line int) bool {
	for _, r := range c.ranges {
		if line >= r.start && line < r.end {
			if cell, ok := r.c.(CellClickable); ok {
				return cell.ClickAt(column, line-r.start)
			}
			if click, ok := r.c.(Clickable); ok {
				return click.Click(line - r.start)
			}
			return false
		}
	}
	return false
}
