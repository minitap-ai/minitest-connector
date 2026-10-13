package service

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
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

// fakeLaunchd keeps a booted-out job registered for a few polls, and refuses
// to bootstrap a job that is still registered, like launchd does.
type fakeLaunchd struct {
	loaded       bool
	unloadPolls  int
	bootstrapped int
}

func (f *fakeLaunchd) launchctl(args ...string) error {
	switch args[0] {
	case "print":
		if f.loaded && f.unloadPolls > 0 {
			f.unloadPolls--
			if f.unloadPolls == 0 {
				f.loaded = false
			}
		}
		if !f.loaded {
			return errors.New("could not find service")
		}
	case "bootout":
		f.unloadPolls = 3
	case "bootstrap":
		if f.loaded {
			return errors.New("Bootstrap failed: 5: Input/output error")
		}
		f.loaded = true
		f.bootstrapped++
	}
	return nil
}

func TestReinstallWaitsForLaunchdToUnloadThePreviousJob(t *testing.T) {
	launchd := &fakeLaunchd{}
	launchctl, pollInterval, retryDelay = launchd.launchctl, time.Millisecond, time.Millisecond
	dir := t.TempDir()
	l := Layout{OS: "darwin", ConfigPath: dir + "/config.yaml", UnitPath: dir + "/unit.plist", Binary: "/bin/true"}
	cfg := Config{Token: "mtc_test", Allow: []string{"api.internal.example.com"}}

	for range 2 {
		if err := Install(l, cfg); err != nil {
			t.Fatalf("install: %v", err)
		}
	}
	if !launchd.loaded || launchd.bootstrapped != 2 {
		t.Fatalf("loaded=%v bootstrapped=%d, want the reinstalled job running", launchd.loaded, launchd.bootstrapped)
	}
}

func TestReinstallKeepsWhatIsNotPassedAgain(t *testing.T) {
	launchctl, pollInterval, retryDelay = (&fakeLaunchd{}).launchctl, time.Millisecond, time.Millisecond
	dir := t.TempDir()
	l := Layout{OS: "darwin", ConfigPath: dir + "/config.yaml", UnitPath: dir + "/unit.plist", Binary: "/bin/true"}
	first := Config{Token: "mtc_test", Relay: "wss://relay.example.com/v1/connect", Allow: []string{"a.example.com"}, Ports: []int{8443}}
	if err := Install(l, first); err != nil {
		t.Fatal(err)
	}

	prev, err := Installed(l)
	if err != nil {
		t.Fatal(err)
	}
	if err := Install(l, Config{Allow: []string{"b.example.com"}}.Over(prev)); err != nil {
		t.Fatal(err)
	}

	got, err := Installed(l)
	if err != nil {
		t.Fatal(err)
	}
	want := Config{Token: first.Token, Relay: first.Relay, Allow: []string{"b.example.com"}, Ports: first.Ports}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("config after reinstall = %+v, want %+v", got, want)
	}
}
