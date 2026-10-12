// Command minitest-connector runs inside a customer network and forwards
// Minitest device traffic for allowed domains to internal hosts, over an
// outbound-only connection to Minitap.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"go.yaml.in/yaml/v3"

	"github.com/minitap-ai/minitest-connector/internal/service"
	"github.com/minitap-ai/minitest-connector/internal/version"
	"github.com/minitap-ai/minitest-connector/pkg/connector"
	"github.com/minitap-ai/minitest-connector/pkg/selector"
)

const usage = `minitest-connector lets Minitest devices reach your private network.

Usage:
  minitest-connector run [flags]                  run in the foreground
  minitest-connector service install [flags]      install and start as an always-on service
  minitest-connector service restart              restart the service (e.g. after an upgrade)
  minitest-connector service uninstall
  minitest-connector service status
  minitest-connector version

Flags for run:
`

type fileConfig struct {
	Token string   `yaml:"token"`
	Relay string   `yaml:"relay"`
	Allow []string `yaml:"allow"`
	Ports []int    `yaml:"ports"`
}

type stringList []string

func (s *stringList) String() string     { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	switch os.Args[1] {
	case "version", "-version", "--version":
		fmt.Println(version.Version)
	case "run", "service":
		cmd := run
		if os.Args[1] == "service" {
			cmd = serviceCmd
		}
		if err := cmd(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "minitest-connector:", err)
			os.Exit(1)
		}
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, usage); fs.PrintDefaults() }
	var allow stringList
	configPath := fs.String("config", "", "YAML config file (token, relay, allow, ports)")
	relayURL := fs.String("relay", "", "relay URL (default "+connector.DefaultRelayURL+")")
	ports := fs.String("ports", "", "comma-separated destination ports to allow (default 443)")
	sessions := fs.Int("sessions", 2, "concurrent relay sessions, for redundancy")
	logFormat := fs.String("log-format", "text", "text or json")
	fs.Var(&allow, "allow", "domain allowed through the tunnel, e.g. '*.stg.example.local' (repeatable, comma-separated)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	var fc fileConfig
	if *configPath != "" {
		raw, err := os.ReadFile(*configPath)
		if err != nil {
			return err
		}
		if err := yaml.Unmarshal(raw, &fc); err != nil {
			return fmt.Errorf("config %s: %w", *configPath, err)
		}
	}

	token := firstNonEmpty(os.Getenv("MINITEST_CONNECTOR_TOKEN"), fc.Token)
	if token == "" {
		return errors.New("no token: set MINITEST_CONNECTOR_TOKEN or token in the config file")
	}
	allowRaw := []string(allow)
	if len(allowRaw) == 0 {
		allowRaw = splitNonEmpty(os.Getenv("MINITEST_CONNECTOR_ALLOW"))
	}
	if len(allowRaw) == 0 {
		allowRaw = fc.Allow
	}
	sel, err := selector.Parse(allowRaw)
	if err != nil {
		return fmt.Errorf("allow: %w", err)
	}
	portList := fc.Ports
	if p := firstNonEmpty(*ports, os.Getenv("MINITEST_CONNECTOR_PORTS")); p != "" {
		if portList, err = parsePorts(p); err != nil {
			return err
		}
	}

	var handler slog.Handler = slog.NewTextHandler(os.Stderr, nil)
	if *logFormat == "json" {
		handler = slog.NewJSONHandler(os.Stderr, nil)
	}
	hostname, _ := os.Hostname()
	c, err := connector.New(connector.Config{
		RelayURL:   firstNonEmpty(*relayURL, os.Getenv("MINITEST_CONNECTOR_RELAY"), fc.Relay),
		Token:      token,
		Allow:      sel,
		Ports:      portList,
		Sessions:   *sessions,
		Version:    version.Version,
		InstanceID: instanceID(),
		Hostname:   hostname,
		Logger:     slog.New(handler),
	})
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := c.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}

func serviceCmd(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: minitest-connector service install|restart|uninstall|status")
	}
	layout, err := service.Detect()
	if err != nil {
		return err
	}
	switch args[0] {
	case "uninstall":
		if err := service.Uninstall(layout); err != nil {
			return err
		}
		fmt.Println("minitest-connector service removed")
		return nil
	case "status":
		return service.Status(layout)
	case "restart":
		if err := service.Restart(layout); err != nil {
			return err
		}
		fmt.Println("minitest-connector service restarted")
		return nil
	case "install":
	default:
		return fmt.Errorf("unknown service command %q", args[0])
	}

	fs := flag.NewFlagSet("service install", flag.ExitOnError)
	var allow stringList
	token := fs.String("token", "", "connector token (or MINITEST_CONNECTOR_TOKEN)")
	relayURL := fs.String("relay", "", "relay URL (default "+connector.DefaultRelayURL+")")
	ports := fs.String("ports", "", "comma-separated destination ports to allow (default 443)")
	fs.Var(&allow, "allow", "domain allowed through the tunnel (repeatable, comma-separated)")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	tok := firstNonEmpty(*token, os.Getenv("MINITEST_CONNECTOR_TOKEN"))
	if tok == "" {
		return errors.New("no token: pass --token or set MINITEST_CONNECTOR_TOKEN")
	}
	sel, err := selector.Parse(allow)
	if err != nil {
		return fmt.Errorf("allow: %w", err)
	}
	var portList []int
	if *ports != "" {
		if portList, err = parsePorts(*ports); err != nil {
			return err
		}
	}
	if err := service.Install(layout, service.Config{Token: tok, Relay: *relayURL, Allow: sel, Ports: portList}); err != nil {
		return err
	}
	scope := "user service (runs while you are logged in; use sudo for a boot-time service)"
	if layout.System {
		scope = "system service (starts at boot)"
	}
	fmt.Printf("minitest-connector installed as a %s\n  config: %s\n  service: %s\n", scope, layout.ConfigPath, layout.UnitPath)
	if layout.LogPath != "" {
		fmt.Printf("  logs:   %s\n", layout.LogPath)
	}
	return nil
}

func parsePorts(s string) ([]int, error) {
	var out []int
	for _, p := range splitNonEmpty(s) {
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 || n > 65535 {
			return nil, fmt.Errorf("invalid port %q", p)
		}
		out = append(out, n)
	}
	return out, nil
}

func splitNonEmpty(s string) []string {
	var out []string
	for part := range strings.SplitSeq(s, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func instanceID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
