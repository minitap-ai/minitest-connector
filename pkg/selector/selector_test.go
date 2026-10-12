package selector

import "testing"

func TestParse(t *testing.T) {
	got, err := Parse([]string{" API.Example.com. ,*.stg.example.local", "api.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	want := List{"api.example.com", "*.stg.example.local"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestParseRejects(t *testing.T) {
	for _, raw := range []string{"", "localhost", "10.0.0.1", "*.", "bad_label.com", "example.123", "*.*.example.com"} {
		if _, err := Parse([]string{raw}); err == nil {
			t.Errorf("Parse(%q) succeeded, want error", raw)
		}
	}
}

func TestMatches(t *testing.T) {
	l, err := Parse([]string{"api.example.com", "*.stg.example.local"})
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]bool{
		"api.example.com":        true,
		"API.EXAMPLE.COM.":       true,
		"x.api.example.com":      false,
		"web.stg.example.local":  true,
		"a.b.stg.example.local":  true,
		"stg.example.local":      false,
		"evilstg.example.local":  false,
		"stg.example.local.evil": false,
	}
	for host, want := range cases {
		if got := l.Matches(host); got != want {
			t.Errorf("Matches(%q) = %v, want %v", host, got, want)
		}
	}
}
