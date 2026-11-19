package connector

import (
	"testing"

	"go.mau.fi/util/l10n/l10ntest"
)

func TestL10NCatalog(t *testing.T) {
	l10ntest.CheckCatalog(t, "../../l10n/en.json", L)
}
