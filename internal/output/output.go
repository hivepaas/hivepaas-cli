// Package output is how the CLI answers: tables and messages for people, the
// API's own data as JSON or YAML for scripts. The result goes to stdout and
// everything else to stderr, so a pipe never reads a progress line.
package output

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/term"
	"gopkg.in/yaml.v3"
)

// Formats -o takes.
const (
	Table = "table"
	JSON  = "json"
	YAML  = "yaml"
)

// Printer writes a command's answer.
type Printer struct {
	Out, Err io.Writer
	Format   string
	// colorOut and colorErr say whether stdout and stderr take colors: each is
	// a terminal, and colors are not turned off.
	colorOut, colorErr bool
}

// New is a printer in format, with colors on a terminal unless they are turned
// off.
func New(out, errw io.Writer, format string, noColor bool) (*Printer, error) {
	switch format {
	case "", Table:
		format = Table
	case JSON, YAML:
	default:
		return nil, fmt.Errorf("-o takes table, json or yaml, not %q", format) //nolint:err113
	}
	color := !noColor && os.Getenv("NO_COLOR") == ""
	return &Printer{
		Out: out, Err: errw, Format: format,
		colorOut: color && isTerminal(out), colorErr: color && isTerminal(errw),
	}, nil
}

func isTerminal(w io.Writer) bool {
	file, ok := w.(*os.File)
	return ok && term.IsTerminal(int(file.Fd())) //nolint:gosec // a file descriptor fits an int
}

// Structured says the answer is data for a script: -o json or yaml.
func (p *Printer) Structured() bool { return p.Format != Table }

// Data writes v - the API's data - as JSON or YAML.
func (p *Printer) Data(v any) error {
	data, err := Marshal(v)
	if err != nil {
		return fmt.Errorf("writing the answer: %w", err)
	}
	return p.JSON(data)
}

// Marshal is v as JSON, with <, > and & as they are: the output is read by
// people and scripts, not put in a web page.
func Marshal(v any) ([]byte, error) {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(v); err != nil {
		return nil, err //nolint:wrapcheck
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

// JSON writes a JSON document as it is, its keys in their order: indented, or
// with -o yaml as YAML. A body that is not JSON is written as it came.
func (p *Printer) JSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if !json.Valid(data) {
		_, err := fmt.Fprintf(p.Out, "%s\n", data)
		return err //nolint:wrapcheck
	}
	if p.Format == YAML {
		// JSON is YAML: read it as a node, which keeps the keys' order.
		var node yaml.Node
		if err := yaml.Unmarshal(data, &node); err != nil {
			return fmt.Errorf("writing the answer: %w", err)
		}
		blockStyle(&node)
		var buf bytes.Buffer
		encoder := yaml.NewEncoder(&buf)
		encoder.SetIndent(2) //nolint:mnd
		if err := encoder.Encode(&node); err != nil {
			return fmt.Errorf("writing the answer: %w", err)
		}
		_, err := p.Out.Write(buf.Bytes())
		return err //nolint:wrapcheck
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, data, "", "  "); err != nil {
		return fmt.Errorf("writing the answer: %w", err)
	}
	buf.WriteByte('\n')
	_, err := p.Out.Write(buf.Bytes())
	return err //nolint:wrapcheck
}

// blockStyle makes a document read from JSON plain YAML: its objects and lists
// in block style, its strings quoted only where YAML would read them as
// something else.
func blockStyle(node *yaml.Node) {
	node.Style = 0
	for _, child := range node.Content {
		blockStyle(child)
	}
}

// Table writes rows under a header, in columns.
//
// The columns are as wide as their widest cell as it shows: colors take no room,
// so a dimmed note in a cell does not push the columns after it out of line, as
// it would with text/tabwriter, which counts their escape codes.
func (p *Printer) Table(header []string, rows [][]string) error {
	all := append([][]string{header}, rows...)
	var widths []int
	for _, row := range all {
		for i, cell := range row {
			if i == len(widths) {
				widths = append(widths, 0)
			}
			widths[i] = max(widths[i], visibleWidth(cell))
		}
	}
	var b strings.Builder
	for _, row := range all {
		for i, cell := range row {
			b.WriteString(cell)
			if i < len(row)-1 {
				b.WriteString(strings.Repeat(" ", widths[i]-visibleWidth(cell)+columnGap))
			}
		}
		b.WriteString("\n")
	}
	_, err := io.WriteString(p.Out, b.String())
	return err //nolint:wrapcheck
}

// columnGap is the space between two columns.
const columnGap = 2

// ansiCode is a color code, which takes no room on a terminal.
var ansiCode = regexp.MustCompile("\x1b\\[[0-9;]*m")

func visibleWidth(cell string) int {
	return utf8.RuneCountInString(ansiCode.ReplaceAllString(cell, ""))
}

// Infof is a message for the person running the command, on stderr.
func (p *Printer) Infof(format string, args ...any) {
	fmt.Fprintf(p.Err, format+"\n", args...)
}

// Successf is a message that something worked, in green on a terminal.
func (p *Printer) Successf(format string, args ...any) {
	fmt.Fprintln(p.Err, paint(p.colorErr, "32", fmt.Sprintf(format, args...)))
}

// Warnf is a warning, in yellow on a terminal.
func (p *Printer) Warnf(format string, args ...any) {
	fmt.Fprintln(p.Err, paint(p.colorErr, "33", "Warning: "+fmt.Sprintf(format, args...)))
}

// Errorf is an error, in red on a terminal.
func (p *Printer) Errorf(format string, args ...any) {
	fmt.Fprintln(p.Err, paint(p.colorErr, "31", "Error: "+fmt.Sprintf(format, args...)))
}

// Dim is text of less weight on stdout, such as an id beside a name.
func (p *Printer) Dim(text string) string { return paint(p.colorOut, "2", text) }

// DimErr is text of less weight on stderr.
func (p *Printer) DimErr(text string) string { return paint(p.colorErr, "2", text) }

func paint(color bool, code, text string) string {
	if !color {
		return text
	}
	return "\x1b[" + code + "m" + text + "\x1b[0m"
}

// Ago is how long ago an RFC 3339 time was, as a person says it: 5m ago, 2h
// ago, 3d ago. A time it cannot read is given back as it is.
func Ago(value string, now time.Time) string {
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return value
	}
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24)) //nolint:mnd
	}
}
