package ui

import (
	"fmt"
	"io"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Status represents the outcome of processing a single .qvw file.
type Status int

const (
	StatusOK Status = iota
	StatusWarn
	StatusErr
)

// Result holds all information about a single file processing outcome.
type Result struct {
	Status  Status
	SrcPath string
	OutDir  string   // output folder path, no trailing slash
	Files   []string // artifact filenames written, in canonical order
	Message string
}

var (
	okStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	warnStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	errStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	dimStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
)

// Printer writes formatted output to w.
type Printer struct {
	w      io.Writer
	tty    bool
	dryRun bool

	okCount      int
	warnCount    int
	errCount     int
	spinnerWidth int
}

// NewPrinter creates a Printer. tty enables color/spinner; dryRun appends [dry run].
func NewPrinter(w io.Writer, tty bool, dryRun bool) *Printer {
	return &Printer{w: w, tty: tty, dryRun: dryRun}
}

// FileResult prints a single per-file result line and updates internal counters.
func (p *Printer) FileResult(r Result) {
	switch r.Status {
	case StatusOK:
		p.okCount++
		p.printOK(r)
	case StatusWarn:
		p.warnCount++
		p.printWarn(r)
	case StatusErr:
		p.errCount++
		p.printErr(r)
	}
}

func (p *Printer) printOK(r Result) {
	sym := p.colorize("✓", okStyle)
	fileList := strings.Join(r.Files, ", ")
	line := fmt.Sprintf("  %s  %s → %s/  (%s)", sym, r.SrcPath, r.OutDir, fileList)
	if p.dryRun {
		line += "  " + p.colorize("[dry run]", dimStyle)
	}
	_, _ = fmt.Fprintln(p.w, line)
}

func (p *Printer) printWarn(r Result) {
	sym := p.colorize("⚠", warnStyle)
	line := fmt.Sprintf("  %s  %s  %s", sym, r.SrcPath, r.Message)
	if p.dryRun {
		line += "  " + p.colorize("[dry run]", dimStyle)
	}
	_, _ = fmt.Fprintln(p.w, line)
}

func (p *Printer) printErr(r Result) {
	sym := p.colorize("✗", errStyle)
	line := fmt.Sprintf("  %s  %s  %s", sym, r.SrcPath, r.Message)
	if p.dryRun {
		line += "  " + p.colorize("[dry run]", dimStyle)
	}
	_, _ = fmt.Fprintln(p.w, line)
}

// Summary prints the final summary line.
func (p *Printer) Summary() {
	total := p.okCount + p.warnCount + p.errCount

	ok := p.colorize(fmt.Sprintf("✓ %d", p.okCount), okStyle)
	warn := p.colorize(fmt.Sprintf("⚠ %d", p.warnCount), warnStyle)
	er := p.colorize(fmt.Sprintf("✗ %d", p.errCount), errStyle)
	counts := fmt.Sprintf("%s  %s  %s", ok, warn, er)

	var line string
	if p.dryRun {
		line = fmt.Sprintf("  Dry run — %d files would be extracted  %s", total, counts)
	} else {
		line = fmt.Sprintf("  Extracted %d apps   %s", p.okCount, counts)
	}
	_, _ = fmt.Fprintln(p.w, line)
}

// UpdateSpinner prints a spinner line (non-TTY: no-op; TTY: overwrite current line).
func (p *Printer) UpdateSpinner(current, total int) {
	if !p.tty {
		return
	}
	text := fmt.Sprintf("  Extracting... %d/%d", current, total)
	p.spinnerWidth = len(text)
	_, _ = fmt.Fprintf(p.w, "\r%s", text)
}

// ClearSpinner clears the spinner line (TTY only).
func (p *Printer) ClearSpinner() {
	if !p.tty {
		return
	}
	_, _ = fmt.Fprintf(p.w, "\r%s\r", strings.Repeat(" ", p.spinnerWidth))
}

// colorize applies the style only in TTY mode.
func (p *Printer) colorize(s string, style lipgloss.Style) string {
	if !p.tty {
		return s
	}
	return style.Render(s)
}
