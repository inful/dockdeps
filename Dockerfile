# syntax=docker/dockerfile:1.7
#
# Production image for dockdeps. Built by goreleaser's dockers_v2
# step from the binary already compiled into the build context
# (builddir/linux/amd64/dockdeps or builddir/linux/arm64/dockdeps
# depending on TARGETPLATFORM). No Go toolchain needed inside the
# image.
#
# Base: gcr.io/distroless/static:nonroot — minimal static-linked
# base with ca-certificates (required for TLS to GitHub / GitLab /
# Gitea / Forgejo) and uid 65532.

FROM gcr.io/distroless/static:nonroot

# Copy the pre-built binary that goreleaser dropped into the
# build context under builddir/<platform>/. TARGETPLATFORM is
# set automatically by buildx (linux/amd64 or linux/arm64).
COPY --chown=nonroot:nonroot builddir/$TARGETPLATFORM/dockdeps /usr/local/bin/dockdeps

# Distroless has no shell, so CMD must be the JSON form.
ENTRYPOINT ["/usr/local/bin/dockdeps"]
