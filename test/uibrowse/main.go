// Command uibrowse is a loopback-only harness for exercising the web client
// against the fake F-Chat server. It never contacts F-List or the live
// F-Chat network: the upstream is fakeserver (an in-process TLS endpoint) and
// the ticket endpoint is a local HTTP stub.
//
// Run it, then open http://127.0.0.1:8091 in a browser. Drive the fake
// upstream over the control port (default 127.0.0.1:8092):
//
//	curl -d '{"channel":"Frontpage","character":"Other","message":"hi"}' \
//		http://127.0.0.1:8092/say
//	curl -d '{"recipient":"Vix","character":"Other","message":"psst"}' \
//		http://127.0.0.1:8092/pm
//	curl -X POST http://127.0.0.1:8092/drop   # simulate a disconnect
//
// The UI flow is the production one: enter any account/password on the
// credentials form (the stub minter accepts everything and returns the
// configured characters), pick a character, join a channel, and chat.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os"

	"plexo/internal/core"
	"plexo/internal/fchat"
	"plexo/internal/model"
	"plexo/internal/render"
	"plexo/internal/session"
	"plexo/internal/store"
	"plexo/internal/web"
	"plexo/test/fakeserver"
	"plexo/test/fixtures"
)

func main() {
	addr := flag.String("http", "127.0.0.1:8091", "HTTP listen address for the UI")
	control := flag.String("control", "127.0.0.1:8092", "control API for injecting fake upstream events")
	flag.Parse()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	st, err := store.OpenSQLite("uibrowse.db")
	if err != nil {
		log.Fatalf("uibrowse: open store: %v", err)
	}
	defer st.Close()

	svc, err := render.New()
	if err != nil {
		log.Fatalf("uibrowse: bbcode: %v", err)
	}

	fac := fakeserver.NewWSFactory(fakeserver.Options{
		Account:   "acct",
		Character: "Vix",
		Gender:    "Female",
		Status:    "online",
		Friends:   []string{"BestFriend", "Neko", "Zoey"},
		Roster: [][]string{
			{"Other", "Male", "online", "away hunting"},
			{"Neko", "Female", "busy", ""},
			{"BestFriend", "Female", "online", ""},
			{"Rookie", "Male", "online", ""},
		},
		Channels: []fchat.OfficialChannel{
			{Name: "Frontpage", Characters: 12},
			{Name: "Fantasy", Characters: 34},
		},
		Rooms: []fchat.PublicRoom{
			{Name: "adh-abc12345", Title: "Private Room", Characters: 3},
		},
		// Static FKS reply so the search dialog has results to show; "Ghost"
		// exercises the unknown-character placeholder path.
		SearchResults: []string{"Other", "Neko", "BestFriend", "Rookie", "Ghost"},
	})
	defer fac.Close()

	// Loopback ticket stub: accepts any credentials, reports the same
	// character list. This stands in for www.f-list.net and nothing else.
	tickets := &http.Server{
		Addr:    "127.0.0.1:8093",
		Handler: http.HandlerFunc(handleTicket),
	}
	go func() {
		if err := tickets.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("uibrowse: ticket stub: %v", err)
		}
	}()

	acct := core.NewAccount(core.WithTicketURL("http://127.0.0.1:8093/"))
	mgr := core.NewManager(ctx, core.Config{
		Store:   st,
		Tickets: acct.Tickets(),
		Dial: func(string) session.Dialer {
			return session.Dialer(fac.Dial)
		},
		Renderer:        svc,
		SessionRenderer: func() model.Renderer { return svc.Clone() },
		Logger:          logger,
		// The mapping fixture is embedded, so the harness serves the mapping
		// endpoint without contacting F-List.
		Mapping: core.MappingFunc(func(context.Context) (model.MappingList, error) {
			return fchat.DecodeMappingList(fixtures.MappingList())
		}),
	})

	ui, _ := web.NewServer(*addr, web.Options{
		Manager: mgr,
		Account: acct,
		Logger:  logger,
	})
	go func() {
		fmt.Fprintf(os.Stderr, "uibrowse: UI on http://%s  control on http://%s\r\n", *addr, *control)
		if err := ui.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("uibrowse: ui server: %v", err)
		}
	}()

	controlSrv := newControl(*control, fac)
	if err := controlSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("uibrowse: control server: %v", err)
	}
}

func handleTicket(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ticket":     "fake-ticket",
		"error":      "",
		"characters": []string{"Vix", "Socks", "Other"},
	})
}

func newControl(addr string, fac *fakeserver.Factory) *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/say", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Channel   string
			Character string
			Message   string
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Channel == "" || in.Message == "" {
			http.Error(w, "channel and message required", http.StatusBadRequest)
			return
		}
		if in.Character == "" {
			in.Character = "Other"
		}
		srv := fac.Last()
		if srv == nil {
			http.Error(w, "no fake connection", http.StatusConflict)
			return
		}
		err := srv.Send("MSG", fchat.MSGEvent{Character: in.Character, Channel: in.Channel, Message: in.Message})
		writeErr(w, err)
	})
	mux.HandleFunc("/pm", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Recipient string
			Character string
			Message   string
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Recipient == "" || in.Message == "" {
			http.Error(w, "recipient and message required", http.StatusBadRequest)
			return
		}
		if in.Character == "" {
			in.Character = "Other"
		}
		srv := fac.Last()
		if srv == nil {
			http.Error(w, "no fake connection", http.StatusConflict)
			return
		}
		err := srv.Send("PRI", fchat.PRIEvent{Character: in.Character, Recipient: in.Recipient, Message: in.Message})
		writeErr(w, err)
	})
	mux.HandleFunc("/lrp", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Channel   string
			Character string
			Message   string
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Channel == "" || in.Message == "" {
			http.Error(w, "channel and message required", http.StatusBadRequest)
			return
		}
		if in.Character == "" {
			in.Character = "Other"
		}
		srv := fac.Last()
		if srv == nil {
			http.Error(w, "no fake connection", http.StatusConflict)
			return
		}
		err := srv.Send("LRP", fchat.LRPEvent{Character: in.Character, Channel: in.Channel, Message: in.Message})
		writeErr(w, err)
	})
	mux.HandleFunc("/drop", func(w http.ResponseWriter, _ *http.Request) {
		srv := fac.Last()
		if srv == nil {
			http.Error(w, "no fake connection", http.StatusConflict)
			return
		}
		writeErr(w, srv.Close())
	})
	return &http.Server{Addr: addr, Handler: mux}
}

func writeErr(w http.ResponseWriter, err error) {
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
