// Package cortex is what an agent has in mind: the context it sends the
// model each step. It holds the instructions and the conversation so
// far, and is where compaction and images will go.
package cortex

import (
	"fmt"
	"strings"

	"atto2/kernel"
	"atto2/model"
)

// Cortex is one agent's context.
type Cortex struct {
	System   string
	messages []model.Message
	// Tokens is the prompt size of the last model call: how full the
	// context is.
	Tokens int
}

// New makes a cortex with the agent's instructions.
func New(system string) *Cortex { return &Cortex{System: system} }

// Add puts a message in the context.
func (c *Cortex) Add(m model.Message) { c.messages = append(c.messages, m) }

// Messages is what the model is sent: the instructions, then the
// conversation.
func (c *Cortex) Messages() []model.Message {
	return append([]model.Message{{Role: "system", Content: c.System}}, c.messages...)
}

// Instructions describes the computer and this agent's granted world interface.
func Instructions(k *kernel.Kernel, project string) string {
	var b strings.Builder
	fmt.Fprintf(&b, `You are an agent with a Lua 5.1 machine. Use the lua tool to execute code; globals persist between runs. print and chunk returns are shown (16 KiB per run).
The machine computes only: base, string, table, math; no files, OS, network, time, random, require, load, or string.dump. pairs/next sort primitive keys; identity keys retain insertion order.
Pure helpers:
text.split(s, sep) -> array (literal separator)
text.lines(s) -> array (CRLF accepted, no final empty line)
text.trim(s) -> string
text.match_all(s, goPattern) -> array of full matches, or arrays of captures if the Go regexp has capture groups
json.encode(v) -> string; json.decode(s) -> value; json.null represents null. Dense tables encode as arrays, string-key tables as objects; decoded empty objects/arrays preserve their shape.
Everything that touches the world is a checked and logged syscall under sys. This agent has:
%sCalls accept positional arguments in schema order or one table; Lua has no named arguments. sys.bash("ls") and sys.bash{cmd = "ls"} are equivalent.
The project working directory for sys.bash is %q. Bash can read host files; it is not a read chroot. Writes outside its private temp directory and /dev/null are denied by the OS.
Your own text is your stdout, shown to observers, not a result to anyone. Results leave through sys.exit{report = "..."}.
`, k.Instructions(), project)
	return b.String()
}
