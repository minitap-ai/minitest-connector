// Package service installs the connector as an always-on OS service:
// launchd on macOS, systemd on Linux. Run as root it installs a system-wide
// service (starts at boot, no login needed); otherwise a per-user one.
package service

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"text/template"
	"time"

	"go.yaml.in/yaml/v3"
)

// Label is the launchd label and systemd unit name.
const Label = "ai.minitap.minitest-connector"

const unitName = "minitest-connector.service"

// Config is what the service runs with. It is written to a 0600 file so the
// token never appears in the process list or the service definition.
type Config struct {
	Token string   `yaml:"token"`
	Relay string   `yaml:"relay,omitempty"`
	Allow []string `yaml:"allow"`
	Ports []int    `yaml:"ports,omitempty"`
}

// Layout is where files go for the current platform and privilege.
type Layout struct {
	OS         string
	System     bool
	ConfigPath string
	UnitPath   string
	LogPath    string
	Binary     string
}

// Detect returns the layout for this machine.
func Detect() (Layout, error) {
	bin, err := os.Executable()
	if err != nil {
		return Layout{}, err
	}
	// Keep symlinks unresolved: Homebrew's /opt/homebrew/bin link survives
	// upgrades, the versioned Cellar path it points to does not.
	home, _ := os.UserHomeDir()
	return layoutFor(runtime.GOOS, os.Geteuid() == 0, home, bin)
}

func layoutFor(goos string, root bool, home, bin string) (Layout, error) {
	l := Layout{OS: goos, System: root, Binary: bin}
	switch {
	case goos == "darwin" && root:
		l.ConfigPath = "/Library/Application Support/minitest-connector/config.yaml"
		l.UnitPath = "/Library/LaunchDaemons/" + Label + ".plist"
		l.LogPath = "/Library/Logs/minitest-connector.log"
	case goos == "darwin":
		l.ConfigPath = filepath.Join(home, ".config/minitest-connector/config.yaml")
		l.UnitPath = filepath.Join(home, "Library/LaunchAgents", Label+".plist")
		l.LogPath = filepath.Join(home, "Library/Logs/minitest-connector.log")
	case goos == "linux" && root:
		l.ConfigPath = "/etc/minitest-connector/config.yaml"
		l.UnitPath = "/etc/systemd/system/" + unitName
	case goos == "linux":
		l.ConfigPath = filepath.Join(home, ".config/minitest-connector/config.yaml")
		l.UnitPath = filepath.Join(home, ".config/systemd/user", unitName)
	default:
		return l, fmt.Errorf("service install is not supported on %s; run `minitest-connector run` under your own supervisor", goos)
	}
	return l, nil
}

