package connector

import (
	"context"
	"fmt"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog"
	"go.mau.fi/util/exsync"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/status"

	"go.mau.fi/mautrix-whatsapp/pkg/waid"
)

const (
	LoginStepIDQR          = "fi.mau.whatsapp.login.qr"
	LoginStepIDPhoneNumber = "fi.mau.whatsapp.login.phone"
	LoginStepIDCode        = "fi.mau.whatsapp.login.code"
	LoginStepIDComplete    = "fi.mau.whatsapp.login.complete"

	LoginFlowIDQR    = "qr"
	LoginFlowIDPhone = "phone"
)

func (wa *WhatsAppConnector) GetLoginFlows() []bridgev2.LoginFlow {
	return []bridgev2.LoginFlow{{
		Name:        "QR",
		Description: "Scan a QR code to pair the bridge to your WhatsApp account",
		ID:          LoginFlowIDQR,
	}, {
		Name:        "Pairing code",
		Description: "Input your phone number to get a pairing code, to pair the bridge to your WhatsApp account",
		ID:          LoginFlowIDPhone,
	}}
}

var (
	ErrLoginClientOutdated = bridgev2.RespError{
		ErrCode:    "FI.MAU.WHATSAPP.CLIENT_OUTDATED",
		Err:        "Got client outdated error while waiting for QRs. The bridge must be updated to continue.",
		StatusCode: http.StatusInternalServerError,
	}
	ErrLoginMultideviceNotEnabled = bridgev2.RespError{
		ErrCode:    "FI.MAU.WHATSAPP.MULTIDEVICE_NOT_ENABLED",
		Err:        "Please enable WhatsApp web multidevice and scan the QR code again.",
		StatusCode: http.StatusBadRequest,
	}
	ErrLoginTimeout = bridgev2.RespError{
		ErrCode:    "FI.MAU.WHATSAPP.LOGIN_TIMEOUT",
		Err:        "Entering code or scanning QR timed out. Please try again.",
		StatusCode: http.StatusBadRequest,
	}
	ErrUnexpectedDisconnect = bridgev2.RespError{
		ErrCode:    "FI.MAU.WHATSAPP.LOGIN_UNEXPECTED_EVENT",
		Err:        "Unexpected event while waiting for login",
		StatusCode: http.StatusInternalServerError,
	}
)

func (wa *WhatsAppConnector) CreateLogin(_ context.Context, user *bridgev2.User, flowID string) (bridgev2.LoginProcess, error) {
	return &WALogin{
		User:      user,
		Main:      wa,
		PhoneCode: flowID == LoginFlowIDPhone,
		Log: user.Log.With().
			Str("action", "login").
			Bool("phone_code", flowID == LoginFlowIDPhone).
			Logger(),

		WaitForQRs:    exsync.NewEvent(),
		LoginComplete: exsync.NewEvent(),
		Received515:   exsync.NewEvent(),
	}, nil
}

type WALogin struct {
	User      *bridgev2.User
	Main      *WhatsAppConnector
	Client    *whatsmeow.Client
	Log       zerolog.Logger
	PhoneCode bool
	Timezone  string

	QRs           []string
	StartTime     time.Time
	LoginError    error
	LoginSuccess  *events.PairSuccess
	WaitForQRs    *exsync.Event
	LoginComplete *exsync.Event
	Received515   *exsync.Event
	PrevQRIndex   atomic.Int32

	Closed         atomic.Bool
	EventHandlerID uint32
}

var (
	_ bridgev2.LoginProcessDisplayAndWait = (*WALogin)(nil)
	_ bridgev2.LoginProcessUserInput      = (*WALogin)(nil)
	_ bridgev2.LoginProcessWithOverride   = (*WALogin)(nil)
)

const LoginConnectWait = 15 * time.Second

func (wl *WALogin) Start(ctx context.Context) (*bridgev2.LoginStep, error) {
	wl.Main.firstClientConnectOnce.Do(wl.Main.onFirstClientConnect)
	device := wl.Main.DeviceStore.NewDevice()
	wl.Client = whatsmeow.NewClient(device, waLog.Zerolog(wl.Log))
	wl.Client.EnableAutoReconnect = false
	wl.Client.DisableLoginAutoReconnect = true
	wl.EventHandlerID = wl.Client.AddEventHandler(wl.handleEvent)
	if err := wl.Main.updateProxy(ctx, wl.Client, true); err != nil {
		return nil, err
	}

	if wl.PhoneCode {
		return &bridgev2.LoginStep{
			Type:   bridgev2.LoginStepTypeUserInput,
			StepID: LoginStepIDPhoneNumber,
			UserInputParams: &bridgev2.LoginUserInputParams{
				Fields: []bridgev2.LoginInputDataField{{
					Type:        bridgev2.LoginInputFieldTypePhoneNumber,
					ID:          "phone_number",
					Name:        "Phone number",
					Description: "Your WhatsApp phone number in international format",
				}},
			},
		}, nil
	}
	err := wl.Client.Connect()
	if err != nil {
		wl.Log.Err(err).Msg("Failed to connect to WhatsApp for QR login")
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, LoginConnectWait)
	defer cancel()
	err = wl.WaitForQRs.Wait(ctx)
	if err != nil {
		wl.Log.Warn().Err(err).Msg("Timed out waiting for first QR")
		wl.Cancel()
		return nil, err
	}
	return makeQRStep(wl.QRs[0]), nil
}

