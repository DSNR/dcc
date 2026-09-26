// Package chatwindow is dcc's chat window: Gio widgets over a gui.Model,
// which is where everything the window knows actually lives. Splitting the
// two is what keeps the desktop client testable — Gio needs a windowing
// system, and on Linux that means cgo and display headers at build time,
// which nothing in internal/gui should need in order to be tested.
//
// Gio wants the process's main goroutine for its event loop, so a program
// that opens this window must call app.Main from main — see cmd/dcc-gui.
package chatwindow

import (
	"image/color"
	"io"
	"strings"
	"time"

	"gioui.org/app"
	"gioui.org/font"
	"gioui.org/io/clipboard"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/text"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"github.com/DSNR/dcc/internal/gui"
	"github.com/DSNR/dcc/internal/identity"
	"github.com/DSNR/dcc/internal/wire"
	"github.com/DSNR/dcc/internal/words"
)

// The window itself: Gio widgets over a gui.Model, and nothing else. Every button
// is on screen at all times and greys out when it would do nothing, so that
// what dcc can do is visible rather than remembered — which is the whole
// difference between this and the terminal client.

// The window's starting size, in device-independent pixels: wide enough that
// an Invite is one line and the history panel does not crowd the
// conversation.
const (
	startWidth  = 900
	startHeight = 640
)

// inputRunes caps one message at a quarter of the wire's byte cap, so that
// anything that can be typed can be sent even if every character of it is a
// four-byte emoji.
const inputRunes = wire.MaxTextBytes / 4

// teardownGrace is how long the window waits, after it has been closed, for
// the Session to let go of what it owns. The wait is what stands between
// quitting and an orphaned cloudflared; the bound is what stands between a
// wedged teardown and a process nobody can kill.
const teardownGrace = 10 * time.Second

// Run opens the window and returns when the participant closes it and the
// Session has been let go. Gio wants the process's main goroutine for its
// event loop, so the program that calls this must call it from main — see
// cmd/dcc-gui.
func Run(opts gui.Options) error {
	win := new(app.Window)
	win.Option(
		app.Title("dcc"),
		app.Size(unit.Dp(startWidth), unit.Dp(startHeight)),
	)
	// Every change to what is on screen — a Session event, a Store read —
	// asks the window for a frame.
	opts.Repaint = win.Invalidate
	return newUI(gui.New(opts)).run(win)
}

// ui is the window's own state: the widgets, and which of them the
// participant is in the middle of using. Everything it knows about the
// Session it reads from a Screen.
type ui struct {
	model *gui.Model
	th    *material.Theme

	conversation widget.List
	history      widget.List
	input        widget.Editor
	invite       widget.Editor

	host, connect, disconnect widget.Clickable
	send, copy, panel         widget.Clickable
	accept, refuse            widget.Clickable
	confirm, cancel           widget.Clickable

	// rows are the history panel's per-Conversation buttons, kept by Peer so
	// that a list that changes under them cannot hand a click to the wrong
	// Conversation.
	rows map[identity.PublicKey]*row
	// clearing is the Conversation a delete has been asked for and not yet
	// confirmed. Deleting history is the one thing in dcc that destroys
	// something, so it is asked twice.
	clearing *gui.Conversation
	// showHistory is whether the history panel is open.
	showHistory bool
}

// row is one Conversation's buttons in the history panel.
type row struct {
	open  widget.Clickable
	clear widget.Clickable
}

func newUI(m *gui.Model) *ui {
	u := &ui{
		model: m,
		th:    theme(),
		rows:  make(map[identity.PublicKey]*row),
	}
	u.conversation.Axis = layout.Vertical
	// The conversation follows the newest message, which is where someone
	// who has not scrolled back is looking.
	u.conversation.ScrollToEnd = true
	u.history.Axis = layout.Vertical

	u.input.Submit = true
	u.input.MaxLen = inputRunes
	u.invite.Submit = true
	u.invite.SingleLine = true
	return u
}

// theme is the window's look: Gio's own, shaped through the collection that
// carries the emoji fallback.
func theme() *material.Theme {
	th := material.NewTheme()
	th.Shaper = text.NewShaper(text.WithCollection(collection()))
	return th
}

