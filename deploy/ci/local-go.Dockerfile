# Local builds (scripts/build-local.ps1): Linux Go toolchain plus what the
# panel-and-endpoint integration test needs. CI uses the GitHub runner instead.
FROM golang:1.26-bookworm
RUN apt-get update && apt-get install -y --no-install-recommends python3 openssl && rm -rf /var/lib/apt/lists/*
