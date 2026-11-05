package connector

import (
	"context"
	"testing"
	"time"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/event"
)

// Getting past the thread check would panic on the empty client and receipt.
func TestHandleMatrixReadReceipt_IgnoresThreadReceipt(t *testing.T) {
	client := &WhatsAppClient{}
	err := client.HandleMatrixReadReceipt(context.Background(), &bridgev2.MatrixReadReceipt{
		ReadUpTo: time.Now(),
		Receipt:  event.ReadReceipt{ThreadID: "$root"},
	})
	if err != nil {
		t.Fatal(err)
	}
}
