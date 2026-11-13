package view

import (
	"strings"
	"testing"

	"github.com/a-Fig/murmur/internal/brand"
	"github.com/a-Fig/murmur/internal/bus"
	"github.com/a-Fig/murmur/internal/proto"
)

// A person reads none of the conversation they wrote into, so a turn from one says so whether or not
// the agent has met them, and the reply isn't optional. The human tag stays on every turn, as who
// shows it, when the rest of the introduction goes (#267).
func TestTurnFromAPerson(t *testing.T) {
	name := brand.Product
	m := proto.MessageView{From: "dev-0001/" + bus.PersonSession, FromName: "ada", FromAgent: bus.AgentPerson, FromOwner: "smash", To: "x", Subject: "look at this", Body: "please"}
	last := "ada sees only what you `send`, not this conversation. Answer with " + name + "'s `send` to ada.\n"
	want := "Message from ada [human] (on smash's machine) via " + name + ":\n\nlook at this\n\nplease\n\n" + last
	if got := Turn(m); got != want {
		t.Errorf("first turn:\n%s\nwant:\n%s", got, want)
	}
	m.Introduced = true
	want = "Message from ada [human] via " + name + ":\n\nlook at this\n\nplease\n\n" + last // still marked
	if got := Turn(m); got != want {
		t.Errorf("later turn:\n%s\nwant:\n%s", got, want)
	}

	// A name seen on more than one device doesn't say who sent it, so every turn names the device.
	m.FromDevice = "dev-0001"
	want = "Message from ada [human] (from device dev-0001) via " + name + ":\n\nlook at this\n\nplease\n\n" + last
	if got := Turn(m); got != want {
		t.Errorf("a later turn from a name on two devices:\n%s\nwant:\n%s", got, want)
	}
	m.Introduced = false
	if got := Turn(m); !strings.Contains(got, "(on smash's machine, from device dev-0001)") {
		t.Errorf("a first turn from a name on two devices:\n%s", got)
	}
}

func TestPeopleAreNotCalledAgents(t *testing.T) {
	agent := proto.MessageView{From: "dev-0001/abc123", FromName: "quiet-otter", FromAgent: "Codex", Subject: "a"}
	person := proto.MessageView{From: "dev-0001/" + bus.PersonSession, FromName: "ada", FromAgent: bus.AgentPerson, Subject: "b", Body: "c"}
	if got := Notices([]proto.MessageView{agent}, 1); !strings.Contains(got, "from teammates' agents") {
		t.Errorf("an agent's notice: %s", got)
	}
	if got := Notices([]proto.MessageView{person}, 1); !strings.Contains(got, "from teammates and their agents") {
		t.Errorf("a person's notice: %s", got)
	}
	got := Inbox(proto.Response{Messages: []proto.MessageView{person}})
	for _, want := range []string{"from teammates and their agents", "ada is a person, and sees only what you send them. Reply to ada."} {
		if !strings.Contains(got, want) {
			t.Errorf("the inbox lacks %q:\n%s", want, got)
		}
	}
}

// Wherever an agent reads a person's message, the sender carries the human tag, and an agent's
// doesn't (#267).
func TestAPersonsMessageIsTaggedHuman(t *testing.T) {
	agent := proto.MessageView{From: "dev-0001/abc123", FromName: "quiet-otter", FromAgent: "Codex", Subject: "a"}
	person := proto.MessageView{From: "dev-0001/" + bus.PersonSession, FromName: "ada", FromAgent: bus.AgentPerson, Subject: "b"}
	for what, got := range map[string]string{
		"notice": Notices([]proto.MessageView{person}, 1),
		"inbox":  Inbox(proto.Response{Messages: []proto.MessageView{person}}),
		"turn":   Turn(person),
	} {
		if !strings.Contains(got, "ada (dev-0001/human) [human]") && !strings.Contains(got, "ada [human]") {
			t.Errorf("a person's %s isn't tagged human:\n%s", what, got)
		}
	}
	for what, got := range map[string]string{
		"notice": Notices([]proto.MessageView{agent}, 1),
		"inbox":  Inbox(proto.Response{Messages: []proto.MessageView{agent}}),
		"turn":   Turn(agent),
	} {
		if strings.Contains(got, "[human]") {
			t.Errorf("an agent's %s is tagged human:\n%s", what, got)
		}
	}
}

// Only the first message a session has from a person says in full that they see only what the agent
// sends; later ones (Explained) end as an agent's do, in a turn and in inbox (#345).
func TestAPersonIsExplainedOnce(t *testing.T) {
	name := brand.Product
	m := proto.MessageView{From: "dev-0001/" + bus.PersonSession, FromName: "ada", FromAgent: bus.AgentPerson, To: "x", Subject: "look at this", Introduced: true}
	long := "ada sees only what you `send`, not this conversation. Answer with " + name + "'s `send` to ada.\n"
	short := "Reply with " + name + "'s `send` to ada if a reply is needed.\n"
	if got := Turn(m); !strings.HasSuffix(got, "\n"+long) {
		t.Errorf("first turn:\n%s", got)
	}
	m.Explained = true
	if got, want := Turn(m), "Message from ada [human] via "+name+":\n\nlook at this\n\n"+short; got != want {
		t.Errorf("later turn:\n%s\nwant:\n%s", got, want)
	}
	if got := Inbox(proto.Response{Messages: []proto.MessageView{m}}); !strings.Contains(got, "(Reply to ada.)\n") || strings.Contains(got, "sees only") {
		t.Errorf("a later message in inbox:\n%s", got)
	}
}
