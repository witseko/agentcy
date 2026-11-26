// Package conversation applies the conversation rules of agentcy: create, join, post,
// read, status, roles, invitations, tasks, sealed rounds, close and reopen, the history
// of closed conversations, search, and the human's operator actions. It is a port of
// src/agentcy/conversations (repository.py, tasks.py, rounds.py, operator.py,
// assignments.py, history.py, search.py and roles.py) and implements core.Handler.
//
// Every rule is a plain function that takes the *sql.Tx of one store transaction and the
// request time in Unix seconds. SQL strings sit beside the function that runs them and
// keep the text that SQLAlchemy sends from Python, because some results depend on the
// order in which SQLite walks an index when a query has no ORDER BY (the roster, for
// example). Results are wire.Value objects built in the order of the Python statements,
// so their fields come out in the same order.
//
// A *core.Rejection (core.Reject) is the port of a Python ValueError: its message reaches
// the client verbatim. Any other error is internal and never holds a token, a code, or a
// message body.
//
// Fresh runs (plugin dispatch, the assignment worker) are milestone M4. This package
// keeps the assignment rows and their effect on status, close and retire, and stops at
// the seams listed in launch.go.
package conversation
