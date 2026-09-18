// Command plexo is the Plexo core. It serves the web client, exposes the
// browser WebSocket protocol, and (as a development harness) loads the BBCode
// parser so it can be reloaded with SIGHUP without restarting.
//
// While it runs, the core shows a system-tray icon, or, with --no-systray, a
// terminal status screen.
//
// Both the parser and the UI are embedded in the binary but are read from
// ./ui and internal/render's table.json / entry_table.json when those exist on
// disk, so editing
// them is enough.
//
// F-Chat credentials are never taken from the command line: they are entered in
// the browser and validated by the core. The only password flag is --password,
// which sets the shared password that guards browser access.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"plexo/internal/config"
	"plexo/internal/console"
	"plexo/internal/core"
	"plexo/internal/fchat"
	"plexo/internal/model"
	"plexo/internal/render"
	"plexo/internal/session"
	"plexo/internal/store"
	"plexo/internal/tray"
	"plexo/internal/web"
)

func main() {
	addr := flag.String("http", "0.0.0.0:8080", "HTTP listen address for the UI")
	password := flag.String("password", os.Getenv("PLEXO_ACCESS_PASSWORD"), "shared password for browser access (or PLEXO_ACCESS_PASSWORD; empty disables)")
	dbPath := flag.String("db", "plexo.db", "SQLite history database path")
	clearHistory := flag.Bool("clear-history", false, "delete all persisted chat history, then exit")
	resetConfig := flag.Bool("reset-config", false, "delete all configuration, restoring defaults, then exit")
	fchatURL := flag.String("fchat-url", fchat.DefaultChatURL, "F-Chat WebSocket URL")
	ticketURL := flag.String("ticket-url", "", "override the F-List ticket endpoint (dev/testing)")
	mappingURL := flag.String("mapping-url", fchat.DefaultMappingURL, "F-List character field mapping endpoint")
	mappingFile := flag.String("mapping-file", fchat.DefaultMappingFixturePath, "offline mapping fixture to load instead of the network; empty fetches live (dev/testing)")
	origin := flag.String("origin", "", "Origin header for the WebSocket upgrade (optional)")
	noSystray := flag.Bool("no-systray", false, "disable the system tray and use the terminal console instead")
	flag.Parse()

	svc, err := render.New()
	if err != nil {
		log.Fatalf("plexo: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	st, err := store.OpenSQLite(*dbPath)
	if err != nil {
		log.Fatalf("plexo: open store: %v", err)
	}
	defer st.Close()

	// Maintenance flags act on the database and exit before any session starts,
	// so they are safe to run against a database the server is not using.
	if *clearHistory {
		if err := st.ClearHistory(context.Background()); err != nil {
			log.Fatalf("plexo: clear history: %v", err)
		}
		fmt.Fprintln(os.Stderr, "plexo: cleared all chat history")
	}
	if *resetConfig {
		if err := st.ConfigClear(context.Background()); err != nil {
			log.Fatalf("plexo: reset config: %v", err)
		}
		fmt.Fprintln(os.Stderr, "plexo: reset all configuration to defaults")
	}
	if *clearHistory || *resetConfig {
		return
	}

	// Logging is disabled for now: the console owns the terminal and a text
	// handler would shred the fixed screen. A later step (Windows systray) will
	// reintroduce a real sink; until then records are discarded.
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	// The shared password lives in the global config document. A password given
	// on the command line (or via PLEXO_ACCESS_PASSWORD) overwrites the stored
	// one before the core starts, so the database stays the single source of
	// truth at runtime and the flag acts as a set/reset.
	provider := config.NewProvider(st)
	globalCfg, _, err := provider.Global(ctx)
	if err != nil {
		log.Fatalf("plexo: load config: %v", err)
	}
	if *password != "" {
		globalCfg.Password = *password
		if err := provider.SaveGlobal(ctx, globalCfg); err != nil {
			log.Fatalf("plexo: store password: %v", err)
		}
	}

	acct := core.NewAccount(append(accountOptions(*ticketURL), core.WithCredentialStore(provider))...)
	// Restore seeds the checking state synchronously (so the first client sees
	// validation in flight) and revalidates any stored credentials in the
	// background.
	acct.Restore(ctx)
	mgr := core.NewManager(ctx, core.Config{
		Store:       st,
		Tickets:     acct.Tickets(),
		Credentials: acct,
		Dial: func(string) session.Dialer {
			return fchat.DialConfig{
				URL:       *fchatURL,
				Origin:    *origin,
				UserAgent: "Plexo/0.1",
			}.Dialer()
		},
		Renderer:        svc,
		SessionRenderer: func() model.Renderer { return svc.Clone() },
		Logger:          logger,
		Settings:        provider,
		Mapping:         mappingSource(*mappingFile, *mappingURL),
	})

	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatalf("plexo: listen %s: %v", *addr, err)
	}
	uiURL := uiAddress(ln.Addr())
	notice := &console.Notice{}

	server, bridge := web.NewServer(*addr, web.Options{
		Manager:       mgr,
		Account:       acct,
		Logger:        logger,
		PlexoPassword: globalCfg.Password,
	})
	go func() {
		if err := server.Serve(ln); err != nil && err != http.ErrServerClosed {
			notice.Set("http: " + err.Error())
		}
	}()

	// reloadParser recompiles the renderer's BBCode and entry-template tables;
	// reloadConfig re-reads every session's configuration from the database and
	// applies it in place. SIGHUP runs both so a daemon can reload everything at
	// once; failures are recorded on the console notice (shown only when the
	// console front end is active).
	reloadParser := func() {
		if err := svc.Reload(); err != nil {
			notice.Set("render reload: " + err.Error())
			return
		}
		notice.Set("")
	}
	reloadConfig := func() {
		if err := mgr.ReloadConfig(ctx); err != nil {
			notice.Set("config reload: " + err.Error())
		}
	}
	reloadAll := func() {
		reloadParser()
		reloadConfig()
	}

	var clients func() []string
	if bridge != nil {
		clients = bridge.Clients
	}

	// SIGHUP reloads both the parser and every session's config; the console's
	// headless path and the tray both rely on it for daemon-style reloads.
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	go func() {
		for range hup {
			reloadAll()
		}
	}()

	// The system tray is the default and --no-systray selects the terminal
	// console; platforms without a tray backend fall back to the console too.
	if tray.Available() && !*noSystray {
		go func() {
			stop := make(chan os.Signal, 1)
			signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
			<-stop
			tray.Quit()
		}()
		tray.Run(uiURL)
	} else if err := console.Run(console.Options{
		URL:      uiURL,
		Sessions: func() []string { return liveSessions(mgr) },
		Clients:  clients,
		Notice:   notice,
	}); err != nil {
		log.Fatal(err)
	}

	// Shut down in dependency order: stop the sessions (they may still write
	// history), close the HTTP server, then let the deferred store close run.
	cancel()
	_ = server.Close()
}

// accountOptions builds the account's minter options from flags.
func accountOptions(ticketURL string) []core.AccountOption {
	if ticketURL == "" {
		return nil
	}
	return []core.AccountOption{core.WithTicketURL(ticketURL)}
}

// mappingSource selects how the core loads the character field mapping data. A
// non-empty file that exists is loaded offline (the committed fixture, so the
// dev harness never queries the live endpoint); otherwise the core fetches live
// from mapping-url.
func mappingSource(file, url string) core.MappingSource {
	if file != "" {
		if _, err := os.Stat(file); err == nil {
			return fchat.MappingFile{Path: file}
		}
	}
	return fchat.MappingFetcher{URL: url}
}

// liveSessions returns the characters whose sessions are fully connected. The
// state string is the one reported in model.SessionSnapshot.State.
func liveSessions(mgr *core.Manager) []string {
	snap := mgr.Snapshot()
	out := make([]string, 0, len(snap.Sessions))
	for _, s := range snap.Sessions {
		if s.State == "live" {
			out = append(out, s.Character)
		}
	}
	return out
}

// uiAddress returns the URL a user should open for the UI. When the listener
// is bound to an unspecified host the most likely outward-facing address is
// discovered, falling back to localhost when there is no default route; a
// loopback-bound listener keeps localhost because nothing else is reachable.
func uiAddress(a net.Addr) string {
	tcp, ok := a.(*net.TCPAddr)
	if !ok {
		return "http://" + a.String()
	}
	host := "localhost"
	switch {
	case tcp.IP == nil || tcp.IP.IsUnspecified():
		if ip := outboundIP(); ip != "" {
			host = ip
		}
	case tcp.IP.IsLoopback():
		// Bound to loopback only; localhost is the honest address.
	default:
		host = tcp.IP.String()
	}
	return "http://" + net.JoinHostPort(host, fmt.Sprint(tcp.Port))
}

// outboundIP returns the local address the host would use to reach the wider
// network. A UDP dial performs only a route lookup and sends no packets, so it
// works offline as long as a default route exists. It is empty when there is
// none.
func outboundIP() string {
	c, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return ""
	}
	defer c.Close()
	a, ok := c.LocalAddr().(*net.UDPAddr)
	if !ok || a.IP == nil || a.IP.IsLoopback() || a.IP.IsUnspecified() {
		return ""
	}
	return a.IP.String()
}