var plist = template.Must(template.New("plist").Parse(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>{{.Label}}</string>
  <key>ProgramArguments</key>
  <array>
    <string>{{.Binary}}</string>
    <string>run</string>
    <string>--config</string>
    <string>{{.ConfigPath}}</string>
  </array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>ThrottleInterval</key><integer>10</integer>
  <key>StandardOutPath</key><string>{{.LogPath}}</string>
  <key>StandardErrorPath</key><string>{{.LogPath}}</string>
</dict>
</plist>
`))

var systemdUnit = template.Must(template.New("unit").Parse(`[Unit]
Description=Minitest connector (private network tunnel)
After=network-online.target
Wants=network-online.target

[Service]
Restart=always
RestartSec=5
{{- if .System}}
# Runs unprivileged; the root-only config is handed over as a credential.
DynamicUser=yes
LoadCredential=config:{{.ConfigPath}}
ExecStart="{{.Binary}}" run --config ${CREDENTIALS_DIRECTORY}/config
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=yes
PrivateTmp=yes
{{- else}}
ExecStart="{{.Binary}}" run --config "{{.ConfigPath}}"
{{- end}}

[Install]
WantedBy={{if .System}}multi-user.target{{else}}default.target{{end}}
`))

// Render returns the service definition for l.
func Render(l Layout) ([]byte, error) {
	var buf bytes.Buffer
	data := struct {
		Layout
		Label string
	}{l, Label}
	tpl := systemdUnit
	if l.OS == "darwin" {
		tpl = plist
	}
	if err := tpl.Execute(&buf, data); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Install writes the config and the service definition, then starts it.
func Install(l Layout, cfg Config) error {
	if cfg.Token == "" || len(cfg.Allow) == 0 {
		return errors.New("token and at least one allowed domain are required")
	}
	raw, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	if err := writeFile(l.ConfigPath, raw, 0o600); err != nil {
		return err
	}
	unit, err := Render(l)
	if err != nil {
		return err
	}
	_ = stop(l) // reinstall: replace a running service
	if err := writeFile(l.UnitPath, unit, 0o644); err != nil {
		return err
	}
	return start(l)
}

// Restart restarts an installed service so it runs the binary currently on disk.
func Restart(l Layout) error {
	if _, err := os.Stat(l.UnitPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("no service installed at %s: run service install first", l.UnitPath)
		}
		return err
	}
	if l.OS != "darwin" {
		return run("systemctl", append(userFlag(l), "restart", unitName)...)
	}
	if !loaded(l) {
		return start(l) // installed but not loaded
	}
	return run("launchctl", "kickstart", "-k", launchdTarget(l))
}

// Uninstall stops the service and removes its files.
func Uninstall(l Layout) error {
	_ = stop(l)
	for _, p := range []string{l.UnitPath, l.ConfigPath} {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

// Status prints the service manager's view of the service.
func Status(l Layout) error {
	if l.OS == "darwin" {
		return run("launchctl", "print", launchdTarget(l))
	}
	return run("systemctl", append(userFlag(l), "status", "--no-pager", unitName)...)
}

func start(l Layout) error {
	if l.OS == "darwin" {
		return bootstrap(l)
	}
	if err := run("systemctl", append(userFlag(l), "daemon-reload")...); err != nil {
		return err
	}
	return run("systemctl", append(userFlag(l), "enable", "--now", unitName)...)
}

func stop(l Layout) error {
	if l.OS == "darwin" {
		err := launchctl("bootout", launchdTarget(l))
		waitUnloaded(l)
		return err
	}
	return exec.Command("systemctl", append(userFlag(l), "disable", "--now", unitName)...).Run()
}

var (
	unloadTimeout  = 30 * time.Second
	pollInterval   = 200 * time.Millisecond
	bootstrapTries = 5
	retryDelay     = time.Second
)

var launchctl = func(args ...string) error {
	out, err := exec.Command("launchctl", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("launchctl %v: %w: %s", args, err, bytes.TrimSpace(out))
	}
	return nil
}

func loaded(l Layout) bool { return launchctl("print", launchdTarget(l)) == nil }

// bootout returns before launchd has removed the job, and bootstrapping it
// meanwhile fails with "5: Input/output error".
func waitUnloaded(l Layout) {
	for deadline := time.Now().Add(unloadTimeout); loaded(l) && time.Now().Before(deadline); {
		time.Sleep(pollInterval)
	}
}

func bootstrap(l Layout) error {
	var err error
	for attempt := range bootstrapTries {
		if attempt > 0 {
			time.Sleep(retryDelay)
		}
		if err = launchctl("bootstrap", launchdDomain(l), l.UnitPath); err == nil {
			return nil
		}
	}
	return err
}

func launchdDomain(l Layout) string {
	if l.System {
		return "system"
	}
	return fmt.Sprintf("gui/%d", os.Getuid())
}

func launchdTarget(l Layout) string { return launchdDomain(l) + "/" + Label }

func userFlag(l Layout) []string {
	if l.System {
		return nil
	}
	return []string{"--user"}
}

func writeFile(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil { //nolint:gosec // service directories are world-readable by convention
		return err
	}
	if err := os.WriteFile(path, data, mode); err != nil {
		return err
	}
	return os.Chmod(path, mode)
}

func run(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %v: %w", name, args, err)
	}
	return nil
}
