package hermesbridge

import "testing"

func TestTakeDirectives(t *testing.T) {
	text, msgs := takeDirectives("t1", "Sure! I will collect them weekly.\n<deck:room name=\"Contest news\" routine=\"job1\"/> <deck:react emoji=\"🔥\"/>\n<deck:unknown x=\"1\"/>")
	if text != "Sure! I will collect them weekly.\n \n<deck:unknown x=\"1\"/>" {
		t.Fatalf("text: %q (only known markers are removed)", text)
	}
	if len(msgs) != 2 || msgs[0].Type != "room.create" || msgs[0].Name != "Contest news" || msgs[0].Routine != "job1" || msgs[1].Type != "reaction" || msgs[1].Emoji != "🔥" || msgs[1].Task != "t1" {
		t.Fatalf("msgs: %+v", msgs)
	}
	if text, msgs := takeDirectives("t1", "plain reply"); text != "plain reply" || len(msgs) != 0 {
		t.Fatalf("plain: %q %v", text, msgs)
	}
	if text, _ := takeDirectives("t1", "<deck:react emoji=\"👍\"/>"); text != "" {
		t.Fatalf("only directive should leave no text: %q", text)
	}
}

func TestDraftTextHidesDirectives(t *testing.T) {
	for in, want := range map[string]string{
		"writing":                            "writing",
		"All done <deck:react emoji=\"👍\"/>": "All done",
		"All done <deck:ro":                  "All done",
		"All done <deck:room name=\"Contest": "All done",
	} {
		if got := draftText(in); got != want {
			t.Errorf("%q: got %q want %q", in, got, want)
		}
	}
}
