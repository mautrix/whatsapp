package connector

import (
	"context"
	"fmt"
	"regexp"

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
		{
			Name:        "Pairing code",
			Description: "Input your phone number to get a pairing code, to pair the bridge to your WhatsApp account",
			ID:          "pairing-code",
		},
	}
}

func (wa *WhatsAppConnector) CreateLogin(_ context.Context, user *bridgev2.User, flowID string) (bridgev2.LoginProcess, error) {

	if flowID == "qr" {
		return &QRLogin{User: user, Main: wa}, nil
	} else if flowID == "pairing-code" {
		return &PairingCodeLogin{User: user, Main: wa}, nil
	} else {
		return nil, fmt.Errorf("invalid login flow ID")
	}
}

func makeQRChan(connector *WhatsAppConnector, user *bridgev2.User) (client *whatsmeow.Client, qrChan <-chan whatsmeow.QRChannelItem, cancelFunc context.CancelFunc, err error) {
	log := connector.Bridge.Log.With().
		Str("action", "login").
		Stringer("user_id", user.MXID).
		Logger()
	qrCtx, cancel := context.WithCancel(log.WithContext(context.Background()))
	cancelFunc = cancel

	device := connector.DeviceStore.NewDevice()

	wa := &WhatsAppClient{
		Main:   connector,
		Device: device,
	}

	wa.MakeNewClient(log)

	client = wa.Client

	qrChan, err = client.GetQRChannel(qrCtx)
	if err != nil {
		return nil, nil, nil, err
	}

	err = client.Connect()
	if err != nil {
		return nil, nil, nil, err
	}
	return
}

func makeUserLogin(ctx context.Context, user *bridgev2.User, client *whatsmeow.Client) (ul *bridgev2.UserLogin, err error) {
	newLoginID := waid.MakeUserLoginID(client.Store.ID)
	ul, err = user.NewLogin(ctx, &database.UserLogin{
		ID:         newLoginID,
		RemoteName: client.Store.PushName,
		Metadata: &UserLoginMetadata{
			WADeviceID: client.Store.ID.Device,
		},
	}, &bridgev2.NewLoginParams{
		DeleteOnConflict: true,
	})
	return
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
	qr.Client.Disconnect()
}

const (
	LoginStepQR               = "fi.mau.whatsapp.login.qr"
	LoginStepComplete         = "fi.mau.whatsapp.login.complete"
	LoginStepPairingCodeInput = "fi.mau.whatsapp.login.pairing.code"
	LoginStepPairingCode      = "fi.mau.whatsapp.login.pairing.code"
)

