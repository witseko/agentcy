# Episode 03: Steer a running team as the human. Sourced by record.zsh.
# The team is already at work before the recording starts.
stand_in create
stand_in post assign-timeout
stand_in post assign-fixtures
stand_in post timeout-found
start_tui
# Open the chat before recording, so the hook shows the team at work.
key enter
sleep 1
start_recording

# Each reply posts right after the human's message, so it shows while the
# narrator still talks about it.
at note
type_text "Keep the fix small. No new dependencies."
key enter
stand_in post lead-ack

at address
type_text "@Fixtures reuse the SlowOnceApi fixture"
key enter
stand_in post fixtures-ack

at question
type_text "@Timeout ?can one invoice be booked twice?"
key enter
stand_in post timeout-answer

at lead-picks-up
stand_in post lead-new-task
