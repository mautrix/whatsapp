package appservice

import (
	"fmt"
	"html/template"
	"io/ioutil"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v2"

	"errors"
	"maunium.net/go/gomatrix"
	"maunium.net/go/maulogger"
	"net/http"
	"regexp"
	"strings"
)

// EventChannelSize is the size for the Events channel in Appservice instances.
var EventChannelSize = 64

// Create a blank appservice instance.
func Create() *AppService {
	return &AppService{
		LogConfig:  CreateLogConfig(),
		clients:    make(map[string]*gomatrix.Client),
		intents:    make(map[string]*IntentAPI),
		StateStore: NewBasicStateStore(),
	}
}

// Load an appservice config from a file.
func Load(path string) (*AppService, error) {
	data, readErr := ioutil.ReadFile(path)
	if readErr != nil {
		return nil, readErr
	}

	var config = &AppService{}
	yaml.Unmarshal(data, config)
	return config, nil
}

// QueryHandler handles room alias and user ID queries from the homeserver.
type QueryHandler interface {
	QueryAlias(alias string) bool
	QueryUser(userID string) bool
}

type QueryHandlerStub struct{}

func (qh *QueryHandlerStub) QueryAlias(alias string) bool {
	return false
}

func (qh *QueryHandlerStub) QueryUser(userID string) bool {
	return false
}

// AppService is the main config for all appservices.
// It also serves as the appservice instance struct.
type AppService struct {
	HomeserverDomain string     `yaml:"homeserver_domain"`
	HomeserverURL    string     `yaml:"homeserver_url"`
	RegistrationPath string     `yaml:"registration"`
	Host             HostConfig `yaml:"host"`
	LogConfig        LogConfig  `yaml:"logging"`

	Registration *Registration    `yaml:"-"`
	Log          maulogger.Logger `yaml:"-"`

	lastProcessedTransaction string
	Events                   chan *gomatrix.Event `yaml:"-"`
	QueryHandler             QueryHandler         `yaml:"-"`
	StateStore               StateStore           `yaml:"-"`

	server    *http.Server
	botClient *gomatrix.Client
	botIntent *IntentAPI
	clients   map[string]*gomatrix.Client
	intents   map[string]*IntentAPI
}

// HostConfig contains info about how to host the appservice.
type HostConfig struct {
	Hostname string `yaml:"hostname"`
	Port     uint16 `yaml:"port"`
	TLSKey   string `yaml:"tls_key,omitempty"`
	TLSCert  string `yaml:"tls_cert,omitempty"`
}

// Address gets the whole address of the Appservice.
func (hc *HostConfig) Address() string {
	return fmt.Sprintf("%s:%d", hc.Hostname, hc.Port)
}

// Save saves this config into a file at the given path.
func (as *AppService) Save(path string) error {
	data, err := yaml.Marshal(as)
	if err != nil {
		return err
	}
	return ioutil.WriteFile(path, data, 0644)
}