func (qr *QRLogin) Start(ctx context.Context) (*bridgev2.LoginStep, error) {
	client, qrChan, cancelFunc, err := makeQRChan(qr.Main, qr.User)
	if err != nil {
		return nil, err
	}
	qr.QRChan = qrChan
	qr.cancelChan = cancelFunc
	qr.Client = client
	var resp whatsmeow.QRChannelItem

	select {
	case resp = <-qr.QRChan:
		if resp.Error != nil {
			return nil, resp.Error
		} else if resp.Event != "code" {
			return nil, fmt.Errorf("invalid QR channel event: %s", resp.Event)
		}
	case <-ctx.Done():
		qr.Cancel()
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

	select {
	case resp := <-qr.QRChan:
		if resp.Event == "code" {
			return &bridgev2.LoginStep{
				Type:         bridgev2.LoginStepTypeDisplayAndWait,
				StepID:       LoginStepQR,
				Instructions: "Scan the QR code on the WhatsApp mobile app to log in",
				DisplayAndWaitParams: &bridgev2.LoginDisplayAndWaitParams{
					Type: bridgev2.LoginDisplayTypeQR,
					Data: resp.Code,
				},
			}, nil
		} else if resp.Event != "success" {
			qr.Cancel()
			return nil, fmt.Errorf("did not pair properly, err: %s", resp.Event)
		}
	case <-ctx.Done():
		qr.Cancel()
		return nil, ctx.Err()
	}

	defer qr.Cancel()

	ul, err := makeUserLogin(ctx, qr.User, qr.Client)
	if err != nil {
		return nil, fmt.Errorf("failed to create user login: %w", err)
	}

	err = ul.Client.Connect(ul.Log.WithContext(context.Background()))

	if err != nil {
		return nil, fmt.Errorf("failed to connect after login: %w", err)
	}

	return &bridgev2.LoginStep{
		Type:         bridgev2.LoginStepTypeComplete,
		StepID:       LoginStepComplete,
		Instructions: fmt.Sprintf("Successfully logged in as %s / %s", ul.RemoteName, ul.ID),
		CompleteParams: &bridgev2.LoginCompleteParams{
			UserLoginID: ul.ID,
			UserLogin:   ul,
		},
	}, nil
}

type PairingCodeLogin struct {
	User       *bridgev2.User
	Main       *WhatsAppConnector
	Client     *whatsmeow.Client
	cancelChan context.CancelFunc
	QRChan     <-chan whatsmeow.QRChannelItem

	AccData *store.Device
}

var _ bridgev2.LoginProcessUserInput = (*PairingCodeLogin)(nil)
var _ bridgev2.LoginProcessDisplayAndWait = (*PairingCodeLogin)(nil)

func (pc *PairingCodeLogin) Cancel() {
	pc.Client.Disconnect()
	pc.cancelChan()
	go func() {
		for range pc.QRChan {
		}
	}()
}

func (pc *PairingCodeLogin) SubmitUserInput(_ context.Context, input map[string]string) (*bridgev2.LoginStep, error) {
	phoneNumber := input["phone_number"]
	if phoneNumber == "" {
		return nil, fmt.Errorf("invalid or missing phone number")
	}
	pairingCode, err := pc.Client.PairPhone(phoneNumber, true, whatsmeow.PairClientChrome, "Chrome (Linux)")
	if err != nil {
		return nil, err
	}
	return &bridgev2.LoginStep{
		Type:         bridgev2.LoginStepTypeDisplayAndWait,
		StepID:       LoginStepPairingCode,
		Instructions: "Input the pairing code on the WhatsApp mobile app to log in",
		DisplayAndWaitParams: &bridgev2.LoginDisplayAndWaitParams{
			Type: bridgev2.LoginDisplayTypeCode,
			Data: pairingCode,
		},
	}, nil
}

var onlyNumberRegex = regexp.MustCompile(`\D+`)

func (pc *PairingCodeLogin) Start(_ context.Context) (*bridgev2.LoginStep, error) {
	client, qrChan, cancelFunc, err := makeQRChan(pc.Main, pc.User)
	if err != nil {
		return nil, err
	}
	pc.QRChan = qrChan
	pc.cancelChan = cancelFunc
	pc.Client = client

	return &bridgev2.LoginStep{
		Type:   bridgev2.LoginStepTypeUserInput,
		StepID: LoginStepPairingCodeInput,
		UserInputParams: &bridgev2.LoginUserInputParams{
			Fields: []bridgev2.LoginInputDataField{
				{
					Type:        bridgev2.LoginInputFieldTypePhoneNumber,
					ID:          "phone_number",
					Name:        "phone number",
					Description: "An international phone number format without symbols",
					Validate: func(s string) (string, error) {
						return onlyNumberRegex.ReplaceAllString(s, ""), nil
					},
				},
			},
		},
	}, nil
}

func (pc *PairingCodeLogin) Wait(ctx context.Context) (*bridgev2.LoginStep, error) {

	select {
	case resp := <-pc.QRChan:
		if resp.Event == "code" {
			return &bridgev2.LoginStep{
				Type:   bridgev2.LoginStepTypeDisplayAndWait,
				StepID: LoginStepPairingCode,
				DisplayAndWaitParams: &bridgev2.LoginDisplayAndWaitParams{
					Type: bridgev2.LoginDisplayTypeNothing,
				},
			}, nil
		} else if resp.Event != "success" {
			pc.Cancel()
			return nil, fmt.Errorf("did not pair properly, err: %s", resp.Event)
		}
	case <-ctx.Done():
		pc.Cancel()
		return nil, ctx.Err()
	}

	defer pc.Cancel()

	ul, err := makeUserLogin(ctx, pc.User, pc.Client)
	if err != nil {
		return nil, fmt.Errorf("failed to create user login: %w", err)
	}

	err = ul.Client.Connect(ul.Log.WithContext(context.Background()))

	if err != nil {
		return nil, fmt.Errorf("failed to connect after login: %w", err)
	}

	return &bridgev2.LoginStep{
		Type:         bridgev2.LoginStepTypeComplete,
		StepID:       LoginStepComplete,
		Instructions: fmt.Sprintf("Successfully logged in as %s / %s", ul.RemoteName, ul.ID),
		CompleteParams: &bridgev2.LoginCompleteParams{
			UserLoginID: ul.ID,
			UserLogin:   ul,
		},
	}, nil
}