func (wl *WALogin) StartWithOverride(ctx context.Context, old *bridgev2.UserLogin) (*bridgev2.LoginStep, error) {
	step, err := wl.Start(ctx)
	if err == nil && step != nil && old != nil && step.StepID == LoginStepIDPhoneNumber {
		phoneNumber := fmt.Sprintf("+%s", old.ID)
		wl.Log.Debug().
			Str("phone_number", phoneNumber).
			Msg("Auto-submitting phone number for relogin")
		return wl.SubmitUserInput(ctx, map[string]string{
			"phone_number": phoneNumber,
		})
	}
	return step, err
}

func (wl *WALogin) SubmitUserInput(ctx context.Context, input map[string]string) (*bridgev2.LoginStep, error) {
	ctx, cancel := context.WithTimeout(ctx, LoginConnectWait)
	defer cancel()
	err := wl.Client.Connect()
	if err != nil {
		wl.Log.Err(err).Msg("Failed to connect to WhatsApp for phone code login")
		return nil, err
	}
	err = wl.WaitForQRs.Wait(ctx)
	if err != nil {
		wl.Log.Warn().Err(err).Msg("Timed out waiting for connection")
		return nil, fmt.Errorf("failed to wait for connection: %w", err)
	}
	pairingCode, err := wl.Client.PairPhone(ctx, input["phone_number"], true, whatsmeow.PairClientChrome, "Chrome (Linux)")
	if err != nil {
		wl.Log.Err(err).Msg("Failed to request phone code login")
		return nil, err
	}
	wl.Log.Debug().Msg("Phone code login started")
	return &bridgev2.LoginStep{
		Type:         bridgev2.LoginStepTypeDisplayAndWait,
		StepID:       LoginStepIDCode,
		Instructions: "Input the pairing code in the WhatsApp mobile app to log in",
		DisplayAndWaitParams: &bridgev2.LoginDisplayAndWaitParams{
			Type: bridgev2.LoginDisplayTypeCode,
			Data: pairingCode,
		},
	}, nil
}

func makeQRStep(qr string) *bridgev2.LoginStep {
	return &bridgev2.LoginStep{
		Type:         bridgev2.LoginStepTypeDisplayAndWait,
		StepID:       LoginStepIDQR,
		Instructions: "Scan the QR code with the WhatsApp mobile app to log in",
		DisplayAndWaitParams: &bridgev2.LoginDisplayAndWaitParams{
			Type: bridgev2.LoginDisplayTypeQR,
			Data: qr,
		},
	}
}

var qrIntervals = []time.Duration{1 * time.Minute, 20 * time.Second, 20 * time.Second, 20 * time.Second, 20 * time.Second, 20 * time.Second}

func (wl *WALogin) getQRIndex() (currentIndex int, timeUntilNext time.Duration) {
	timeSinceStart := time.Since(wl.StartTime)
	var sum time.Duration
	for i, interval := range qrIntervals {
		if timeSinceStart > sum+interval {
			sum += interval
		} else {
			return i, interval - (timeSinceStart - sum)
		}
	}
	return -1, 0
}

