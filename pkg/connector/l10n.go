package connector

import (
	"go.mau.fi/util/l10n"
)

var L = l10n.NewNamespace("mautrix-whatsapp")

var (
	msgFlowQRName           = L.Define("login.flow.qr.name", "QR")
	msgFlowQRDescription    = L.Define("login.flow.qr.description", "Scan a QR code to pair the bridge to your WhatsApp account")
	msgFlowPhoneName        = L.Define("login.flow.phone.name", "Pairing code")
	msgFlowPhoneDescription = L.Define("login.flow.phone.description", "Input your phone number to get a pairing code, to pair the bridge to your WhatsApp account")

	msgFieldPhoneName        = L.Define("login.field.phone_number.name", "Phone number")
	msgFieldPhoneDescription = L.Define("login.field.phone_number.description", "Your WhatsApp phone number in international format")

	msgStepQRInstructions       = L.Define("login.step.qr.instructions", "Scan the QR code with the WhatsApp mobile app to log in")
	msgStepCodeInstructions     = L.Define("login.step.code.instructions", "Input the pairing code in the WhatsApp mobile app to log in")
	msgStepCompleteInstructions = L.Define(
		"login.step.complete.instructions", "Successfully logged in as {phone}",
		l10n.WithComment("phone is the international phone number of the WhatsApp account, e.g. +15550100"),
	)
)
