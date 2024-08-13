package connector

import (
	"context"
	"fmt"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"

	"maunium.net/go/mautrix-whatsapp/pkg/waid"
)

func (wa *WhatsAppConnector) GetLoginFlows() []bridgev2.LoginFlow {
	return []bridgev2.LoginFlow{
		{
			Name:        "QR",
			Description: "Scan a QR code to pair the bridge to your WhatsApp account",
			ID:          "qr",
		},
		//TODO
		/*		{
				Name:        "Pairing code",
				Description: "Input your phone number to get a pairing code, to pair the bridge to your WhatsApp account",
				ID:          "pairing-code",
			},*/
	}
}

func (wa *WhatsAppConnector) CreateLogin(ctx context.Context, user *bridgev2.User, flowID string) (bridgev2.LoginProcess, error) {
	//TODO: ADD PAIRING CODE HERE
	if flowID != "qr" {
		return nil, fmt.Errorf("invalid login flow ID")
	}
	return &QRLogin{User: user, Main: wa}, nil
}

type QRLogin struct {
	User       *bridgev2.User
	Main       *WhatsAppConnector
	Client     *whatsmeow.Client
	cancelChan context.CancelFunc
	QRChan     <-chan whatsmeow.QRChannelItem

	AccData *store.Device
}

var _ bridgev2.LoginProcessDisplayAndWait = (*QRLogin)(nil)

func (qr *QRLogin) Cancel() {
	qr.cancelChan()
	go func() {
		for range qr.QRChan {
		}
	}()
}

const (
	LoginStepQR       = "fi.mau.whatsapp.login.qr"
	LoginStepComplete = "fi.mau.whatsapp.login.complete"
)

func (qr *QRLogin) Start(ctx context.Context) (*bridgev2.LoginStep, error) {
	log := qr.Main.Bridge.Log.With().
		Str("action", "login").
		Stringer("user_id", qr.User.MXID).
		Logger()
	qrCtx, cancel := context.WithCancel(log.WithContext(context.Background()))
	qr.cancelChan = cancel

	device := qr.Main.DeviceStore.NewDevice()

	wa := &WhatsAppClient{
		Main:   qr.Main,
		Device: device,
	}

	wa.MakeNewClient()

	qr.Client = wa.Client

	var err error
	qr.QRChan, err = qr.Client.GetQRChannel(qrCtx)
	if err != nil {
		return nil, err
	}

	var resp whatsmeow.QRChannelItem

	select {
	case resp = <-qr.QRChan:
		if resp.Error != nil {
			return nil, resp.Error
		} else if resp.Event != "code" {
			return nil, fmt.Errorf("invalid QR channel event: %s", resp.Event)
		}
	case <-ctx.Done():
		cancel()
		return nil, ctx.Err()
	}

	return &bridgev2.LoginStep{
		Type:         bridgev2.LoginStepTypeDisplayAndWait,
		StepID:       LoginStepQR,
		Instructions: "Scan the QR code on the WhatsApp mobile app to log in",
		DisplayAndWaitParams: &bridgev2.LoginDisplayAndWaitParams{
			Type: bridgev2.LoginDisplayTypeQR,
			Data: resp.Code,
		},
	}, nil
}

func (qr *QRLogin) Wait(ctx context.Context) (*bridgev2.LoginStep, error) {
	if qr.QRChan == nil {
		return nil, fmt.Errorf("login not started")
	}

	defer qr.cancelChan()

	select {
	case resp := <-qr.QRChan:
		if resp.Event != "success" {
			return nil, fmt.Errorf("did not pair properly, err: %s", resp.Event)
		}
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	newLoginID := waid.MakeWAUserLoginID(qr.Client.Store.ID)

	ul, err := qr.User.NewLogin(ctx, &database.UserLogin{
		ID:         newLoginID,
		RemoteName: qr.Client.Store.PushName,
		Metadata: &UserLoginMetadata{
			WADeviceID: qr.Client.Store.ID.Device,
		},
	}, &bridgev2.NewLoginParams{
		DeleteOnConflict: true,
	})

	if err != nil {
		return nil, fmt.Errorf("failed to create user login: %w", err)
	}

	err = qr.Main.LoadUserLogin(context.Background(), ul)

	if err != nil {
		return nil, fmt.Errorf("failed to connect after login: %w", err)
	}

	return &bridgev2.LoginStep{
		Type:         bridgev2.LoginStepTypeComplete,
		StepID:       LoginStepComplete,
		Instructions: fmt.Sprintf("Successfully logged in as %s / %s", qr.Client.Store.PushName, newLoginID),
		CompleteParams: &bridgev2.LoginCompleteParams{
			UserLoginID: ul.ID,
			UserLogin:   ul,
		},
	}, nil
}
