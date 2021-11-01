## Element fork

The Element fork includes the following changes:
- [Track "active" WhatsApp users and implement blocking when reaching the limit](https://github.com/tulir/mautrix-whatsapp/pull/323)
- [Prevent keepalive from segfaulting if its WS is not initialized](https://github.com/vector-im/go-whatsapp/pull/1)

Some changes that appear here may get upstreamed to https://github.com/mautrix/whatsapp, and will be removed from
the list when they appear in both versions.

Tagged versions will appear as `v{UPSTREAM-VERSION}-mod-{VERSION}`

E.g. The third modification release to 1.0 of the upstream bridge would be `v1.0-mod-3`.

# mautrix-whatsapp
A Matrix-WhatsApp puppeting bridge based on the [Rhymen/go-whatsapp](https://github.com/Rhymen/go-whatsapp)
implementation of the [sigalor/whatsapp-web-reveng](https://github.com/sigalor/whatsapp-web-reveng) project.
### Documentation
All setup and usage instructions are located on
[docs.mau.fi](https://docs.mau.fi/bridges/go/whatsapp/index.html).
Some quick links:

* [Bridge setup](https://docs.mau.fi/bridges/go/whatsapp/setup/index.html)
  (or [with Docker](https://docs.mau.fi/bridges/go/whatsapp/setup/docker.html))
* Basic usage: [Authentication](https://docs.mau.fi/bridges/go/whatsapp/authentication.html)

### Features & Roadmap
[ROADMAP.md](https://github.com/mautrix/whatsapp/blob/master/ROADMAP.md)
contains a general overview of what is supported by the bridge.

## Discussion
Matrix room: [#whatsapp:maunium.net](https://matrix.to/#/#whatsapp:maunium.net)
