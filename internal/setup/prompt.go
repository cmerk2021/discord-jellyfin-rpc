package setup

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"golang.org/x/term"
)

// Prompter asks questions on a terminal.
type Prompter struct {
	in    *bufio.Reader
	out   io.Writer
	fd    int
	isTTY bool
}

// NewPrompter uses /dev/tty when available so it works under `curl | sh`.
func NewPrompter() (*Prompter, func()) {
	if f, err := os.OpenFile("/dev/tty", os.O_RDWR, 0); err == nil {
		return &Prompter{in: bufio.NewReader(f), out: f, fd: int(f.Fd()), isTTY: true}, func() { f.Close() }
	}
	fd := int(os.Stdin.Fd())
	return &Prompter{in: bufio.NewReader(os.Stdin), out: os.Stdout, fd: fd, isTTY: term.IsTerminal(fd)}, func() {}
}

// NewPrompterFrom creates a prompter from arbitrary streams (tests).
func NewPrompterFrom(in io.Reader, out io.Writer) *Prompter {
	return &Prompter{in: bufio.NewReader(in), out: out, fd: -1}
}

const (
	bold   = "\033[1m"
	dim    = "\033[2m"
	green  = "\033[32m"
	yellow = "\033[33m"
	red    = "\033[31m"
	cyan   = "\033[36m"
	reset  = "\033[0m"
)

func (p *Prompter) color(c, s string) string {
	if !p.isTTY || os.Getenv("NO_COLOR") != "" {
		return s
	}
	return c + s + reset
}

func (p *Prompter) Printf(format string, a ...any) { fmt.Fprintf(p.out, format, a...) }
func (p *Prompter) Header(s string)                { p.Printf("\n%s\n", p.color(bold+cyan, "── "+s+" ──")) }
func (p *Prompter) Info(s string)                  { p.Printf("%s\n", p.color(dim, s)) }
func (p *Prompter) OK(s string)                    { p.Printf("%s %s\n", p.color(green, "✔"), s) }
func (p *Prompter) Warn(s string)                  { p.Printf("%s %s\n", p.color(yellow, "!"), s) }
func (p *Prompter) Err(s string)                   { p.Printf("%s %s\n", p.color(red, "✘"), s) }

func (p *Prompter) readLine() (string, error) {
	line, err := p.in.ReadString('\n')
	if err != nil && (err != io.EOF || line == "") {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

// Ask prompts for a string with an optional default.
func (p *Prompter) Ask(q, def string) (string, error) {
	if def != "" {
		p.Printf("%s %s: ", p.color(bold, q), p.color(dim, "["+def+"]"))
	} else {
		p.Printf("%s: ", p.color(bold, q))
	}
	v, err := p.readLine()
	if err != nil {
		return "", err
	}
	if v == "" {
		return def, nil
	}
	return v, nil
}

// AskRequired prompts until a non-empty value is given.
func (p *Prompter) AskRequired(q, def string) (string, error) {
	for {
		v, err := p.Ask(q, def)
		if err != nil {
			return "", err
		}
		if v != "" {
			return v, nil
		}
		p.Warn("A value is required.")
	}
}

// Secret prompts without echo when possible. A non-empty def is kept on empty input.
func (p *Prompter) Secret(q string, hasDefault bool) (string, error) {
	hint := ""
	if hasDefault {
		hint = " " + p.color(dim, "[keep current]")
	}
	p.Printf("%s%s: ", p.color(bold, q), hint)
	if p.isTTY && p.fd >= 0 {
		b, err := term.ReadPassword(p.fd)
		p.Printf("\n")
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(b)), nil
	}
	return p.readLine()
}

// Confirm asks a yes/no question.
func (p *Prompter) Confirm(q string, def bool) (bool, error) {
	d := "y/N"
	if def {
		d = "Y/n"
	}
	for {
		p.Printf("%s %s: ", p.color(bold, q), p.color(dim, "["+d+"]"))
		v, err := p.readLine()
		if err != nil {
			return false, err
		}
		switch strings.ToLower(v) {
		case "":
			return def, nil
		case "y", "yes":
			return true, nil
		case "n", "no":
			return false, nil
		}
		p.Warn("Please answer y or n.")
	}
}

// Choose asks the user to pick one option (1-based display). Returns the index.
func (p *Prompter) Choose(q string, options []string, def int) (int, error) {
	p.Printf("%s\n", p.color(bold, q))
	for i, o := range options {
		marker := " "
		if i == def {
			marker = p.color(green, "›")
		}
		p.Printf(" %s %d) %s\n", marker, i+1, o)
	}
	for {
		v, err := p.Ask("Choice", strconv.Itoa(def+1))
		if err != nil {
			return 0, err
		}
		n, err := strconv.Atoi(v)
		if err == nil && n >= 1 && n <= len(options) {
			return n - 1, nil
		}
		p.Warn(fmt.Sprintf("Enter a number between 1 and %d.", len(options)))
	}
}
