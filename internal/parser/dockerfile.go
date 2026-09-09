// Package parser extracts structured data from Dockerfiles.
//
// The parser handles the subset of the Dockerfile grammar that
// affects the dependency graph: FROM lines and their flags
// (--platform, --as, --build-arg), stage aliases (AS keyword), and
// line continuations. Every other instruction (RUN, COPY, ENV, etc.)
// is ignored — the graph doesn't need them.
//
// Parameterised FROM statements (those whose image reference
// contains an ARG or shell variable) are preserved verbatim with a
// Parameterized flag set. The graph marks the corresponding edge as
// "incomplete" but doesn't reject it: the runtime build may resolve
// the parameter in ways the static parser cannot predict.
package parser

import (
	"bufio"
	"fmt"
	"strings"

	"github.com/inful/dockdeps/internal/identity"
)

// FromStatement is the parsed form of a single FROM line.
type FromStatement struct {
	// Image is the parsed image reference. For parameterised
	// references, Tag captures the literal substitution marker.
	Image identity.Image

	// Stage is the stage name from the AS clause (or --as= flag).
	// Empty when the FROM is anonymous.
	Stage string

	// Platform is the value of --platform=. Empty when absent.
	Platform string

	// Line is the 1-indexed line number in the source where the
	// FROM begins. Useful for location in error messages and the
	// dependency graph's edge metadata.
	Line int

	// Parameterized reports whether the image reference contains
	// shell or ARG substitution that the static parser cannot
	// resolve.
	Parameterized bool

	// RawImage is the unparsed image reference string from the
	// FROM line, useful for diagnostics and round-tripping.
	RawImage string
}

// Parse reads a Dockerfile and returns every FROM statement in
// source order.
//
// The input may contain line continuations (backslash-newline),
// comments (# at start of line, ignoring whitespace), and arbitrary
// whitespace between tokens. Errors are returned only for malformed
// FROM lines (e.g. FROM with no image); missing or empty input
// returns an empty slice with no error.
func Parse(src string) ([]FromStatement, error) {
	var out []FromStatement
	scanner := bufio.NewScanner(strings.NewReader(src))
	// Allow long lines — Dockerfiles can have long RUN commands.
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	lineNum := 0
	// continuationBuffer accumulates a multi-line FROM whose
	// tokens span physical lines via backslash continuations.
	var continuationBuffer strings.Builder
	var continuationStartLine int
	flushContinuation := func() error {
		if continuationBuffer.Len() == 0 {
			return nil
		}
		stmt, err := parseFromLine(continuationBuffer.String(), continuationStartLine)
		if err != nil {
			return fmt.Errorf("line %d: %w", continuationStartLine, err)
		}
		out = append(out, stmt)
		continuationBuffer.Reset()
		return nil
	}

	for scanner.Scan() {
		lineNum++
		line := scanner.Text()
		// Strip trailing whitespace and the line-continuation
		// backslash, joining physical lines into a logical line.
		trimmed := strings.TrimRight(line, " \t\r")

		if continuationBuffer.Len() > 0 {
			// We're accumulating a logical FROM line that
			// spans physical lines.
			continuationBuffer.WriteByte(' ')
			continuationBuffer.WriteString(strings.TrimSpace(strings.TrimSuffix(trimmed, "\\")))
			if strings.HasSuffix(trimmed, "\\") {
				continue
			}
			if err := flushContinuation(); err != nil {
				return nil, err
			}
			continue
		}

		// Skip blank lines and comments at top level.
		stripped := strings.TrimSpace(trimmed)
		if stripped == "" || strings.HasPrefix(stripped, "#") {
			continue
		}

		// We only care about FROM instructions at the start of a
		// logical line. Other instructions are ignored.
		fields := strings.Fields(stripped)
		if len(fields) == 0 {
			continue
		}
		if !strings.EqualFold(fields[0], "FROM") {
			continue
		}

		// If the line ends with a backslash, this is a
		// continuation — accumulate.
		if strings.HasSuffix(trimmed, "\\") {
			continuationStartLine = lineNum
			// First physical line of the logical FROM, with the
			// trailing backslash stripped.
			continuationBuffer.WriteString(strings.TrimSuffix(trimmed, "\\"))
			continue
		}

		// Single-line FROM.
		stmt, err := parseFromLine(stripped, lineNum)
		if err != nil {
			return nil, err
		}
		out = append(out, stmt)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if err := flushContinuation(); err != nil {
		return nil, err
	}
	return out, nil
}

// parseFromLine parses one logical FROM line (which may have been
// joined from multiple physical lines). The first token is "FROM";
// flags (--platform=, --as=, --build-arg=) come next; the image
// reference is the first non-flag token; an optional "AS <name>"
// trailing clause may follow.
//
// The image token is preserved verbatim in RawImage so callers can
// round-trip or report diagnostics even if parsing fails downstream.
func parseFromLine(line string, lineNum int) (FromStatement, error) {
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return FromStatement{}, fmt.Errorf("FROM with no image: %q", line)
	}
	if !strings.EqualFold(fields[0], "FROM") {
		return FromStatement{}, fmt.Errorf("not a FROM line: %q", line)
	}

	stmt := FromStatement{Line: lineNum}

	// Walk tokens after FROM, skipping flag tokens until we find the
	// image reference.
	i := 1
	for ; i < len(fields); i++ {
		tok := fields[i]
		if !strings.HasPrefix(tok, "--") {
			break // first non-flag token is the image
		}
		switch {
		case strings.HasPrefix(tok, "--platform="):
			stmt.Platform = strings.TrimPrefix(tok, "--platform=")
		case strings.HasPrefix(tok, "--as="):
			stmt.Stage = strings.TrimPrefix(tok, "--as=")
		case strings.HasPrefix(tok, "--build-arg="):
			// --build-arg=<argname>=<value> is metadata for
			// the FROM, not a flag we need to interpret. Skip.
		default:
			// Unknown flag — surface as an error so we don't
			// silently misinterpret the rest of the line.
			return FromStatement{}, fmt.Errorf("unsupported FROM flag: %q", tok)
		}
	}

	if i >= len(fields) {
		return FromStatement{}, fmt.Errorf("FROM has no image after flags: %q", line)
	}

	stmt.RawImage = fields[i]
	img, err := identity.Parse(stmt.RawImage)
	if err != nil {
		return FromStatement{}, fmt.Errorf("FROM image %q: %w", stmt.RawImage, err)
	}
	stmt.Image = img
	stmt.Parameterized = img.IsParameterized()

	// Trailing "AS <name>" (or no AS, in which case Stage stays
	// empty).
	if i+1 < len(fields) {
		if strings.EqualFold(fields[i+1], "AS") {
			if i+2 >= len(fields) {
				return FromStatement{}, fmt.Errorf("FROM ... AS with no name: %q", line)
			}
			stmt.Stage = fields[i+2]
		}
		// Anything else after the image is treated as unknown.
		// Real Dockerfiles never have trailing content after the
		// image (other than AS), so flag it.
		if !strings.EqualFold(fields[i+1], "AS") {
			return FromStatement{}, fmt.Errorf("unexpected token after FROM image: %q", fields[i+1])
		}
	}

	return stmt, nil
}