// run is the window's event loop. It ends when the window is destroyed, and
// not before the Session has been let go.
func (u *ui) run(win *app.Window) error {
	var ops op.Ops
	for {
		switch e := win.Event().(type) {
		case app.DestroyEvent:
			// The window is already gone; what is left is the Rendezvous and
			// the connection behind it.
			u.model.Quit()
			select {
			case <-u.model.Done():
			case <-time.After(teardownGrace):
			}
			return e.Err
		case app.FrameEvent:
			gtx := app.NewContext(&ops, e)
			u.frame(gtx)
			e.Frame(gtx.Ops)
		}
	}
}

// frame draws one frame: the Screen as it stands, with whatever the
// participant did since the last one already folded into it.
func (u *ui) frame(gtx layout.Context) layout.Dimensions {
	s := u.model.Screen()
	u.acted(gtx, s)
	// Acting may have changed everything — a message sent, a prompt
	// answered — so the frame is drawn from a fresh snapshot.
	return u.layout(gtx, u.model.Screen())
}

// acted turns this frame's clicks and keystrokes into what they mean. It runs
// before anything is drawn, so a click is never a frame behind.
func (u *ui) acted(gtx layout.Context, s gui.Screen) {
	// The message box: enter sends, shift+enter is a new line.
	for {
		ev, ok := u.input.Update(gtx)
		if !ok {
			break
		}
		if _, submitted := ev.(widget.SubmitEvent); submitted {
			u.submit()
		}
	}
	for {
		ev, ok := u.invite.Update(gtx)
		if !ok {
			break
		}
		if _, submitted := ev.(widget.SubmitEvent); submitted && s.Controls.Join {
			u.join()
		}
	}

	if u.send.Clicked(gtx) && s.Controls.Send {
		u.submit()
	}
	if u.host.Clicked(gtx) && s.Controls.Host {
		u.model.Host()
	}
	if u.connect.Clicked(gtx) && s.Controls.Join {
		u.join()
	}
	if u.disconnect.Clicked(gtx) && s.Controls.Disconnect {
		u.model.Disconnect()
	}
	if u.copy.Clicked(gtx) && s.Invite != "" {
		gtx.Execute(clipboard.WriteCmd{
			Type: "application/text",
			Data: io.NopCloser(strings.NewReader(s.Invite)),
		})
	}
	if u.panel.Clicked(gtx) {
		u.showHistory = !u.showHistory
		if u.showHistory {
			u.model.RefreshHistory()
		}
	}
	if s.Prompt != nil {
		if u.accept.Clicked(gtx) {
			u.model.Accept()
		}
		if u.refuse.Clicked(gtx) {
			u.model.Refuse()
		}
	}
	u.actedOnHistory(gtx, s)
}

// actedOnHistory is the history panel's clicks: opening a Conversation, and
// the two steps it takes to delete one.
func (u *ui) actedOnHistory(gtx layout.Context, s gui.Screen) {
	if u.clearing != nil {
		if u.confirm.Clicked(gtx) {
			u.model.Clear(*u.clearing)
			u.clearing = nil
		}
		if u.cancel.Clicked(gtx) {
			u.clearing = nil
		}
	}
	for _, c := range s.Conversations {
		r := u.row(c)
		if r.open.Clicked(gtx) {
			u.model.Open(c)
		}
		if r.clear.Clicked(gtx) {
			asked := c
			u.clearing = &asked
		}
	}
}

// row is one Conversation's buttons, minted the first time it is listed.
func (u *ui) row(c gui.Conversation) *row {
	r, known := u.rows[c.Peer]
	if !known {
		r = &row{}
		u.rows[c.Peer] = r
	}
	return r
}

// submit sends what is in the message box, and empties it only if it went: a
// message that could not be sent stays where it can be sent again.
func (u *ui) submit() {
	body := strings.TrimSpace(u.input.Text())
	if body == "" {
		u.input.SetText("")
		return
	}
	if u.model.Send(body) {
		u.input.SetText("")
	}
}

// join connects to whatever is in the Invite box, and empties it: an Invite
// is used once.
func (u *ui) join() {
	invite := u.invite.Text()
	u.invite.SetText("")
	u.model.Join(invite)
}

