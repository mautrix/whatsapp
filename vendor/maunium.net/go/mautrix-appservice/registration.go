package appservice

import (
	"io/ioutil"

	"regexp"

	"gopkg.in/yaml.v2"
)

// Registration contains the data in a Matrix appservice registration.
// See https://matrix.org/docs/spec/application_service/unstable.html#registration
type Registration struct {
	ID              string     `yaml:"id"`
	URL             string     `yaml:"url"`
	AppToken        string     `yaml:"as_token"`
	ServerToken     string     `yaml:"hs_token"`
	SenderLocalpart string     `yaml:"sender_localpart"`
	RateLimited     bool       `yaml:"rate_limited"`
	Namespaces      Namespaces `yaml:"namespaces"`
}

// CreateRegistration creates a Registration with random appservice and homeserver tokens.
func CreateRegistration(name string) *Registration {
	return &Registration{
		AppToken:    RandomString(64),
		ServerToken: RandomString(64),
	}
}

// LoadRegistration loads a YAML file and turns it into a Registration.
func LoadRegistration(path string) (*Registration, error) {
	data, err := ioutil.ReadFile(path)
	if err != nil {
		return nil, err
	}

	reg := &Registration{}
	err = yaml.Unmarshal(data, reg)
	if err != nil {
		return nil, err
	}
	return reg, nil
}

// Save saves this Registration into a file at the given path.
func (reg *Registration) Save(path string) error {
	data, err := yaml.Marshal(reg)
	if err != nil {
		return err
	}
	return ioutil.WriteFile(path, data, 0600)
}

// YAML returns the registration in YAML format.
func (reg *Registration) YAML() (string, error) {
	data, err := yaml.Marshal(reg)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// Namespaces contains the three areas that appservices can reserve parts of.
type Namespaces struct {
	UserIDs     []Namespace `yaml:"users,omitempty"`
	RoomAliases []Namespace `yaml:"aliases,omitempty"`
	RoomIDs     []Namespace `yaml:"rooms,omitempty"`
}

// Namespace is a reserved namespace in any area.
type Namespace struct {
	Regex     string `yaml:"regex"`
	Exclusive bool   `yaml:"exclusive"`
}

// RegisterUserIDs creates an user ID namespace registration.
func (nslist *Namespaces) RegisterUserIDs(regex *regexp.Regexp, exclusive bool) {
	nslist.UserIDs = append(nslist.UserIDs, Namespace{
		Regex:     regex.String(),
		Exclusive: exclusive,
	})
}

// RegisterRoomAliases creates an room alias namespace registration.
func (nslist *Namespaces) RegisterRoomAliases(regex *regexp.Regexp, exclusive bool) {
	nslist.RoomAliases = append(nslist.RoomAliases, Namespace{
		Regex:     regex.String(),
		Exclusive: exclusive,
	})
}

// RegisterRoomIDs creates an room ID namespace registration.
func (nslist *Namespaces) RegisterRoomIDs(regex *regexp.Regexp, exclusive bool) {
	nslist.RoomIDs = append(nslist.RoomIDs, Namespace{
		Regex:     regex.String(),
		Exclusive: exclusive,
	})
}
