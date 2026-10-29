package mail

import (
	"strings"
	"testing"
)

func TestRenderWelcome(t *testing.T) {
	lines := []WelcomeLine{{"alice", "https://forum.example.org/m/welcome/t/1/-"}, {"<b>bob", "https://forum.example.org/m/welcome/t/2/-"}}
	m, err := RenderWelcome(lines, Names{Meta: "Riverside"}, "https://forum.example.org/m/welcome", "https://e.example.org/u/w.tok")
	if err != nil {
		t.Fatal(err)
	}
	if m.Subject != "Say hello to 2 new people on Riverside" || m.Unsubscribe != "https://e.example.org/u/w.tok" {
		t.Fatalf("message = %+v", m)
	}
	for _, s := range []string{"alice", "https://forum.example.org/m/welcome/t/2/-", "https://e.example.org/u/w.tok", "https://forum.example.org/m/welcome"} {
		if !strings.Contains(m.Text, s) || !strings.Contains(m.HTML, s) {
			t.Errorf("%q missing", s)
		}
	}
	if strings.Contains(m.HTML, "<b>bob") || !strings.Contains(m.HTML, "&lt;b&gt;bob") {
		t.Fatal("name not escaped in HTML")
	}
	if WelcomeSubject(1, "Riverside") != "Say hello to 1 new person on Riverside" {
		t.Fatal(WelcomeSubject(1, "Riverside"))
	}
}
