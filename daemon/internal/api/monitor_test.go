package api

import "testing"

func TestParseProbe(t *testing.T) {
	out := `login=true
disk=3221225472 63312289792
win=main rc=1 theme=0 url=https://claude.ai/code/session_ABC123
win=api rc=0 theme=1 url=
`
	st := parseProbe(out)
	if st.LoggedIn == nil || !*st.LoggedIn {
		t.Fatal("login")
	}
	if st.GuestUsed != 3221225472 || st.GuestSize != 63312289792 {
		t.Fatalf("%d %d", st.GuestUsed, st.GuestSize)
	}
	if a := st.Agents["main"]; !a.Window || !a.RC || a.URL != "https://claude.ai/code/session_ABC123" {
		t.Fatalf("%+v", a)
	}
	if a := st.Agents["api"]; a.RC || !a.Theme {
		t.Fatalf("%+v", a)
	}
	if st.stable() {
		t.Fatal("ein Agent ohne RC darf nicht stabil sein")
	}
	f := parseProbe("login=false\n")
	if f.LoggedIn == nil || *f.LoggedIn || f.stable() {
		t.Fatal("ausgeloggt")
	}
	if parseProbe("").LoggedIn != nil {
		t.Fatal("ohne Ausgabe ist der Login unbekannt")
	}
}
