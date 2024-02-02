#!/bin/sh
if [[ $DBG -eq 1 ]]; then
    GO_GCFLAGS='all=-N -l'
fi
go build -gcflags="$GO_GCFLAGS" -ldflags="-X main.Tag=$(git describe --exact-match --tags 2>/dev/null) -X main.Commit=$(git rev-parse HEAD) -X 'main.BuildTime=`date '+%b %_d %Y, %H:%M:%S'`'" "$@"
