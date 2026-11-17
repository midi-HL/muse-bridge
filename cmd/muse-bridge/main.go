package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
	_ "time/tzdata"

	"github.com/midi-HL/muse-bridge/internal/admin"
	"github.com/midi-HL/muse-bridge/internal/channels/telegram"
	"github.com/midi-HL/muse-bridge/internal/cli"
	"github.com/midi-HL/muse-bridge/internal/config"
	"github.com/midi-HL/muse-bridge/internal/core"
	"github.com/midi-HL/muse-bridge/internal/hatch"
	"github.com/midi-HL/muse-bridge/internal/httpapi"
	"github.com/midi-HL/muse-bridge/internal/secure"
	"github.com/midi-HL/muse-bridge/internal/store"
)

var version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	runner := cli.Runner{Serve: serve, EphemeralChat: ephemeralChat}
	if e := runner.Run(ctx, os.Args[1:]); e != nil {
		fmt.Fprintln(os.Stderr, e.Error())
		os.Exit(1)
	}
}
func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
func serve(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	addr := flags.String("addr", env("MUSE_ADDR", "127.0.0.1:8787"), "")
	dataDir := flags.String("data-dir", env("MUSE_DATA_DIR", "data"), "")
	configPath := flags.String("config", os.Getenv("MUSE_CONFIG"), "")
	if flags.Parse(args) != nil || flags.NArg() != 0 {
		return errors.New("invalid serve options")
	}
	host, _, e := net.SplitHostPort(*addr)
	if e != nil {
		return errors.New("MUSE_ADDR must be a host:port address")
	}
	ip := net.ParseIP(host)
	loopback := host == "localhost" || ip != nil && ip.IsLoopback()
	vault, e := secure.NewVault(os.Getenv("MUSE_MASTER_KEY"))
	if e != nil {
		return errors.New("MUSE_MASTER_KEY must contain a base64 encoded 32-byte key")
	}
	values, warnings, e := config.Load(*configPath)
	if e != nil {
		return errors.New("configuration is invalid or unreadable")
	}
	for range warnings {
		fmt.Fprintln(os.Stderr, "configuration warning: unknown field ignored")
	}
	db, e := store.Open(*dataDir, vault)
	if e != nil {
		return errors.New("cannot open encrypted database; verify data directory and master key")
	}
	defer db.Close()
	redactor := secure.NewRedactor()
	redactor.Add(os.Getenv("MUSE_MASTER_KEY"))
	redactor.Add(os.Getenv("MUSE_SETUP_TOKEN"))
	service, e := core.New(context.Background(), db, vault, redactor, *dataDir, values)
	if e != nil {
		return errors.New("cannot initialize service")
	}
	defer service.Close()
	var telegramChannel *telegram.Channel
	if token, ok := values["telegram.token"].(string); ok && token != "" {
		telegramChannel, e = startTelegram(ctx, service, values)
		if e != nil {
			return errors.New("cannot initialize Telegram channel")
		}
		defer telegramChannel.Close()
	}
	auth := admin.NewAuth(db, os.Getenv("MUSE_SETUP_TOKEN"))
	if !loopback && auth.SetupRequired(ctx) && os.Getenv("MUSE_SETUP_TOKEN") == "" {
		return errors.New("first setup on a non-loopback listener requires MUSE_SETUP_TOKEN")
	}
	keys := admin.NewKeys(db, redactor)
	keys.Timezone = func() string { v, _ := service.Settings()["general.timezone"].(string); return v }
	handler := httpapi.New(httpapi.Options{Auth: auth, Keys: keys, Backend: service, LoopbackOnly: loopback, SetupTokenRequired: os.Getenv("MUSE_SETUP_TOKEN") != ""})
	server := httpapi.HTTPServer(*addr, handler)
	server.BaseContext = func(net.Listener) context.Context { return context.Background() }
	listener, e := net.Listen("tcp", *addr)
	if e != nil {
		return errors.New("cannot listen on requested address")
	}
	defer listener.Close()
	fmt.Fprintf(os.Stderr, "Muse Bridge %s listening on %s\n", version, listener.Addr())
	stopped := make(chan error, 1)
	go func() { stopped <- server.Serve(listener) }()
	select {
	case err := <-stopped:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return errors.New("HTTP server stopped unexpectedly")
		}
		return nil
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if server.Shutdown(shutdown) != nil {
			_ = service.Close()
			_ = server.Close()
		}
		<-stopped
		return nil
	}
}
func ephemeralChat(ctx context.Context, raw, prompt, model string, emit func(string) error) error {
	// This adapter lives for one command, uses the same hatch client, and never opens a database.
	cookies := map[string]string{}
	if json.Unmarshal([]byte(raw), &cookies) != nil {
		var envelope map[string]any
		if json.Unmarshal([]byte(raw), &envelope) == nil {
			if nested, ok := envelope["cookies"].(map[string]any); ok {
				for k, v := range nested {
					if value, ok := v.(string); ok {
						cookies[k] = value
					}
				}
			}
		}
		if len(cookies) == 0 {
			for _, part := range strings.Split(raw, ";") {
				k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
				if ok {
					cookies[k] = v
				}
			}
		}
	}
	allowed := map[string]string{}
	redactor := secure.NewRedactor()
	redactor.Add(raw)
	for _, k := range []string{"hatch_gw", "hatch_sess", "hatch_vml"} {
		if v := cookies[k]; v != "" {
			allowed[k] = v
			redactor.Add(v)
		}
	}
	if len(allowed) == 0 {
		return errors.New("cookie source has no Muse cookies")
	}
	if model != "muse-spark" && model != "muse-image" && model != "muse-video" && model != "gpt-4o-mini" {
		return errors.New("unknown model")
	}
	client := hatch.New(hatch.Config{Cookies: allowed})
	defer client.Close()
	run, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	_, e := client.Generate(run, hatch.GenerateRequest{Prompt: prompt, Model: model}, func(event hatch.Event) error {
		if event.Type == "text" {
			return emit(redactor.Text(event.Text))
		}
		return nil
	})
	return e
}