// The window's colours, beyond the theme's own: a status bar that reads as a
// bar, a panel that stands out from the conversation, and a muted button that
// reads as one that would do nothing.
var (
	barBg    = color.NRGBA{R: 0xEC, G: 0xEF, B: 0xF4, A: 0xFF}
	panelBg  = color.NRGBA{R: 0xFF, G: 0xF4, B: 0xD6, A: 0xFF}
	dangerBg = color.NRGBA{R: 0xB3, G: 0x2D, B: 0x2D, A: 0xFF}
	mutedBg  = color.NRGBA{R: 0xC4, G: 0xC8, B: 0xD0, A: 0xFF}
	mutedFg  = color.NRGBA{R: 0x6A, G: 0x6F, B: 0x78, A: 0xFF}
	noticeFg = color.NRGBA{R: 0x50, G: 0x55, B: 0x60, A: 0xFF}
	stampFg  = color.NRGBA{R: 0x8A, G: 0x8F, B: 0x98, A: 0xFF}
)

// pad is the window's one spacing unit.
const pad = unit.Dp(8)

// layout draws the whole window: the controls across the top, the
// conversation and the history panel in the middle, the standing Security
// Code prompt over the message box, and the message box at the bottom.
func (u *ui) layout(gtx layout.Context, s gui.Screen) layout.Dimensions {
	paint.Fill(gtx.Ops, u.th.Bg)
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return u.controls(gtx, s)
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return u.inviteBar(gtx, s)
		}),
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Horizontal}.Layout(gtx,
				layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
					return u.entries(gtx, s)
				}),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return u.historyPanel(gtx, s)
				}),
			)
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return u.prompt(gtx, s)
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return u.messageBox(gtx, s)
		}),
	)
}

// controls is the top bar: where the Session stands, and every button that
// starts or ends one.
func (u *ui) controls(gtx layout.Context, s gui.Screen) layout.Dimensions {
	return bar(gtx, barBg, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(material.Body2(u.th, s.Status).Layout),
			layout.Rigid(layout.Spacer{Height: pad}.Layout),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return u.button(gtx, &u.host, "Host a Session", s.Controls.Host, u.th.ContrastBg)
					}),
					layout.Rigid(layout.Spacer{Width: pad}.Layout),
					layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
						return u.editor(gtx, &u.invite, "Paste an Invite to join…")
					}),
					layout.Rigid(layout.Spacer{Width: pad}.Layout),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return u.button(gtx, &u.connect, "Connect", s.Controls.Join, u.th.ContrastBg)
					}),
					layout.Rigid(layout.Spacer{Width: pad}.Layout),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return u.button(gtx, &u.disconnect, "Disconnect", s.Controls.Disconnect, dangerBg)
					}),
					layout.Rigid(layout.Spacer{Width: pad}.Layout),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return u.button(gtx, &u.panel, u.historyLabel(), true, u.th.ContrastBg)
					}),
				)
			}),
		)
	})
}

// historyLabel says what the history button will do, which is the only way a
// toggle can be honest about which way it is pointing.
func (u *ui) historyLabel() string {
	if u.showHistory {
		return "Hide history"
	}
	return "History"
}

// inviteBar is the Invite, once there is one to hand over: the string in full
// and a button that copies it, because an Invite that has to be retyped is an
// Invite that gets mistyped.
func (u *ui) inviteBar(gtx layout.Context, s gui.Screen) layout.Dimensions {
	if s.Invite == "" {
		return layout.Dimensions{}
	}
	return bar(gtx, panelBg, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(material.Body2(u.th, "Invite: ").Layout),
			layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
				invite := material.Body2(u.th, s.Invite)
				invite.Font.Typeface = "monospace"
				invite.MaxLines = 1
				return invite.Layout(gtx)
			}),
			layout.Rigid(layout.Spacer{Width: pad}.Layout),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return u.button(gtx, &u.copy, "Copy", true, u.th.ContrastBg)
			}),
		)
	})
}

// entries is the conversation area.
func (u *ui) entries(gtx layout.Context, s gui.Screen) layout.Dimensions {
	return layout.UniformInset(pad).Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return material.List(u.th, &u.conversation).Layout(gtx, len(s.Entries),
			func(gtx layout.Context, i int) layout.Dimensions {
				return u.entry(gtx, s.Entries[i])
			})
	})
}

// entry is one line of the conversation: the time it happened, who said it,
// and how far it got if this side sent it.
func (u *ui) entry(gtx layout.Context, e gui.Entry) layout.Dimensions {
	return layout.Inset{Top: unit.Dp(2), Bottom: unit.Dp(2)}.Layout(gtx,
		func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Start}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					at := material.Caption(u.th, e.Stamp())
					at.Color = stampFg
					return at.Layout(gtx)
				}),
				layout.Rigid(layout.Spacer{Width: pad}.Layout),
				layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
					return u.body(gtx, e)
				}),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					marker := e.Marker()
					if marker == "" {
						return layout.Dimensions{}
					}
					m := material.Caption(u.th, " "+marker)
					m.Color = stampFg
					return m.Layout(gtx)
				}),
			)
		})
}

