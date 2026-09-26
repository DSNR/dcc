// Package client is everything dcc's two binaries do before their interface
// starts: the flags they both take, the Identity they load, the Store they
// open and the Tunnel they will host over. A terminal and a window differ in
// how they show a Session, never in what a Session is made of, so what is
// left in each main is the interface itself.
package client

import (
	"flag"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strings"

	"github.com/DSNR/dcc/internal/identity"
	"github.com/DSNR/dcc/internal/rendezvous"
	"github.com/DSNR/dcc/internal/session"
	"github.com/DSNR/dcc/internal/storage"
	"github.com/DSNR/dcc/internal/wire"
)

// Client is one binary's share of dcc: the Identity it is, the Store it
// keeps, and the means to start a Session. It is what both mains hand their
// interface.
type Client struct {
	// Name is the Display Name announced to the other side.
	Name string
	// Store is the local history, which is also where Identity pins live.
	// Closing the Client closes it.
	Store *storage.Store
	// Warnings are what has to be put in front of the participant at
	// startup — an Identity that had to be reset, chiefly.
	Warnings []string

	id     identity.Identity
	tunnel rendezvous.Tunnel
}

// Flags declares the flags both binaries take, against a FlagSet the caller
// parses. Keeping them here is what stops the two binaries growing different
// ideas of what -name or -local mean.
type Flags struct {
	name  *string
	dir   *string
	local *bool
}

// Flags registers the common flags on a FlagSet.
func NewFlags(fs *flag.FlagSet) *Flags {
	return &Flags{
		name: fs.String("name", DefaultName(), "the Display Name announced to the other side"),
		dir: fs.String("dir", "",
			"the application directory (default: the dcc directory in the user config dir)"),
		local: fs.Bool("local", false,
			"publish the Rendezvous on loopback instead of a cloudflared Quick Tunnel — for two clients on one machine"),
	}
}

// Open turns parsed flags into a Client: the Identity loaded, the Store open
// and the Tunnel chosen. The caller must Close what it gets back.
func Open(f *Flags) (*Client, error) {
	name := strings.TrimSpace(*f.name)
	// A Display Name the handshake would refuse is worth saying now, rather
	// than as a Session that cannot start.
	if err := wire.ValidateName(name); err != nil {
		return nil, fmt.Errorf("-name: %w", err)
	}

	dir := *f.dir
	if dir == "" {
		found, err := identity.Dir()
		if err != nil {
			return nil, err
		}
		dir = found
	}
	id, reset, err := identity.Load(dir)
	if err != nil {
		return nil, err
	}
	var warnings []string
	if reset != nil {
		warnings = append(warnings, reset.Warning())
	}

	store, err := storage.Open(filepath.Join(dir, storage.FileName), id.DBKey())
	if err != nil {
		return nil, err
	}

	c := &Client{Name: name, Store: store, Warnings: warnings, id: id}
	if *f.local {
		// Nil would mean a real cloudflared Quick Tunnel.
		c.tunnel = rendezvous.Loopback{}
	}
	return c, nil
}

// NewSession mints a Session. One Session runs once — Invite to disconnect —
// so an interface asks for a fresh one every time it hosts or connects. Every
// Session pins and keeps history through the one Store, which is what makes a
// Conversation outlive them.
func (c *Client) NewSession() (*session.Session, error) {
	return session.New(session.Options{
		Identity: c.id,
		Name:     c.Name,
		Tunnel:   c.tunnel,
		Pins:     c.Store,
		History:  c.Store,
	})
}

// Close releases what the Client holds.
func (c *Client) Close() error { return c.Store.Close() }

// DefaultName is the Display Name to announce when none was given. It is a
// hint for the other side, never proof, so the local account name is a fair
// starting point — and it is on screen in both interfaces to be corrected.
func DefaultName() string {
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
