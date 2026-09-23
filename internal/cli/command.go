package cli

import (
	"strings"
	"unicode"
)

// kind is what one submitted line turned out to be.
type kind int

const (
	// nothing: an empty line, which does nothing at all.
	nothing kind = iota
	// text: something to say to the other person. arg is the body.
	text
	// invite, connect, disconnect, quit, help: the commands. Only connect
	// carries an arg — the Invite.
	invite
	connect
	disconnect
	quit
	help
	// accept, refuse: the answers to a standing Security Code prompt.
	accept
	refuse
	// unknown: a slash command that is not one. arg is what was typed.
	unknown
)

// command is one submitted line, parsed.
type command struct {
	kind kind
	arg  string
}

// mode is what a bare word — one typed without a leading '/' — means at the
// moment it is typed. A slash always means a command, in every mode.
type mode int

const (
	// modeCommand: there is nothing to talk to, so nothing typed could be a
	// message and bare words are commands.
	modeCommand mode = iota
	// modeVerify: a Security Code prompt stands, and a bare yes or no
	// answers it.
	modeVerify
	// modeChat: Connected, so a bare line is a message — which is what makes
	// chatting frictionless, at the price of needing '/msg' to say something
	// that begins with a slash.
	modeChat
)

// commands maps every command word to its kind. The words are the ones
// docs/mvp.md's CLI section names; 'exit' and '?' are there because a
// terminal program that refuses them is merely annoying.
var commands = map[string]kind{
	"invite":     invite,
	"connect":    connect,
	"disconnect": disconnect,
	"quit":       quit,
	"exit":       quit,
	"help":       help,
	"?":          help,
	"msg":        text,
	"accept":     accept,
	"refuse":     refuse,
}

// answers maps the bare words that resolve a standing Security Code prompt.
var answers = map[string]kind{
	"yes": accept,
	"y":   accept,
	"no":  refuse,
	"n":   refuse,
}

// parse reads one submitted line. It is purely syntactic: whether a command
// can be obeyed is the Model's business, and anything unrecognised comes back
// as text for the Model to place.
func parse(line string, m mode) command {
	body := strings.TrimSpace(line)
	if body == "" {
		return command{kind: nothing}
	}

	// A command is one line. Anything with a newline in it is something
	// someone wrote, not something they typed at the app.
	if !strings.ContainsRune(body, '\n') {
		if rest, slashed := strings.CutPrefix(body, "/"); slashed {
			word, arg := cut(rest)
			if k, known := commands[strings.ToLower(word)]; known {
				return command{kind: k, arg: arg}
			}
			return command{kind: unknown, arg: "/" + word}
		}
		word, arg := cut(body)
		switch m {
		case modeVerify:
			if k, isAnswer := answers[strings.ToLower(word)]; isAnswer && arg == "" {
				return command{kind: k}
			}
		case modeCommand:
			if k, known := commands[strings.ToLower(word)]; known {
				return command{kind: k, arg: arg}
			}
		}
	}
	return command{kind: text, arg: body}
}

// cut splits a line into its first word and whatever follows.
func cut(line string) (word, rest string) {
	i := strings.IndexFunc(line, unicode.IsSpace)
	if i < 0 {
		return line, ""
	}
	return line[:i], strings.TrimSpace(line[i:])
}
