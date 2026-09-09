package parser_test

import (
	"strings"
	"testing"

	"github.com/inful/dockdeps/internal/identity"
	"github.com/inful/dockdeps/internal/parser"
)

func TestParse_Simple(t *testing.T) {
	got, err := parser.Parse("FROM alpine:3.19\n")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	want := parser.FromStatement{
		Image:    mustParse(t, "alpine:3.19"),
		Line:     1,
		RawImage: "alpine:3.19",
	}
	if !sameFrom(got[0], want) {
		t.Errorf("got %+v, want %+v", got[0], want)
	}
}

func TestParse_MultiStage(t *testing.T) {
	src := `FROM golang:1.22 AS builder
WORKDIR /app
COPY . .
RUN go build -o /out/app

FROM alpine:3.19
COPY --from=builder /out/app /app
`
	got, err := parser.Parse(src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0].Stage != "builder" {
		t.Errorf("got[0].Stage = %q, want builder", got[0].Stage)
	}
	if got[1].Stage != "" {
		t.Errorf("got[1].Stage = %q, want empty (no AS clause)", got[1].Stage)
	}
	if got[0].Line != 1 || got[1].Line != 6 {
		t.Errorf("line numbers = (%d, %d), want (1, 6)", got[0].Line, got[1].Line)
	}
}

func TestParse_PlatformFlag(t *testing.T) {
	src := "FROM --platform=linux/amd64 alpine:3.19\n"
	got, err := parser.Parse(src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got[0].Platform != "linux/amd64" {
		t.Errorf("Platform = %q, want linux/amd64", got[0].Platform)
	}
	if got[0].Image.ID() != "docker.io/library/alpine:3.19" {
		t.Errorf("Image = %+v", got[0].Image)
	}
}

func TestParse_ASFlag(t *testing.T) {
	// Docker also accepts --as=<name> in addition to the AS keyword.
	src := "FROM --as=builder golang:1.22\n"
	got, err := parser.Parse(src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got[0].Stage != "builder" {
		t.Errorf("Stage = %q, want builder", got[0].Stage)
	}
}

func TestParse_Parameterized(t *testing.T) {
	src := `ARG VERSION=1.22
FROM golang:${VERSION}
`
	got, err := parser.Parse(src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !got[0].Parameterized {
		t.Error("expected Parameterized=true")
	}
	if !got[0].Image.IsParameterized() {
		t.Error("expected Image.IsParameterized=true")
	}
}

func TestParse_BuildArgFlag(t *testing.T) {
	// --build-arg is rarely used on FROM but the grammar allows it.
	src := "FROM --build-arg=VERSION=1.22 golang:1.22\n"
	got, err := parser.Parse(src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got[0].Image.ID() != "docker.io/library/golang:1.22" {
		t.Errorf("Image = %+v", got[0].Image)
	}
}

func TestParse_Scratch(t *testing.T) {
	src := "FROM scratch\n"
	got, err := parser.Parse(src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got[0].Image.ID() != "scratch" {
		t.Errorf("scratch ID = %q", got[0].Image.ID())
	}
}

func TestParse_Comments(t *testing.T) {
	src := `# Top-level comment
# Another comment

FROM golang:1.22
# inline comment after FROM

FROM alpine:3.19
`
	got, err := parser.Parse(src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0].Line != 4 || got[1].Line != 7 {
		t.Errorf("line numbers = (%d, %d), want (4, 7)", got[0].Line, got[1].Line)
	}
}

func TestParse_LineContinuation(t *testing.T) {
	// Dockerfile allows a line continuation with backslash.
	src := `FROM golang:1.22 \
    AS builder
`
	got, err := parser.Parse(src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	if got[0].Stage != "builder" {
		t.Errorf("Stage = %q, want builder", got[0].Stage)
	}
}

func TestParse_NoFROM(t *testing.T) {
	src := `# A Dockerfile without any FROM
RUN echo "no base image"
`
	got, err := parser.Parse(src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("len = %d, want 0", len(got))
	}
}

func TestParse_InvalidFROM(t *testing.T) {
	// FROM with no image at all is malformed.
	src := "FROM\n"
	_, err := parser.Parse(src)
	if err == nil {
		t.Fatal("expected error for FROM without image")
	}
	if !strings.Contains(err.Error(), "FROM") {
		t.Errorf("error should mention FROM: %v", err)
	}
}

// TestParse_DigestReference verifies that FROM image references
// using @sha256:... (digest pinning) parse cleanly.
func TestParse_DigestReference(t *testing.T) {
	src := "FROM alpine@sha256:abcd1234\n"
	got, err := parser.Parse(src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got[0].Image.Digest != "sha256:abcd1234" {
		t.Errorf("Digest = %q, want sha256:abcd1234", got[0].Image.Digest)
	}
	if got[0].Image.Tag != "" {
		t.Errorf("Tag = %q, want empty for digest-pinned reference", got[0].Image.Tag)
	}
}

// TestParse_RealisticExamples exercises a handful of real-world
// Dockerfile patterns that combine multiple features.
func TestParse_RealisticExamples(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want int // expected number of FROM statements
	}{
		{
			name: "go-builder-then-alpine",
			src: `FROM golang:1.22 AS builder
WORKDIR /src
COPY . .
RUN go build -o /out/app

FROM alpine:3.19
RUN apk add --no-cache ca-certificates
COPY --from=builder /out/app /app
CMD ["/app"]
`,
			want: 2,
		},
		{
			name: "scratch-with-builder",
			src: `FROM golang:1.22 AS builder
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -o /out/app

FROM scratch
COPY --from=builder /out/app /app
`,
			want: 2,
		},
		{
			name: "platform-specific-multi-arch",
			src: `FROM --platform=$BUILDPLATFORM golang:1.22 AS build
ARG TARGETARCH
WORKDIR /src
COPY . .
RUN GOARCH=$TARGETARCH go build -o /out/app

FROM --platform=$TARGETPLATFORM gcr.io/distroless/static-debian12
COPY --from=build /out/app /app
`,
			want: 2,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parser.Parse(tc.src)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if len(got) != tc.want {
				t.Errorf("len = %d, want %d", len(got), tc.want)
			}
		})
	}
}

// mustParse is a small helper to make test setup less noisy.
func mustParse(t *testing.T, ref string) identity.Image {
	t.Helper()
	img, err := identity.Parse(ref)
	if err != nil {
		t.Fatalf("identity.Parse(%q): %v", ref, err)
	}
	return img
}

// sameFrom compares two FromStatements for structural equality on
// the fields the tests care about.
func sameFrom(a, b parser.FromStatement) bool {
	return a.Image.ID() == b.Image.ID() &&
		a.Stage == b.Stage &&
		a.Platform == b.Platform &&
		a.Line == b.Line &&
		a.Parameterized == b.Parameterized &&
		a.RawImage == b.RawImage
}
