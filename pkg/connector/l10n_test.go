package connector

import (
	"testing"

	"maunium.net/go/mautrix/bridgev2/l10n/l10ntest"
)

func TestL10NCatalog(t *testing.T) {
	l10ntest.CheckCatalog(t, "../../l10n/en.json", L.Name())
}
