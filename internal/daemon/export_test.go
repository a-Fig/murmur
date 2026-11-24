package daemon

import "time"

// SetMaxWholeReply sets maxWholeReply for a test, and returns what puts it back.
func SetMaxWholeReply(n int) func() {
	was := maxWholeReply
	maxWholeReply = n
	return func() { maxWholeReply = was }
}

// WhichTurn is which of turns, the texts of turns handed to one session in order, prompt is, or -1,
// as the daemon matches them (#611, #651).
func WhichTurn(prompt string, turns ...string) int {
	list := make([]awaitingRead, len(turns))
	for i, t := range turns {
		list[i] = awaitingRead{Turn: turnStart(t)}
	}
	return whichTurn(prompt, list)
}

// CatchUpWindow is catchUpWindow, how long after the start what is read for a reconnecting session
// is held for it.
const CatchUpWindow = catchUpWindow

// RefFileOK is refFileOK, for a test of what counts as a broken bus ref (#644).
func RefFileOK(b []byte) bool { return refFileOK(b) }

// SetSendRace sets sendRace, what runs in a send between the look at a part of a path and its
// opening, and returns what puts it back (#773).
func SetSendRace(f func(part string)) func() {
	was := sendRace
	sendRace = f
	return func() { sendRace = was }
}

// SetGraceBegins sets graceBegins, what is called as a dropped session's grace begins and returns
// what ends it, and returns what puts it back.
func SetGraceBegins(f func(id string) <-chan struct{}) func() {
	was := graceBegins
	graceBegins = f
	return func() { graceBegins = was }
}

// SetCommandHold sets how long a command's session is held with its thread idle (commandIdle), and
// how often that is looked at (holdCheck), and returns what puts both back (#913). Either may run
// while a daemon is still up: both are atomic.
func SetCommandHold(idle, check time.Duration) func() {
	wasIdle, wasCheck := commandIdle.Swap(int64(idle)), holdCheck.Swap(int64(check))
	return func() { commandIdle.Store(wasIdle); holdCheck.Store(wasCheck) }
}

// SetSocketIdle sets how long a session whose plugin's socket answers is held idle (socketIdle), and
// returns what puts it back (#1184).
func SetSocketIdle(idle time.Duration) func() {
	was := socketIdle.Swap(int64(idle))
	return func() { socketIdle.Store(was) }
}
