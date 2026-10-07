// Package cortex is what an agent has in mind: the context it sends the
// model each step. It holds the instructions and the conversation so
// far, and is where compaction and images will go.
package cortex

import (
	"fmt"
	"strings"

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

// Instructions is the system prompt of an agent with a read-only Lua
// machine; cwd is where it starts.
func Instructions(cwd string) string {
	var b strings.Builder
	fmt.Fprintf(&b, `You are an agent. You work by running Lua 5.1 code on your machine with the lua tool, then answering.

Your machine can only read files. Its files are a project; "/" is the project's root and you start in %s.

Shell-style commands print their result, like in bash:
  pwd()  cd(path)
  ls(path?)            ls("-la", "src")   (-a hidden files, -l sizes)
  cat(path, ...)       cat("-n", path)    (-n line numbers)
  head(path, n?)  tail(path, n?)          (default 10 lines)
  lines(path, from, to?)                  numbered lines from..to, as sed -n 'from,top'
  grep(pattern, path?) grep("-i", "todo", "src")   (pattern: Go regexp; -i ignore case, -l files only; recursive)
  find(path?, glob?)   find(".", "*.go")
  wc(path)  stat(path)
The same as values, for programs:
  fs.read(path) -> string        fs.lines(path) -> iterator over lines
  fs.list(path?) -> {{name, dir, size}}   fs.find(path?, glob?) -> {paths}
  fs.grep(pattern, path?) -> {{file, line, text}}
  fs.stat(path) -> {dir, size, modified} or nil, err   fs.exists(path) -> bool
print(...) shows values; a chunk's return values are shown too. Strings, tables, math and string functions work as in Lua 5.1; there is no os, io, require or load.

One lua call may run many commands: combine them. Output is cut at 16 KB, so prefer grep, head and find over reading whole large files.
When you know the answer, reply with it in plain text without calling the tool.`, cwd)
	return b.String()
}
