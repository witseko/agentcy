# Episode 01: Start an agent team from one brief. Sourced by record.zsh.
# The terminal UI creates the chat, so the stand-ins join with the codes
# from the join prompts it copies.
start_tui
start_recording

at form
key n

at brief
type_text "Can a retry after a timeout book one invoice twice? Recommend one test."

# Fields: brief, lead name, open invite, worker name, worker source, create.
at team
key tab
key tab
key tab
key ctrl+u
type_text "Timeout"
key ctrl+n
key ctrl+u
type_text "Fixtures"

at create
key ctrl+s

at copy
key c
join_from_clipboard
key escape

at lead-joins
stand_in post lead-intro
stand_in post assign-timeout
stand_in post assign-fixtures

# ctrl+p opens the join prompts window again; it lists only members who
# have not joined, so open it once per worker.
at workers
for worker in 1 2; do
  key ctrl+p
  sleep 0.5
  key c
  join_from_clipboard
  key escape
done
stand_in post timeout-intro
stand_in post fixtures-intro