// body is one entry's text: a notice reads as dcc talking, a message reads as
// the person who sent it.
func (u *ui) body(gtx layout.Context, e gui.Entry) layout.Dimensions {
	if e.Notice() {
		notice := material.Body2(u.th, e.Body)
		notice.Color = noticeFg
		return notice.Layout(gtx)
	}
	return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Start}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			who := material.Body1(u.th, e.Who+": ")
			who.Font.Weight = font.Bold
			return who.Layout(gtx)
		}),
		layout.Flexed(1, material.Body1(u.th, e.Body).Layout),
	)
}

// historyPanel lists the stored Conversations, each openable into the
// conversation area and deletable from this device.
func (u *ui) historyPanel(gtx layout.Context, s gui.Screen) layout.Dimensions {
	if !u.showHistory {
		return layout.Dimensions{}
	}
	gtx.Constraints.Min.X = gtx.Dp(unit.Dp(260))
	gtx.Constraints.Max.X = gtx.Constraints.Min.X
	return bar(gtx, barBg, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(material.Body1(u.th, "Stored on this device").Layout),
			layout.Rigid(layout.Spacer{Height: pad}.Layout),
			layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
				if len(s.Conversations) == 0 {
					empty := material.Body2(u.th, "Nothing yet — history appears once a Conversation has happened.")
					empty.Color = noticeFg
					return empty.Layout(gtx)
				}
				return material.List(u.th, &u.history).Layout(gtx, len(s.Conversations),
					func(gtx layout.Context, i int) layout.Dimensions {
						return u.conversationRow(gtx, s.Conversations[i])
					})
			}),
		)
	})
}

// conversationRow is one stored Conversation in the panel, with the
// confirmation in place of its buttons while a delete is being asked about.
func (u *ui) conversationRow(gtx layout.Context, c gui.Conversation) layout.Dimensions {
	asked := u.clearing != nil && u.clearing.Peer == c.Peer
	return layout.Inset{Bottom: pad}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(material.Body1(u.th, words.Quoted(c.Name)).Layout),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				summary := material.Caption(u.th, c.Summary)
				summary.Color = noticeFg
				return summary.Layout(gtx)
			}),
			layout.Rigid(layout.Spacer{Height: unit.Dp(4)}.Layout),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				if asked {
					return u.confirmClear(gtx)
				}
				r := u.row(c)
				return layout.Flex{Axis: layout.Horizontal}.Layout(gtx,
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return u.button(gtx, &r.open, "Read", true, u.th.ContrastBg)
					}),
					layout.Rigid(layout.Spacer{Width: pad}.Layout),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return u.button(gtx, &r.clear, "Clear", true, dangerBg)
					}),
				)
			}),
		)
	})
}

// confirmClear is the second question a delete has to answer. It says what
// clearing does and does not do: this device's copy goes, the other side's
// stays.
func (u *ui) confirmClear(gtx layout.Context) layout.Dimensions {
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(material.Caption(u.th, "Delete this device's copy? Theirs is untouched.").Layout),
		layout.Rigid(layout.Spacer{Height: unit.Dp(4)}.Layout),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Horizontal}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return u.button(gtx, &u.confirm, "Delete", true, dangerBg)
				}),
				layout.Rigid(layout.Spacer{Width: pad}.Layout),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return u.button(gtx, &u.cancel, "Keep", true, u.th.ContrastBg)
				}),
			)
		}),
	)
}

