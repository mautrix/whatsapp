module maunium.net/go/mautrix-whatsapp

go 1.14

require (
	github.com/Rhymen/go-whatsapp v0.1.0
	github.com/chai2010/webp v1.1.0
	github.com/gorilla/websocket v1.4.2
	github.com/lib/pq v1.5.2
	github.com/mattn/go-sqlite3 v2.0.3+incompatible
	github.com/pkg/errors v0.9.1
	github.com/shurcooL/sanitized_anchor_name v1.0.0 // indirect
	github.com/skip2/go-qrcode v0.0.0-20191027152451-9434209cb086
	golang.org/x/image v0.0.0-20200430140353-33d19683fad8
	gopkg.in/yaml.v2 v2.2.8
	maunium.net/go/mauflag v1.0.0
	maunium.net/go/maulogger/v2 v2.1.1
	maunium.net/go/mautrix v0.4.7
)

replace github.com/Rhymen/go-whatsapp => github.com/tulir/go-whatsapp v0.2.8
