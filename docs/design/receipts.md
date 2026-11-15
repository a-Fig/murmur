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
- **A message asks for receipts** (`Event.Receipts`), and only a person's from the page asks now. A
  recipient's daemon writes marks only for messages that ask, so agents' chatter costs the bus
  nothing, and a later opt-in for agents (#276) needs no change on the recipients' side.
- **The recipient's daemon marks each state as it sees it**: queued when the message goes in a
  session's inbox, handed over when the client took it, delivered when the hook's notice told the
  session, read when the session had it whole. For a person, being in their inbox is delivered, and
  opening the conversation is read. A turn handed to T3, the socket or a client's command line stays
  handed over, since the daemon can't see the turn start.
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
- **Left for later.** Receipts in an agent's `send` result, `lookup` and `who` (Tyler wants them
  queryable on demand, #26, 2026-11-09). #275: delivered is marked where the daemon sees the hook
  fetch a notice, as far as it can see, and whether the agent's context took it is unknown. #276: an
  opt-in "tell me" that sets the flag on an agent's message and tells the sender once. A recipient
  idle, on do not disturb or offline shows as sent, and saying why isn't built.
