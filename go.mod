module go.mau.fi/mautrix-whatsapp

go 1.22.0

toolchain go1.23.3

require (
	github.com/gorilla/mux v1.8.0
	github.com/gorilla/websocket v1.5.0
	github.com/lib/pq v1.10.9
	github.com/rs/zerolog v1.33.0
	go.mau.fi/util v0.8.2
	go.mau.fi/webp v0.1.0
	go.mau.fi/whatsmeow v0.0.0-20241202173457-b2dd543e5721
	golang.org/x/exp v0.0.0-20241108190413-2d47ceb2692f
	golang.org/x/image v0.22.0
	golang.org/x/net v0.31.0
	golang.org/x/sync v0.9.0
	google.golang.org/protobuf v1.35.2
	gopkg.in/yaml.v3 v3.0.1
	maunium.net/go/mautrix v0.22.1-0.20241202131110-166ba04aae02
)

require (
	filippo.io/edwards25519 v1.1.0 // indirect
	github.com/coreos/go-systemd/v22 v22.5.0 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/kr/pretty v0.3.1 // indirect
	github.com/mattn/go-colorable v0.1.13 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/mattn/go-sqlite3 v1.14.24 // indirect
	github.com/petermattis/goid v0.0.0-20241025130422-66cb2e6d7274 // indirect
	github.com/rogpeppe/go-internal v1.10.0 // indirect
	github.com/rs/xid v1.6.0 // indirect
	github.com/skip2/go-qrcode v0.0.0-20200617195104-da1b6568686e // indirect
	github.com/tidwall/gjson v1.18.0 // indirect
	github.com/tidwall/match v1.1.1 // indirect
	github.com/tidwall/pretty v1.2.1 // indirect
	github.com/tidwall/sjson v1.2.5 // indirect
	github.com/yuin/goldmark v1.7.8 // indirect
	go.mau.fi/libsignal v0.1.1 // indirect
	go.mau.fi/zeroconfig v0.1.3 // indirect
	golang.org/x/crypto v0.29.0 // indirect
	golang.org/x/sys v0.27.0 // indirect
	golang.org/x/text v0.20.0 // indirect
	gopkg.in/check.v1 v1.0.0-20201130134442-10cb98267c6c // indirect
	gopkg.in/natefinch/lumberjack.v2 v2.2.1 // indirect
	maunium.net/go/mauflag v1.0.0 // indirect
)

replace maunium.net/go/mautrix => ./golib
