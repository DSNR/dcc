// Command dcc-cli is dcc's terminal client: end-to-end encrypted chat between
// two people, peer to peer, with Cloudflare used only to find each other.
//
// It is a thin consumer of internal/session, like dcc-gui — all the protocol,
// crypto and connection work lives there, and what the two binaries have in
// common before their interface starts lives in internal/client. A Call's
// video appears in a window of its own, opened in this same process, which is
// why main hands the main goroutine to Gio and runs the terminal interface
// beside it.
package main

import (
	"flag"
	"fmt"
	"os"

	"gioui.org/app"

	"github.com/DSNR/dcc/internal/cli"
	"github.com/DSNR/dcc/internal/client"
	"github.com/DSNR/dcc/internal/window"
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
	flags := client.NewFlags(flag.CommandLine)
	flag.Parse()

	c, err := client.Open(flags)
	if err != nil {
		return err
	}
	defer c.Close()

	return cli.Run(cli.Options{
		Name:     c.Name,
		Warnings: c.Warnings,
		Store:    c.Store,
		// A Call's video goes in a window of its own, opened in this process:
		// no second binary, no IPC, and the terminal stays a terminal.
		Video: func(opts cli.VideoOptions) cli.VideoWindow {
			return window.Open(window.Options{
				Title:  opts.Title,
				Frames: opts.Frames,
				Failed: opts.Failed,
			})
		},
		New: func() (cli.Session, error) { return c.NewSession() },
	})
}
