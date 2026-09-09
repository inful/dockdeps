// Package identity parses and normalises OCI image references into
// stable identifiers that the dependency graph uses to deduplicate
// edges.
//
// The grammar we accept is a subset of the OCI distribution spec:
//
//	[REGISTRY[:PORT]/]REPOSITORY[:TAG][@DIGEST]
//
// where:
//
//   - REGISTRY defaults to "docker.io" when the first path segment is
//     not a domain (no dot, no port).
//   - REPOSITORY is one or more slash-separated path segments. A
//     single-segment repository (e.g. "alpine") is normalised to
//     "library/<name>" — Docker's convention for official images.
//   - TAG defaults to "latest" when no tag is given. The default is
//     skipped for digest-only references ("alpine@sha256:..."), for
//     "scratch" (which has no tag), and for parameterised references
//     that contain shell variable substitution.
//   - DIGEST is the @sha256:... form and may co-exist with a tag in
//     theory, but we don't preserve the tag in that case — the
//     digest is the unambiguous identity.
//
// "scratch" is special-cased: it has no registry, no tag, no digest,
// and its ID is the literal "scratch". It represents an empty base
// layer and isn't a real image.
package identity

import (
	"errors"
	"fmt"
	"strings"
)

// Image is the parsed form of an image reference. All fields are
// normalised: Registry defaults to "docker.io" (except for scratch);
// Repository includes any implicit "library/" prefix; Tag is empty
// when the reference is digest-only or unparseable.
type Image struct {
	Registry   string // "docker.io", "ghcr.io", "registry.gitlab.com:443"
	Repository string // "library/alpine", "inful/dockdeps"
	Tag        string // "1.22", "latest", "${VERSION}" — may be empty
	Digest     string // "sha256:abc..." — empty when absent
}

// Parse converts a Docker-style image reference into an Image. It
// returns an error when the input is empty or otherwise unusable.
//
// Parameterised references (those containing ${VAR} or $VAR) are
// accepted; their Tag captures the literal string. Use IsParameterized
// to detect them.
func Parse(ref string) (Image, error) {
	if ref == "" {
		return Image{}, errors.New("identity: empty image reference")
	}

	// Split digest first: digest is always preceded by '@' and lives
	// at the end of the reference. Splitting this way keeps the tag
	// (if any) on the "left" side cleanly.
	tagPart := ref
	var digest string
	if idx := strings.LastIndex(ref, "@"); idx >= 0 {
		tagPart = ref[:idx]
		digest = ref[idx+1:]
		if digest == "" {
			return Image{}, fmt.Errorf("identity: empty digest in %q", ref)
		}
	}

	// Now split registry from repository+tag. A registry is the
	// first segment if it contains a '.' or ':' (domain or
	// host:port). After we've identified and stripped the registry,
	// the first ':' anywhere in the remainder is the tag separator
	// (because the registry's own port colon has been consumed).
	registry := "docker.io"
	rest := tagPart
	if firstSlash := strings.Index(tagPart, "/"); firstSlash >= 0 {
		first := tagPart[:firstSlash]
		if strings.ContainsAny(first, ".:") {
			registry = first
			rest = tagPart[firstSlash+1:]
		}
	}

	// Split tag from repository. Any ':' in `rest` is the tag
	// separator — the only legitimate ':' in a Docker image
	// reference (registry port has already been stripped above).
	repository := rest
	var tag string
	if idx := strings.Index(rest, ":"); idx >= 0 {
		repository = rest[:idx]
		tag = rest[idx+1:]
	}

	// Special-case "scratch": it's its own registry-less entity.
	if registry == "docker.io" && repository == "scratch" {
		return Image{
			Registry:   "",
			Repository: "scratch",
		}, nil
	}

	// Normalise single-segment repositories on docker.io to the
	// "library/" namespace — Docker's convention for official images.
	if registry == "docker.io" && !strings.Contains(repository, "/") {
		repository = "library/" + repository
	}

	// Apply tag defaults. A missing tag becomes "latest" unless the
	// reference is digest-only (no tag on the wire means "no tag")
	// or parameterised (we keep the literal so the caller can
	// resolve it).
	if tag == "" && digest == "" && !isParameterizedString(repository) {
		tag = "latest"
	}

	return Image{
		Registry:   registry,
		Repository: repository,
		Tag:        tag,
		Digest:     digest,
	}, nil
}

// String returns the canonical reference form. It is the inverse of
// Parse for the common cases (registry + repository + tag, or
// registry + repository + digest). Parameterised references are
// preserved verbatim.
func (i Image) String() string {
	if i.Registry == "" && i.Repository == "scratch" {
		return "scratch"
	}
	out := i.Repository
	if i.Registry != "" && i.Registry != "docker.io" {
		out = i.Registry + "/" + out
	}
	if i.Digest != "" {
		return out + "@" + i.Digest
	}
	if i.Tag != "" {
		return out + ":" + i.Tag
	}
	return out
}

// ID returns the stable identifier the dependency graph uses. Two
// references with the same ID refer to the same image even if their
// String() forms differ.
//
// The ID always includes the registry (even when it's docker.io)
// so that explicit vs implicit registry never produces different IDs
// for what should be the same image. The only exception is
// "scratch", whose ID is the literal "scratch".
//
// For digest-pinned references, the ID includes the digest and not
// the tag (because the digest is the unambiguous identity). For
// tag-only references, the ID includes the tag and not a digest.
func (i Image) ID() string {
	if i.Registry == "" && i.Repository == "scratch" {
		return "scratch"
	}
	out := i.Repository
	if i.Registry != "" {
		out = i.Registry + "/" + out
	}
	if i.Digest != "" {
		return out + "@" + i.Digest
	}
	if i.Tag != "" {
		return out + ":" + i.Tag
	}
	return out
}

// IsParameterized reports whether the image reference contains
// unresolved shell or ARG substitution. Parameterised references
// can't be resolved statically; the scanner flags them so the graph
// can mark the edge as "incomplete" without rejecting it.
func (i Image) IsParameterized() bool {
	return isParameterizedString(i.Tag) || isParameterizedString(i.Repository)
}

// isParameterizedString looks for shell/ARG substitution markers:
// ${VAR}, $VAR, and bash-style ${VAR:-default} forms.
func isParameterizedString(s string) bool {
	if strings.Contains(s, "${") {
		return true
	}
	// Bare $VAR only counts when followed by an identifier character
	// (letter, digit, underscore). A trailing "$" alone isn't a
	// substitution.
	for i := 0; i < len(s); i++ {
		if s[i] == '$' && i+1 < len(s) {
			c := s[i+1]
			if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '_' {
				return true
			}
		}
	}
	return false
}
