Branch checkpoint: the user is leaving this branch of the conversation and going back to an earlier point. Everything from {{.Start}} onward is about to be removed from the context. Write a summary of that part, so the conversation can continue from the earlier point knowing what was tried here.

Use this format:

## Goal
What the user was trying to do on this branch.

## Constraints & Preferences
What the user asked for or ruled out.

## Progress
### Done
### In Progress
### Blocked

## Key Decisions
What was decided and why.

## Next Steps
What was about to happen next.

Leave out what came before that point: it stays in the context. Commands run on this branch changed files and processes for real, and those changes stay, so name the files changed and anything left running. Be specific: exact file paths, function names, commands, error messages. Skip sections with nothing to say. Stay under {{.Words}} words. Output only the summary, no preamble. Do not call tools.{{with .Focus}}

Additional focus: {{.}}{{end}}