// YAML returns the config in YAML format.
func (as *AppService) YAML() (string, error) {
	data, err := yaml.Marshal(as)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func (as *AppService) BotMXID() string {
	return fmt.Sprintf("@%s:%s", as.Registration.SenderLocalpart, as.HomeserverDomain)
}

var MatrixUserIDRegex = regexp.MustCompile("^@([^:]+):(.+)$")

func ParseUserID(mxid string) (string, string) {
	match := MatrixUserIDRegex.FindStringSubmatch(mxid)
	if match != nil && len(match) == 3 {
		return match[1], match[2]
	}
	return "", ""
}

func (as *AppService) Intent(userID string) *IntentAPI {
	intent, ok := as.intents[userID]
	if !ok {
		localpart, homeserver := ParseUserID(userID)
		if len(localpart) == 0 || homeserver != as.HomeserverDomain {
			return nil
		}
		intent = as.NewIntentAPI(localpart)
		as.intents[userID] = intent
	}
	return intent
}

func (as *AppService) BotIntent() *IntentAPI {
	if as.botIntent == nil {
		as.botIntent = as.NewIntentAPI(as.Registration.SenderLocalpart)
	}
	return as.botIntent
}

func (as *AppService) Client(userID string) *gomatrix.Client {
	client, ok := as.clients[userID]
	if !ok {
		var err error
		client, err = gomatrix.NewClient(as.HomeserverURL, userID, as.Registration.AppToken)
		if err != nil {
			as.Log.Fatalln("Failed to create gomatrix instance:", err)
			return nil
		}
		client.Syncer = nil
		client.Store = nil
		client.AppServiceUserID = userID
		client.Logger = as.Log.Sub(userID)
		as.clients[userID] = client
	}
	return client
}

func (as *AppService) BotClient() *gomatrix.Client {
	if as.botClient == nil {
		var err error
		as.botClient, err = gomatrix.NewClient(as.HomeserverURL, as.BotMXID(), as.Registration.AppToken)
		if err != nil {
			as.Log.Fatalln("Failed to create gomatrix instance:", err)
			return nil
		}
		as.botClient.Syncer = nil
		as.botClient.Store = nil
		as.botClient.Logger = as.Log.Sub("Bot")
	}
	return as.botClient
}

// Init initializes the logger and loads the registration of this appservice.
func (as *AppService) Init() (bool, error) {
	as.Events = make(chan *gomatrix.Event, EventChannelSize)
	as.QueryHandler = &QueryHandlerStub{}

	as.Log = maulogger.Create()
	as.LogConfig.Configure(as.Log)
	as.Log.Debugln("Logger initialized successfully.")

	if len(as.RegistrationPath) > 0 {
		var err error
		as.Registration, err = LoadRegistration(as.RegistrationPath)
		if err != nil {
			return false, err
		}
	}

	as.Log.Debugln("Appservice initialized successfully.")
	return true, nil
}

// LogConfig contains configs for the logger.
type LogConfig struct {
	Directory       string `yaml:"directory"`
	FileNameFormat  string `yaml:"file_name_format"`
	FileDateFormat  string `yaml:"file_date_format"`
	FileMode        uint32 `yaml:"file_mode"`
	TimestampFormat string `yaml:"timestamp_format"`
	RawPrintLevel   string `yaml:"print_level"`
	PrintLevel      int    `yaml:"-"`
}

type umLogConfig LogConfig

func (lc *LogConfig) UnmarshalYAML(unmarshal func(interface{}) error) error {
	err := unmarshal((*umLogConfig)(lc))
	if err != nil {
		return err
	}

	switch strings.ToUpper(lc.RawPrintLevel) {
	case "DEBUG":
		lc.PrintLevel = maulogger.LevelDebug.Severity
	case "INFO":
		lc.PrintLevel = maulogger.LevelInfo.Severity
	case "WARN", "WARNING":
		lc.PrintLevel = maulogger.LevelWarn.Severity
	case "ERR", "ERROR":
		lc.PrintLevel = maulogger.LevelError.Severity
	case "FATAL":
		lc.PrintLevel = maulogger.LevelFatal.Severity
	default:
		return errors.New("invalid print level " + lc.RawPrintLevel)
	}
	return err
}

func (lc *LogConfig) MarshalYAML() (interface{}, error) {
	switch {
	case lc.PrintLevel >= maulogger.LevelFatal.Severity:
		lc.RawPrintLevel = maulogger.LevelFatal.Name
	case lc.PrintLevel >= maulogger.LevelError.Severity:
		lc.RawPrintLevel = maulogger.LevelError.Name
	case lc.PrintLevel >= maulogger.LevelWarn.Severity:
		lc.RawPrintLevel = maulogger.LevelWarn.Name
	case lc.PrintLevel >= maulogger.LevelInfo.Severity:
		lc.RawPrintLevel = maulogger.LevelInfo.Name
	default:
		lc.RawPrintLevel = maulogger.LevelDebug.Name
	}
	return lc, nil
}

// CreateLogConfig creates a basic LogConfig.
func CreateLogConfig() LogConfig {
	return LogConfig{
		Directory:       "./logs",
		FileNameFormat:  "%[1]s-%02[2]d.log",
		TimestampFormat: "Jan _2, 2006 15:04:05",
		FileMode:        0600,
		FileDateFormat:  "2006-01-02",
		PrintLevel:      10,
	}
}

type FileFormatData struct {
	Date string
	Index int
}

// GetFileFormat returns a mauLogger-compatible logger file format based on the data in the struct.
func (lc LogConfig) GetFileFormat() maulogger.LoggerFileFormat {
	os.MkdirAll(lc.Directory, 0700)
	path := filepath.Join(lc.Directory, lc.FileNameFormat)
	tpl, _ := template.New("fileformat").Parse(path)

	return func(now string, i int) string {
		var buf strings.Builder
		tpl.Execute(&buf, FileFormatData{
			Date: now,
			Index: i,
		})
		return buf.String()
	}
}

// Configure configures a mauLogger instance with the data in this struct.
func (lc LogConfig) Configure(log maulogger.Logger) {
	basicLogger := log.(*maulogger.BasicLogger)
	basicLogger.FileFormat = lc.GetFileFormat()
	basicLogger.FileMode = os.FileMode(lc.FileMode)
	basicLogger.FileTimeFormat = lc.FileDateFormat
	basicLogger.TimeFormat = lc.TimestampFormat
	basicLogger.PrintLevel = lc.PrintLevel
}
