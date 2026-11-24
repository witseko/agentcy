# Episode 02: One prompt in Claude Code, a whole team. Sourced by record.zsh.
# Left pane: the stand-in Claude Code session (screens/). Right: the TUI.
stand_in_plugins gemini:gemini-3-pro glm:glm-5
start_agent_beside_tui
start_recording

at prompt
type_text "Use agentcy to review the retry bug with two fresh workers, Timeout and Fixtures."
key enter

at create
stand_in create
# Open the new chat in the UI, then give the keyboard back to the agent.
sleep 1.5
tui_pane
key enter
agent_pane

at launch
stand_in launch Timeout glm
stand_in launch Fixtures gemini

at tasks
stand_in post assign-timeout
stand_in post assign-fixtures

at evidence
stand_in post fixtures-finding
stand_in post timeout-done
stand_in post fixtures-done

at answer
agent_next
