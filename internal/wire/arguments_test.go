package wire

import (
	"strings"
	"testing"
)

const testWorkspace = "0000000000000000000000000000000000000000000000000000000000000000"

func TestReadArgumentsChecksEachArgumentInSignatureOrder(t *testing.T) {
	arguments, _ := Parse([]byte(`{"body":"  ","your_name":5,"unknown":1}`))
	request := &Create{Workspace: testWorkspace}
	err := ReadArguments(request, "create_conversation", arguments, []Argument{
		{Name: "your_name", Field: "initiator"},
		{Name: "body", Field: "brief"},
	})
	want := "2 validation errors for create_conversationArguments\nyour_name\n  Input should be a valid string [type=string_type]" +
		"\n    For further information visit https://errors.pydantic.dev/2.14/v/string_type" +
		"\nbody\n  String should have at least 1 character [type=string_too_short]" +
		"\n    For further information visit https://errors.pydantic.dev/2.14/v/string_too_short"
	if err == nil || err.Error() != want {
		t.Errorf("error %v, want %q", err, want)
	}
}

func TestReadArgumentsPreParsesJSONExceptForRequiredTexts(t *testing.T) {
	arguments, _ := Parse([]byte(`{"token":"null","to":"null","invitees":"null","reply_to":"[1]"}`))
	post := &Post{Workspace: testWorkspace}
	err := ReadArguments(post, "post_message", arguments, []Argument{{Name: "token", Field: "token"}, {Name: "to", Field: "to"}})
	if err != nil || post.Token != "null" || post.To != nil {
		t.Errorf("token %q, to %v, error %v", post.Token, post.To, err)
	}
	create := &Create{Workspace: testWorkspace}
	if err := ReadArguments(create, "create_conversation", arguments, []Argument{{Name: "invitees", Field: "invitees", NullIsAbsent: true}}); err != nil || create.Invitees == nil || len(create.Invitees) != 0 {
		t.Errorf("invitees %v, error %v", create.Invitees, err)
	}
	err = ReadArguments(post, "post_message", arguments, []Argument{{Name: "reply_to", Field: "reply_to"}})
	if err == nil || !strings.Contains(err.Error(), "reply_to\n  Input should be a valid integer") {
		t.Errorf("reply_to error %v", err)
	}
}

func TestValidateRestSkipsCheckedFieldsAndChecksTheOthers(t *testing.T) {
	arguments, _ := Parse([]byte(`{"token":" t ","name":" Worker ","plugin":"bad plugin!"}`))
	bindings := []Argument{{Name: "token", Field: "token"}, {Name: "name", Field: "name"}, {Name: "plugin", Field: "plugin", TypeOnly: true}}
	launch := &Launch{Workspace: testWorkspace}
	if err := ReadArguments(launch, "launch_participant", arguments, bindings); err != nil {
		t.Fatalf("type-only plugin was checked: %v", err)
	}
	if launch.Name != "Worker" {
		t.Errorf("name %q was not stripped", launch.Name)
	}
	err := ValidateRest(launch, bindings)
	want := "2 validation errors for Launch\nplugin\n  String should match pattern '^[a-zA-Z0-9-]{1,64}$'"
	if err == nil || !strings.HasPrefix(err.Error(), want) || !strings.Contains(err.Error(), "\ndirectory\n  String should have at least 1 character") {
		t.Errorf("error %v", err)
	}
}

func TestValidateRestAppliesTheRulesAcrossFields(t *testing.T) {
	create := &Create{Workspace: testWorkspace, Brief: "B", Initiator: "Lead", DeadlineMinutes: new(int64)}
	*create.DeadlineMinutes = 5
	err := Validate(create)
	if err == nil || err.Error() != "1 validation error for Create\n  Value error, Invite at least one participant or enable open_invite [type=value_error]\n    For further information visit https://errors.pydantic.dev/2.14/v/value_error" {
		t.Errorf("error %v", err)
	}
	create.OpenInvite = true
	if err := Validate(create); err == nil || !strings.Contains(err.Error(), "A proposal deadline requires a sealed round") {
		t.Errorf("error %v", err)
	}
}

func TestReadArgumentsPanicsOnAnUnknownField(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("an argument bound to no field was accepted")
		}
	}()
	_ = ReadArguments(&Status{}, "conversation_status", NewObject(), []Argument{{Name: "token", Field: "tokn"}})
}
