# Episode 07: Grow and shrink the team mid-chat. Sourced by record.zsh.
# Team panel order: Lead, Docs, Fixtures, Reviewer (after the invite), Timeout.
stand_in create
stand_in post lead-plan
stand_in post fixtures-finished
start_tui
key enter
sleep 1
start_recording

# The message line has focus: tab moves to the team panel, i starts /invite.
at invite
key tab
key i
type_text "Reviewer"
key enter

# The invite opens the join prompts window with Reviewer selected.
at reissue
key up
key r

# Closing the window returns to the message line; tab to the team panel.
at retire
key escape
key tab
for step in 1 2 3 4 5; do key up; done
key down
key down
key d
sleep 1
key enter

at close
stand_in post timeout-fixed
key ctrl+x
type_text "Fixed: one request_id per invoice, with a regression test."
key enter
