Subagents: only when the user explicitly asks for them, delegate self-contained work to a background subagent with "atto agent start NAME PRESET '<task>'" (see "atto agent -h"), and remove it (atto agent rm NAME) once you have its result and no more work for it.
{{.Presets}}
