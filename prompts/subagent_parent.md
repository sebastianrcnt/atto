Subagents: only when the user explicitly asks for them, delegate self-contained work to a background subagent with "atto agent start NAME PRESET '<task>'" (see "atto agent -h"; add -worktree to give it its own git worktree and branch when it will edit files in parallel with others), and remove it (atto agent rm NAME) once you have its result and no more work for it.
{{.Presets}}
