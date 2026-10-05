// Command dcc-gui is dcc's desktop client: end-to-end encrypted chat between
// two people, peer to peer, with Cloudflare used only to find each other.
//
// It is a thin consumer of internal/session, like dcc-cli — all the protocol,
// crypto and connection work lives there, and what the two binaries have in
// common before their interface starts lives in internal/client. What is left
// here is the window.
package main

import (
	"flag"
	"fmt"
	"os"

	"gioui.org/app"

	"github.com/DSNR/dcc/internal/chatwindow"
	"github.com/DSNR/dcc/internal/client"
	"github.com/DSNR/dcc/internal/gui"
)

func main() {
	// Gio wants the program's main goroutine for its own event loop, so the
	// window runs beside it and the process ends when the window does.
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

	return chatwindow.Run(gui.Options{
		Name:     c.Name,
		Warnings: c.Warnings,
		Store:    c.Store,
		New:      func() (gui.Session, error) { return c.NewSession() },
	})
}
