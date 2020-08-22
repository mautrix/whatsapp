module maunium.net/go/mautrix-whatsapp

go 1.14

require (
	github.com/Rhymen/go-whatsapp v0.1.0
	github.com/chai2010/webp v1.1.0
	github.com/gorilla/websocket v1.4.2
	github.com/lib/pq v1.7.0
	github.com/mattn/go-sqlite3 v1.14.0
	github.com/pkg/errors v0.9.1
	github.com/prometheus/client_golang v1.7.0
	github.com/shurcooL/sanitized_anchor_name v1.0.0 // indirect
	github.com/skip2/go-qrcode v0.0.0-20200617195104-da1b6568686e
	golang.org/x/image v0.0.0-20200618115811-c13761719519
	gopkg.in/yaml.v2 v2.3.0
	maunium.net/go/mauflag v1.0.0
	maunium.net/go/maulogger/v2 v2.1.1
	maunium.net/go/mautrix v0.7.2
)

replace github.com/Rhymen/go-whatsapp => github.com/tulir/go-whatsapp v0.3.7