func (wl *WALogin) handleEvent(rawEvt any) {
	if wl.Closed.Load() {
		return
	}
	switch evt := rawEvt.(type) {
	case *events.QR:
		wl.Log.Debug().Int("code_count", len(evt.Codes)).Msg("Received QR codes")
		wl.QRs = evt.Codes
		wl.StartTime = time.Now()
		wl.WaitForQRs.Set()
		return
	case *events.QRScannedWithoutMultidevice:
		wl.Log.Error().Msg("QR code scanned without multidevice enabled")
		wl.LoginError = ErrLoginMultideviceNotEnabled
	case *events.ClientOutdated:
		wl.Log.Error().Msg("Got client outdated error")
		wl.LoginError = ErrLoginClientOutdated
	case *events.PairSuccess:
		wl.Log.Info().Any("event_data", evt).Msg("Got pair successful event")
		wl.LoginSuccess = evt
	case *events.PairError:
		wl.Log.Error().Any("event_data", evt).Msg("Got pair error event")
		wl.LoginError = bridgev2.RespError{
			ErrCode:    "FI.MAU.WHATSAPP.PAIR_ERROR",
			Err:        evt.Error.Error(),
			StatusCode: http.StatusInternalServerError,
		}
	case *events.Disconnected:
		wl.Log.Warn().Msg("Got disconnected event (login timed out)")
		wl.LoginError = ErrLoginTimeout
	case *events.Connected, *events.ConnectFailure, *events.LoggedOut, *events.TemporaryBan:
		wl.Log.Warn().Any("event_data", evt).Type("event_type", evt).Msg("Got unexpected disconnect event")
		wl.LoginError = ErrUnexpectedDisconnect
	case *events.ManualLoginReconnect:
		wl.Received515.Set()
	default:
		wl.Log.Warn().Type("event_type", evt).Msg("Got unexpected event")
		return
	}
	wl.LoginComplete.Set()
}

func (wl *WALogin) Wait(ctx context.Context) (*bridgev2.LoginStep, error) {
	if wl.PhoneCode {
		err := wl.LoginComplete.Wait(ctx)
		if err != nil {
			wl.Cancel()
			return nil, err
		}
	} else {
		prevIndex := int(wl.PrevQRIndex.Load())
		currentIndex, timeUntilNext := wl.getQRIndex()
		nextIndex := currentIndex + 1
		logEvt := wl.Log.Debug().
			Int("prev_index", prevIndex).
			Int("current_index", currentIndex)
		if currentIndex > prevIndex {
			logEvt.Msg("Returning new QR immediately")
			wl.PrevQRIndex.Store(int32(currentIndex))
			return makeQRStep(wl.QRs[currentIndex]), nil
		}
		logEvt.Int("next_index", nextIndex).Stringer("time_until_next", timeUntilNext).Msg("Waiting for next QR")
		select {
		case <-time.After(timeUntilNext):
			if nextIndex < 0 || nextIndex >= len(wl.QRs) {
				wl.Log.Debug().Msg("No more QRs to return")
				wl.Cancel()
				return nil, ErrLoginTimeout
			}
			wl.PrevQRIndex.Store(int32(nextIndex))
			return makeQRStep(wl.QRs[nextIndex]), nil
		case <-ctx.Done():
			wl.Cancel()
			return nil, ctx.Err()
		case <-wl.LoginComplete.GetChan():
		}
	}
	if wl.LoginError != nil {
		wl.Log.Debug().Err(wl.LoginError).Msg("Login completed with error")
		wl.Cancel()
		return nil, wl.LoginError
	}
	wl.Log.Debug().Msg("Login completed without error, waiting for 515 event")
	ctxTimeout, cancel := context.WithTimeout(ctx, 10*time.Second)
	err := wl.Received515.Wait(ctxTimeout)
	cancel()
	wl.Cancel()
	if err != nil {
		return nil, fmt.Errorf("timed out waiting for 515: %w", err)
	}

	newLoginID := waid.MakeUserLoginID(wl.LoginSuccess.ID)
	ul, err := wl.User.NewLogin(ctx, &database.UserLogin{
		ID:         newLoginID,
		RemoteName: "+" + wl.LoginSuccess.ID.User,
		RemoteProfile: status.RemoteProfile{
			Phone: "+" + wl.LoginSuccess.ID.User,
			Name:  wl.LoginSuccess.BusinessName,
		},
		Metadata: &waid.UserLoginMetadata{
			WALID:      wl.LoginSuccess.LID.User,
			WADeviceID: wl.LoginSuccess.ID.Device,
			Timezone:   wl.Timezone,

			HistorySyncPortalsNeedCreating: true,
		},
	}, &bridgev2.NewLoginParams{
		DeleteOnConflict: true,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create user login: %w", err)
	}

	ul.Client.(*WhatsAppClient).isNewLogin = true
	ul.Client.Connect(ul.Log.WithContext(context.Background()))

	return &bridgev2.LoginStep{
		Type:         bridgev2.LoginStepTypeComplete,
		StepID:       LoginStepIDComplete,
		Instructions: fmt.Sprintf("Successfully logged in as %s", ul.RemoteName),
		CompleteParams: &bridgev2.LoginCompleteParams{
			UserLoginID: ul.ID,
			UserLogin:   ul,
		},
	}, nil
}

func (wl *WALogin) Cancel() {
	wl.Closed.Store(true)
	wl.Client.RemoveEventHandler(wl.EventHandlerID)
	wl.Client.Disconnect()
}