// prompt is the standing Security Code prompt: the code in the groups it is
// meant to be read in, what to do with it, and the two answers. It sits over
// the message box, which is dead until it is answered, because no content
// flows until then.
func (u *ui) prompt(gtx layout.Context, s gui.Screen) layout.Dimensions {
	if s.Prompt == nil {
		return layout.Dimensions{}
	}
	p := s.Prompt
	half := len(p.Groups) / 2
	return bar(gtx, panelBg, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				title := material.Body1(u.th, "Security Code with "+words.Quoted(p.Name))
				title.Font.Weight = font.Bold
				return title.Layout(gtx)
			}),
			layout.Rigid(layout.Spacer{Height: unit.Dp(4)}.Layout),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return u.code(gtx, strings.Join(p.Groups[:half], " "))
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return u.code(gtx, strings.Join(p.Groups[half:], " "))
			}),
			layout.Rigid(layout.Spacer{Height: pad}.Layout),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return u.lines(gtx, p.Lines)
			}),
			layout.Rigid(layout.Spacer{Height: pad}.Layout),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(material.Body1(u.th, "Does their code match yours?  ").Layout),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return u.button(gtx, &u.accept, "Yes — accept", true, u.th.ContrastBg)
					}),
					layout.Rigid(layout.Spacer{Width: pad}.Layout),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return u.button(gtx, &u.refuse, "No — refuse", true, dangerBg)
					}),
				)
			}),
		)
	})
}

// code is one half of the Security Code, big and monospaced: it exists to be
// read aloud, digit by digit.
func (u *ui) code(gtx layout.Context, digits string) layout.Dimensions {
	l := material.Label(u.th, unit.Sp(22), digits)
	l.Font.Typeface = "monospace"
	return l.Layout(gtx)
}

// lines lays several lines of explanation out, one under the other.
func (u *ui) lines(gtx layout.Context, lines []string) layout.Dimensions {
	children := make([]layout.FlexChild, 0, len(lines))
	for _, line := range lines {
		children = append(children, layout.Rigid(material.Body2(u.th, line).Layout))
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx, children...)
}

// messageBox is the bottom of the window: what to say, and the button that
// says it. Enter sends and shift+enter starts a line, which is what every
// chat window does.
func (u *ui) messageBox(gtx layout.Context, s gui.Screen) layout.Dimensions {
	if !s.Controls.Send {
		// A dead message box still has to be visible: an input that came and
		// went with the Session would make the window look broken.
		gtx = gtx.Disabled()
	}
	return bar(gtx, barBg, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
			layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
				return u.editor(gtx, &u.input, u.hint(s))
			}),
			layout.Rigid(layout.Spacer{Width: pad}.Layout),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return u.button(gtx, &u.send, "Send", s.Controls.Send, u.th.ContrastBg)
			}),
		)
	})
}

// hint is what the empty message box says, which is where someone with
// nothing to type yet finds out what to do instead.
func (u *ui) hint(s gui.Screen) string {
	switch {
	case s.Prompt != nil:
		return "Answer the Security Code above first"
	case s.Controls.Send:
		return "Type a message — enter sends, shift+enter starts a line"
	case s.Controls.Host:
		return "Host a Session, or paste an Invite above to join one"
	}
	return "Not connected yet"
}

// editor draws one text box, boxed so that it reads as one.
func (u *ui) editor(gtx layout.Context, e *widget.Editor, hint string) layout.Dimensions {
	return widget.Border{
		Color:        mutedBg,
		Width:        unit.Dp(1),
		CornerRadius: unit.Dp(4),
	}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.UniformInset(unit.Dp(6)).Layout(gtx, material.Editor(u.th, e, hint).Layout)
	})
}

// button draws one button. A button that would do nothing is drawn greyed and
// does not take the click: every control dcc has stays on screen, so that
// what it can do is visible, and says for itself when it is not available.
func (u *ui) button(gtx layout.Context, click *widget.Clickable, label string, live bool, bg color.NRGBA) layout.Dimensions {
	b := material.Button(u.th, click, label)
	b.Background = bg
	b.CornerRadius = unit.Dp(4)
	b.Inset = layout.UniformInset(unit.Dp(6))
	if !live {
		b.Background = mutedBg
		b.Color = mutedFg
		gtx = gtx.Disabled()
	}
	return b.Layout(gtx)
}

// bar draws one horizontal band of the window — the top controls, the Invite,
// the prompt, the message box — on its own background.
func bar(gtx layout.Context, bg color.NRGBA, w layout.Widget) layout.Dimensions {
	macro := op.Record(gtx.Ops)
	dims := layout.UniformInset(pad).Layout(gtx, w)
	call := macro.Stop()

	defer clip.Rect{Max: dims.Size}.Push(gtx.Ops).Pop()
	paint.ColorOp{Color: bg}.Add(gtx.Ops)
	paint.PaintOp{}.Add(gtx.Ops)
	call.Add(gtx.Ops)
	return dims
}
