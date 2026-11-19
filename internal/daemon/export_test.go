package daemon

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
