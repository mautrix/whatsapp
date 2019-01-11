module maunium.net/go/mautrix-whatsapp

require (
	github.com/Rhymen/go-whatsapp v0.0.0-20181218094654-2ca6af00572c
	github.com/mattn/go-sqlite3 v1.10.0
	github.com/shurcooL/sanitized_anchor_name v0.0.0-20170918181015-86672fcb3f95 // indirect
	github.com/skip2/go-qrcode v0.0.0-20171229120447-cf5f9fa2f0d8
	golang.org/x/sync v0.0.0-20181221193216-37e7f081c4d4 // indirect
	golang.org/x/sys v0.0.0-20181221143128-b4a75ba826a6 // indirect
	gopkg.in/yaml.v2 v2.2.2
	maunium.net/go/mauflag v1.0.0
	maunium.net/go/maulogger/v2 v2.0.0
	maunium.net/go/mautrix v0.1.0-alpha.2
	maunium.net/go/mautrix-appservice v0.1.0-alpha.2
)

replace maunium.net/go/mautrix-appservice => ../mautrix-appservice-go

replace maunium.net/go/mautrix => ../mautrix-go
