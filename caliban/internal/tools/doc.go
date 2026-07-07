// Package tools implements the builtin tool set (kept deliberately small;
// file operations go through the shell). Implemented today:
//
//   - shell: bash in the workspace, scrubbed environment, timeout
//   - memory_upsert: durable memory fact create/update
//   - schedule_reminder / schedule_turn / list_scheduled / cancel_scheduled
//   - notify: immediate push outside the normal reply flow
//   - delegate / delegate_continue: blocking child-conversation subagents
//   - history_search: bounded transcript search in the current conversation
//   - task_list / task_output / task_stop: managed background process visibility
//   - runner_list / runner_models / runner_run: trusted external agent harnesses
//
// Planned (not yet implemented; do not assume available):
//
//   - web_fetch / web_search
//
// Tools depend on capability interfaces (scheduling, notification) provided by
// the engine at construction; they do not import engine.
package tools
