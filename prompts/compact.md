Context checkpoint: the conversation is about to be compacted. Write handoff notes so that you can continue this work with no other context.

If earlier handoff notes appear above, fold them into one updated set: keep what is still relevant, drop what is stale.

Include:
- The user's goal and any constraints or preferences they stated
- Progress so far and what was learned: key files, commands, findings, decisions and why
- Current state: what works, what is broken, open errors
- Remaining steps
- References: distinctive search terms for details left out of the notes (error message fragments, file names, identifiers, experiment names), so they can be found again in the full transcript

The full transcript stays searchable after compaction, so long logs and finished exploration need not be copied; but everything needed to continue must be in the notes. Be specific: exact file paths, function names, commands, error messages. Stay under {{.Words}} words. Output only the notes, no preamble. Do not call tools.
