// Command dcc-cli is dcc's terminal client: end-to-end encrypted chat between
// two people, peer to peer, with Cloudflare used only to find each other.
//
// It is a thin consumer of internal/session, like dcc-gui — all the protocol,
// crypto and connection work lives there. A Call's video appears in a window
// of its own, opened in this same process, which is why main hands the main
// goroutine to Gio and runs the terminal interface beside it.
package main

import (
	"flag"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strings"

	"gioui.org/app"

	"github.com/DSNR/dcc/internal/cli"
	"github.com/DSNR/dcc/internal/identity"
	"github.com/DSNR/dcc/internal/rendezvous"
	"github.com/DSNR/dcc/internal/session"
	"github.com/DSNR/dcc/internal/storage"
	"github.com/DSNR/dcc/internal/window"
	"github.com/DSNR/dcc/internal/wire"
)

func main() {
	// The terminal interface runs beside Gio rather than instead of it: a Call
	// opens a video window in this same process, and Gio wants the program's
	// main goroutine for its own event loop. So the TUI gets a goroutine, and
	// the process ends when the TUI does.
	go func() {
		if err := run(); err != nil {
			fmt.Fprintln(os.Stderr, "dcc:", err)
			os.Exit(1)
		}
		os.Exit(0)
	}()
	app.Main()
}

func run() error {
	name := flag.String("name", defaultName(), "the Display Name announced to the other side")
	dir := flag.String("dir", "", "the application directory (default: the dcc directory in the user config dir)")
	local := flag.Bool("local", false,
		"publish the Rendezvous on loopback instead of a cloudflared Quick Tunnel — for two clients on one machine")
	flag.Parse()

	// A Display Name the handshake would refuse is worth saying now, rather
	// than as a Session that cannot start.
	if err := wire.ValidateName(strings.TrimSpace(*name)); err != nil {
		return fmt.Errorf("-name: %w", err)
	}

	appDir := *dir
	if appDir == "" {
		found, err := identity.Dir()
		if err != nil {
			return err
		}
		appDir = found
	}
	id, reset, err := identity.Load(appDir)
	if err != nil {
		return err
	}
	var warnings []string
	if reset != nil {
		warnings = append(warnings, reset.Warning())
	}

	store, err := storage.Open(filepath.Join(appDir, storage.FileName), id.DBKey())
	if err != nil {
		return err
	}
	defer store.Close()

	// Nil means a real cloudflared Quick Tunnel.
	var tunnel rendezvous.Tunnel
	if *local {
		tunnel = rendezvous.Loopback{}
	}

	return cli.Run(cli.Options{
		Name:     *name,
		Warnings: warnings,
		Store:    store,
		// A Call's video goes in a window of its own, opened in this process:
		// no second binary, no IPC, and the terminal stays a terminal.
		Video: func(opts cli.VideoOptions) cli.VideoWindow {
			return window.Open(window.Options{
				Title:  opts.Title,
				Frames: opts.Frames,
				Failed: opts.Failed,
			})
		},
		New: func() (cli.Session, error) {
			// One Session per Invite: the TUI asks for a fresh one each time
			// it hosts or connects. Every Session pins and keeps history
			// through the one Store, which is what makes a Conversation
			// outlive them.
			s, err := session.New(session.Options{
				Identity: id,
				Name:     *name,
				Tunnel:   tunnel,
				Pins:     store,
				History:  store,
			})
			if err != nil {
				return nil, err
			}
			return s, nil
		},
	})
}

// defaultName is the Display Name to announce when none was given. It is a
// hint for the other side, never proof, so the local account name is a fair
// starting point — and it is visible on the status line to be corrected.
func defaultName() string {
	if u, err := user.Current(); err == nil {
		if name := strings.TrimSpace(u.Username); name != "" {
			return name
		}
	}
	if host, err := os.Hostname(); err == nil && host != "" {
		return host
	}
	return "someone"
}
