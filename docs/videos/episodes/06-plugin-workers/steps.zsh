# Episode 06: Fresh read-only workers through plugins. Sourced by record.zsh.
# Team panel order: Lead, Fixtures, Timeout.
stand_in_plugins gemini:gemini-3-pro glm:glm-5
stand_in create
stand_in post assign-timeout
stand_in post assign-fixtures
start_tui
key enter
sleep 1
start_recording

# The message line has focus: tab to the team panel, select Fixtures, press l.
at launch-first
key tab
key down
key l
sleep 0.5
key enter

at launch-second
type_text "/launch Timeout glm"
key enter

at work
stand_in post fixtures-finding
stand_in post timeout-finding
