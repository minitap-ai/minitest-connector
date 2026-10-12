// Command relay runs in Minitap's cluster. It accepts connector sessions on a
// public listener and serves device egress CONNECT requests on a
// cluster-internal listener.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"

	"github.com/minitap-ai/minitest-connector/internal/relay"
	"github.com/minitap-ai/minitest-connector/internal/runtoken"
	"github.com/minitap-ai/minitest-connector/internal/version"
)

func main() {
	if len(os.Args) > 1 && (os.Args[1] == "version" || os.Args[1] == "-version") {
		println(version.Version)
		return
	}
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: logLevel()}))
	slog.SetDefault(log)
	if err := run(log); err != nil {
		log.Error("relay.fatal", "error", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	keys, err := runtoken.ParseKeys(os.Getenv("RELAY_RUN_TOKEN_KEYS"))
	if err != nil {
		return err
	}
	auth, err := authenticator()
	if err != nil {
		return err
	}
	internalPort := envInt("RELAY_INTERNAL_PORT", 8081)
	var peers relay.PeerFinder
	if svc := os.Getenv("RELAY_PEERS_SERVICE"); svc != "" {
		peers = &relay.DNSPeers{Service: svc, Port: internalPort, SelfIP: os.Getenv("POD_IP")}
	}
	reg := prometheus.NewRegistry()
	reg.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))

	srv := relay.New(relay.Config{
		RelayID:           os.Getenv("POD_NAME"),
		Auth:              auth,
		Tokens:            runtoken.NewVerifier(keys),
		Peers:             peers,
		TrustProxyHeaders: os.Getenv("RELAY_TRUST_PROXY_HEADERS") == "true",
		Logger:            log,
		Registerer:        reg,
	})
	public := &http.Server{
		Addr: ":" + strconv.Itoa(envInt("RELAY_PUBLIC_PORT", 8080)), Handler: srv.PublicHandler(),
		ReadHeaderTimeout: 10 * time.Second,
		// Only IdleTimeout: Read/WriteTimeout would also cut hijacked sessions.
		IdleTimeout: 60 * time.Second,
	}
	internal := &http.Server{
		Addr: ":" + strconv.Itoa(internalPort), Handler: srv.InternalHandler(reg),
		ReadHeaderTimeout: 10 * time.Second,
	}
	errs := make(chan error, 2)
	for _, s := range []*http.Server{public, internal} {
		go func() {
			if err := s.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				errs <- err
			}
		}()
	}
	log.Info("relay.started", "version", version.Version, "public", public.Addr, "internal", internal.Addr)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, os.Interrupt)
	select {
	case err := <-errs:
		return err
	case <-sig:
	}

	drainTimeout := time.Duration(envInt("RELAY_DRAIN_TIMEOUT_SECONDS", 1200)) * time.Second
	log.Info("relay.draining", "timeout_s", int(drainTimeout.Seconds()))
	ctx, cancel := context.WithTimeout(context.Background(), drainTimeout)
	defer cancel()
	_ = public.Shutdown(ctx)
	srv.Drain(ctx)
	_ = internal.Shutdown(ctx)
	log.Info("relay.stopped")
	return nil
}

func authenticator() (relay.Authenticator, error) {
	if spec := os.Getenv("RELAY_STATIC_CONNECTORS"); spec != "" {
		return relay.ParseStatic(spec)
	}
	return nil, errors.New("no connector authenticator configured (RELAY_STATIC_CONNECTORS)")
}

func envInt(name string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(name)); err == nil {
		return v
	}
	return def
}

func logLevel() slog.Level {
	if os.Getenv("LOG_LEVEL") == "DEBUG" {
		return slog.LevelDebug
	}
	return slog.LevelInfo
}
