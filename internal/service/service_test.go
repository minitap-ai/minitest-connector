package service

import (
	"strings"
	"testing"
)

func TestLayoutAndRender(t *testing.T) {
	cases := []struct {
		goos   string
		root   bool
		unit   string
		expect []string
	}{
		{"darwin", true, "/Library/LaunchDaemons/ai.minitap.minitest-connector.plist",
			[]string{"<string>/opt/homebrew/bin/minitest-connector</string>", "/Library/Application Support/minitest-connector/config.yaml", "<key>KeepAlive</key><true/>"}},
		{"darwin", false, "/Users/qa/Library/LaunchAgents/ai.minitap.minitest-connector.plist",
			[]string{"/Users/qa/.config/minitest-connector/config.yaml", "/Users/qa/Library/Logs/minitest-connector.log"}},
		{"linux", true, "/etc/systemd/system/minitest-connector.service",
			[]string{"DynamicUser=yes", "LoadCredential=config:/etc/minitest-connector/config.yaml", "${CREDENTIALS_DIRECTORY}/config", "WantedBy=multi-user.target"}},
		{"linux", false, "/Users/qa/.config/systemd/user/minitest-connector.service",
			[]string{`run --config "/Users/qa/.config/minitest-connector/config.yaml"`, "WantedBy=default.target"}},
	}
	for _, tc := range cases {
		l, err := layoutFor(tc.goos, tc.root, "/Users/qa", "/opt/homebrew/bin/minitest-connector")
		if err != nil {
			t.Fatal(err)
		}
		if l.UnitPath != tc.unit {
			t.Errorf("%s root=%v: unit path %s, want %s", tc.goos, tc.root, l.UnitPath, tc.unit)
		}
		out, err := Render(l)
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range tc.expect {
			if !strings.Contains(string(out), want) {
				t.Errorf("%s root=%v: rendered unit missing %q:\n%s", tc.goos, tc.root, want, out)
			}
		}
		if strings.Count(string(out), "ExecStart=") > 1 {
			t.Errorf("%s root=%v: more than one ExecStart", tc.goos, tc.root)
		}
	}
}

func TestUnsupportedOS(t *testing.T) {
	if _, err := layoutFor("windows", false, "C:/", "x"); err == nil {
		t.Fatal("want error on windows")
	}
}
