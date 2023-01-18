#!/bin/sh

CGO_EXTRA_CPPFLAGS=""
CGO_EXTRA_LDFLAGS=""
if [ `uname` = "FreeBSD" ] ; then
    CGO_EXTRA_CPPFLAGS="-I/usr/local/include"
    CGO_EXTRA_LDFLAGS="-L/usr/local/lib"
fi

if [ -n "${CGO_EXTRA_CPPFLAGS}" ] ; then
    if [ -z "${CGO_CPPFLAGS}" ] ; then
        export CGO_CPPFLAGS="${CGO_EXTRA_CPPFLAGS}"
    else
        CGO_CPPFLAGS_OLD="${CGO_CPPFLAGS}"
        export CGO_CPPFLAGS="${CGO_CPPFLAGS_OLD} ${CGO_EXTRA_CPPFLAGS}"
    fi
fi

if [ -n "${CGO_EXTRA_LDFLAGS}" ] ; then
    if [ -z "${CGO_LDFLAGS}" ] ; then
        export CGO_LDFLAGS="${CGO_EXTRA_LDFLAGS}"
    else
        CGO_LDFLAGS_OLD="${CGO_LDFLAGS}"
        export CGO_LDFLAGS="${CGO_LDFLAGS_OLD} ${CGO_EXTRA_LDFLAGS}"
    fi
fi

go build -ldflags "-X main.Tag=$(git describe --exact-match --tags 2>/dev/null) -X main.Commit=$(git rev-parse HEAD) -X 'main.BuildTime=`date '+%b %_d %Y, %H:%M:%S'`'" "$@"
