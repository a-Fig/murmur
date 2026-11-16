# Receipts

#26, first part built 2026-11-13, for a hackathon user who asked, after writing in a group chat on
the page, whether the agents got it. The recipients' daemons are on other devices, so their marks
travel back over the bus. Here a *mark* is what a recipient's daemon writes on the bus, and a *tick*
is what the page shows. The states are defined in [delivery](delivery.md).

- **The page shows a chat app's ticks, and no more** (Tyler, 2026-11-13: a first version, with a
  word and a count under every bubble, was too much next to what WhatsApp or iMessage show). Each of
  the person's messages has one tick under it: one for sent, two for delivered, and two in the
  accent color for read, the only state that stands out. Queued and handed over show as sent, and so
  does a recipient whose Murmur sends no receipts: the tick says what the person can act on, and the
  bus keeps the rest. A message to several shows the lowest state among them, so it reads as read
  only once everyone has read it. The tick's tooltip says its state in a few words, then lists who
  has it and since when; nothing else shows them (Tyler, 2026-11-13). A message from before
  receipts, or past the newest 200, has no tick.
- **A message asks for receipts** (`Event.Receipts`): a person's from the page, and an agent's whose
  sender asked to be told how far it got (#276, below). A recipient's daemon writes marks only for
  messages that ask, so agents' chatter costs the bus nothing.
- **The recipient's daemon marks each state as it sees it**: queued when the message goes in a
  session's inbox, handed over when the client took it, delivered when the hook's notice told the
  session, read when the session had it whole. For a person, being in their inbox is delivered, and
  opening the conversation is read. A turn handed to T3, the socket or a client's command line is
  handed over, since the daemon can't see the turn start. It becomes read when the hook's
  `UserPromptSubmit` sends a prompt that is that turn (#611, below); clients whose hook sends no
  prompt (Pi, OpenCode) leave it handed over.
- **A turn's prompt marks it read** (#611, 2026-11-15). The hook sends the first 4 KB of each
  prompt with `UserPromptSubmit` to its own daemon, which keeps none of it. The daemon remembers each
  message that asked for receipts as its handoff begins (the turn can start before the client's
  command returns), with the first 4 KB of the turn's text, and marks read the oldest whose turn the
  prompt is: from Murmur's header line on, the prompt and that text agree. Matching the turn, not
  just taking the next prompt, keeps a prompt the user typed while T3 held Murmur's turn in its queue
  from counting. Matching the whole text, not the subject, keeps a cancelled turn for "Deploy" from
  taking the mark of the turn for "Deploy now" (#651). At most 20 a session are remembered, for a
  day.
- **Marks are batched.** They wait 3 s (`ReceiptGather`) and go out as one `receipt` event on the
  recipient's own log, at most one per device every 15 s (`ReceiptGap`), keeping only each message's
  furthest state. Without the gap, a message to ten sessions on five devices could cost twenty
  receipts. A receipt is encrypted like any event, but its size (boxes aren't padded) shows roughly
  how many marks it holds. Receipts count toward the 4,000 commits the page reads back ([the
  page](page.md)), so a busy group chat leaves it fewer old messages.
- **The sender's daemon keeps the furthest mark per recipient** for its person's newest 200
  messages, with the recipients it expected when it sent: the one it was sent to, or for a group, a
  device or everyone, the sessions it saw there, less offline ones and those that muted the sender.
  A mark from any other address is dropped. The cost: a session that joined a group after the send
  gets the message but no place in the tick. Marks only go forward, so one read late, or twice,
  changes nothing.
- **An older recipient isn't waited for.** A device whose presence doesn't say it writes marks is
  shown as sent. A recipient with no presence here (a person's daemon exits soon after their page
  closes) isn't counted as older, since it may still write a mark.
- **An agent can ask to be told** (#276, built 2026-11-15). `send`'s `tell_me` is `read` or
  `delivered`; it is off unless asked, per message, and refused for a message to everyone, which
  would draw a mark from every session on the mesh. The message asks for receipts, and the sender's
  daemon keeps it in `Sent` like a person's, with the state asked about and who asked, until the
  sender has been told; meanwhile it is counted apart from the person's 200 (#748, 2026-11-16):
  sharing them, a busy half hour pushed out a record still waited on, and its sender never heard.
  The page shows no ticks for it.
  The sender is told once: when every recipient whose
  device writes marks has got that far, or after `TellWait` (30 minutes) with where it is with each.
  A message handed over as a turn counts as delivered: the client holds the turn whole, and it is
  how an idle session in T3 gets a message. The notice says which recipients got it as a turn. It
  doesn't count as read, as the turn may still wait in T3's queue: the mark becomes read once the
  turn's prompt comes (#611), and the sender is told then (#654). A recipient that sends no prompt
  stays handed over, so asking it for read waits the 30 minutes: Pi, OpenCode and Grok, a
  session with no hook, and a device whose daemon is older than #611.
  A message with nobody to wait for (every recipient's device too old, or nobody there) is told so in
  `send`'s result, and nothing is kept.
- **The telling is a notice from Murmur itself**, in the sender's inbox: a message from this device's
  receipts address, which no session holds, that never goes on the bus. The views show it as
  Murmur's, with no sender to describe and nobody to reply to. It doesn't start a turn of its own:
  the sender asked to know, not to be woken. It reaches the agent as the hook's notice after a tool
  call, or at the end of a turn, or when it reads its inbox; one the hook told of while the agent
  worked goes in at the turn's end, as any message does (#202). An older daemon passes over
  `tell_me`; its reply then has no `Telling`, and the MCP server tells the sender it won't be told.
  A sender replaced since it sent (a T3 thread's new session) is told in the session that holds its
  name now, as a message for it would be (#659).
- **Left for later.** Receipts in an agent's `send` result, `lookup` and `who`, on demand (#605; Tyler,
  #26, 2026-11-09). Delivered is marked where the daemon sees the hook fetch a notice, as far as it
  can see, and whether the agent's context took it is unknown. A recipient idle, on do not disturb or
  offline shows as sent on the page, and saying why isn't built.
