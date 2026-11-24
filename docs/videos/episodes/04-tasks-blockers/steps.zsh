# Episode 04: Tasks, blockers and handoffs. Sourced by record.zsh.
stand_in create
stand_in post lead-plan
start_tui
key enter
sleep 1
start_recording

at assign
stand_in post assign-timeout
stand_in post assign-fixtures

at tasks
key ctrl+t

at blocker
key ctrl+t
stand_in post timeout-blocked

at help
stand_in post lead-asks-fixtures

at evidence
stand_in post fixtures-evidence

at resume
stand_in post timeout-resume

at handoff
stand_in post fixtures-handoff
stand_in post timeout-accept

at done
stand_in post timeout-done
