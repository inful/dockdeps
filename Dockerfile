# syntax=docker/dockerfile:1.7
#
# Production image for dockdeps. Built by goreleaser's dockers_v2
# step from the binary already compiled into the build context.
# Goreleaser lays out the per-platform binaries at
#   <goos>/<arch>/<binary>
# directly at the root of the build context (no extra prefix),
# so $TARGETPLATFORM/dockdeps is the right COPY source. No Go
# toolchain needed inside the image.
#
# Base: gcr.io/distroless/static:nonroot — minimal static-linked
# base with ca-certificates (required for TLS to GitHub / GitLab /
# Gitea / Forgejo) and uid 65532.

FROM gcr.io/distroless/static:nonroot

# TARGETPLATFORM is auto-populated by buildx once any ARG is
# declared, giving "linux/amd64" or "linux/arm64". Without an ARG
# declaration the variable is empty and COPY fails.
ARG TARGETPLATFORM

# Copy the pre-built binary for the target platform.
COPY --chown=nonroot:nonroot ${TARGETPLATFORM}/dockdeps /usr/local/bin/dockdeps

# Distroless has no shell, so CMD must be the JSON form.
ENTRYPOINT ["/usr/local/bin/dockdeps"]
