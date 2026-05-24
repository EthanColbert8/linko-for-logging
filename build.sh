#!/usr/bin/bash

go build -o linko \
    -ldflags "-X boot.dev/linko/internal/build.GitSHA=$(git rev-parse HEAD) -X boot.dev/linko/internal/build.BuildTime=$(date -u '+%Y-%m-%dT%H:%M:%SZ')"
