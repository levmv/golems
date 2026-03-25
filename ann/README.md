
- unlike the first bot, this one uses llm package (it was developed in parallel) and trying to be more universal and serious (but not too much)
- wе have persistent memory for each session. It's just a simple jsonl file and it's append only and never truncated (implement it externally if needed),
  context is compacted every N messages and new checkpoint (line number in history) saved in additional file.
- soul.md is global (per bot) by design. Idea is to have evolving persona for each bot, but not the shared memory.
- it's intentionaly simple, not suitable for thousands of chats, etc

