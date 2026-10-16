package main

import (
	"encoding/json"
	"html"
	"strings"
	"testing"
	"time"
	"unicode/utf16"
)

func TestNativeApprovalCardEscapesInput(t *testing.T) {
	a := approval{Method: "item/commandExecution/requestApproval", Host: "host", Params: json.RawMessage(`{"tool_name":"Bash","cwd":"/work/project","tool_input":{"command":"echo '<b>unsafe</b>' && pwd","description":"Inspect <project>"}}`)}
	card := approvalCard(a, "0123456789abcdef", time.Unix(0, 0))
	for _, want := range []string{"Run command", "/work/project", "Inspect &lt;project&gt;", "&lt;b&gt;unsafe&lt;/b&gt;", "&amp;&amp;", "This request only"} {
		if !strings.Contains(card, want) {
			t.Fatalf("missing %q in %s", want, card)
		}
	}
	if strings.Contains(card, "<b>unsafe</b>") {
		t.Fatal("untrusted HTML was not escaped")
	}
}
func TestDetailsPreserveUnicodeAndEscapeMarkup(t *testing.T) {
	raw := []byte(strings.Repeat("😀<&>", 2000))
	messages := approvalDetails(raw, "0123456789abcdef")
	var restored strings.Builder
	for _, message := range messages {
		if len(utf16.Encode([]rune(html.UnescapeString(message)))) > 4096 {
			t.Fatal("message exceeds conservative wire length")
		}
		start := strings.Index(message, "<blockquote expandable>") + len("<blockquote expandable>")
		end := strings.Index(message, "</blockquote>")
		restored.WriteString(message[start:end])
	}
	escaped := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(string(raw))
	if restored.String() != escaped {
		t.Fatal("details were lost or changed during chunking")
	}
}
func TestLargeCardFitsTelegram(t *testing.T) {
	a := sampleApproval()
	a.VetoSeconds = 5
	params := map[string]any{"command": strings.Repeat("😀", 4000), "reason": strings.Repeat("😀", 1000), "cwd": strings.Repeat("😀", 1000)}
	a.Params, _ = json.Marshal(params)
	card := approvalCard(a, "0123456789abcdef", time.Now())
	// Limits apply after parsing entities: this example needs no escaping.
	if len(utf16.Encode([]rune(card))) > 4096 {
		t.Fatal("summary card too long")
	}
}

func TestConcurrentCardExplainsProvisionalPhoneVote(t *testing.T) {
	a := sampleApproval()
	a.VetoSeconds = 5
	card := approvalCard(a, "0123456789abcdef", time.Now())
	if !strings.Contains(card, "phone vote is provisional") || !strings.Contains(card, "5 seconds") || !strings.Contains(card, "terminal decision takes priority") {
		t.Fatal("concurrent card must explain the terminal override window")
	}
}
