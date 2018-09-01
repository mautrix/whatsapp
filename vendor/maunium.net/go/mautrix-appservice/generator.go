package appservice

import (
	"bufio"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/fatih/color"
)

func readString(reader *bufio.Reader, message, defaultValue string) (string, error) {
	color.Green(message)
	if len(defaultValue) > 0 {
		fmt.Printf("[%s]", defaultValue)
	}
	fmt.Print("> ")
	val, err := reader.ReadString('\n')
	if err != nil {
		return "", err
	}
	val = strings.TrimSuffix(val, "\n")
	if len(val) == 0 {
		return defaultValue, nil
	}
	val = strings.TrimSuffix(val, "\r")
	if len(val) == 0 {
		return defaultValue, nil
	}
	return val, nil
}

const (
	yes      = "yes"
	yesShort = "y"
)

// GenerateRegistration asks the user questions and generates a config and registration based on the answers.
func GenerateRegistration(asName, botName string, reserveRooms, reserveUsers bool) {
	var boldCyan = color.New(color.FgCyan).Add(color.Bold)
	var boldGreen = color.New(color.FgGreen).Add(color.Bold)
	boldCyan.Println("Generating appservice config and registration.")
	reader := bufio.NewReader(os.Stdin)

	registration := CreateRegistration()
	config := Create()
	registration.RateLimited = false

	name, err := readString(reader, "Enter name for appservice", asName)
	if err != nil {
		fmt.Println("Failed to read user Input:", err)
		return
	}
	registration.ID = name

	registration.SenderLocalpart, err = readString(reader, "Enter bot username", botName)
	if err != nil {
		fmt.Println("Failed to read user Input:", err)
		return
	}

	asProtocol, err := readString(reader, "Enter appservice host protocol", "http")
	if err != nil {
		fmt.Println("Failed to read user Input:", err)
		return
	}
	if asProtocol == "https" {
		sslInput, err := readString(reader, "Do you want the appservice to handle SSL [yes/no]?", "yes")
		if err != nil {
			fmt.Println("Failed to read user Input:", err)
			return
		}
		wantSSL := strings.ToLower(sslInput)
		if wantSSL == yes {
			config.Host.TLSCert, err = readString(reader, "Enter path to SSL certificate", "appservice.crt")
			if err != nil {
				fmt.Println("Failed to read user Input:", err)
				return
			}
			config.Host.TLSKey, err = readString(reader, "Enter path to SSL key", "appservice.key")
			if err != nil {
				fmt.Println("Failed to read user Input:", err)
				return
			}
		}
	}
	asHostname, err := readString(reader, "Enter appservice hostname", "localhost")
	if err != nil {
		fmt.Println("Failed to read user Input:", err)
		return
	}
	asInput, err := readString(reader, "Enter appservice host port", "29313")
	if err != nil {
		fmt.Println("Failed to read user Input:", err)
		return
	}
	asPort, convErr := strconv.Atoi(asInput)
	if convErr != nil {
		fmt.Println("Failed to parse port:", convErr)
		return
	}
	registration.URL = fmt.Sprintf("%s://%s:%d", asProtocol, asHostname, asPort)
	config.Host.Hostname = asHostname
	config.Host.Port = uint16(asPort)

	config.HomeserverURL, err = readString(reader, "Enter homeserver address", "http://localhost:8008")
	if err != nil {
		fmt.Println("Failed to read user Input:", err)
		return
	}
	config.HomeserverDomain, err = readString(reader, "Enter homeserver domain", "example.com")
	if err != nil {
		fmt.Println("Failed to read user Input:", err)
		return
	}
	config.LogConfig.Directory, err = readString(reader, "Enter directory for logs", "./logs")
	if err != nil {
		fmt.Println("Failed to read user Input:", err)
		return
	}
	os.MkdirAll(config.LogConfig.Directory, 0755)

	if reserveRooms || reserveUsers {
		for {
			namespace, err := readString(reader, "Enter namespace prefix", fmt.Sprintf("_%s_", name))
			if err != nil {
				fmt.Println("Failed to read user Input:", err)
				return
			}
			roomNamespaceRegex, err := regexp.Compile(fmt.Sprintf("#%s.+:%s", namespace, config.HomeserverDomain))
			if err != nil {
				fmt.Println(err)
				continue
			}
			userNamespaceRegex, regexpErr := regexp.Compile(fmt.Sprintf("@%s.+:%s", namespace, config.HomeserverDomain))
			if regexpErr != nil {
				fmt.Println("Failed to generate regexp for the userNamespace:", err)
				return
			}
			if reserveRooms {
				registration.Namespaces.RegisterRoomAliases(roomNamespaceRegex, true)
			}
			if reserveUsers {
				registration.Namespaces.RegisterUserIDs(userNamespaceRegex, true)
			}
			break
		}
	}

	boldCyan.Println("\n==== Registration generated ====")
	yamlString, yamlErr := registration.YAML()
	if err != nil {
		fmt.Println("Failed to return the registration Config:", yamlErr)
		return
	}
	color.Yellow(yamlString)

	okInput, readErr := readString(reader, "Does the registration look OK [yes/no]?", "yes")
	if readErr != nil {
		fmt.Println("Failed to read user Input:", readErr)
		return
	}
	ok := strings.ToLower(okInput)
	if ok != yesShort && ok != yes {
		fmt.Println("Cancelling generation.")
		return
	}

	path, err := readString(reader, "Where should the registration be saved?", "registration.yaml")
	if err != nil {
		fmt.Println("Failed to read user Input:", err)
		return
	}
	err = registration.Save(path)
	if err != nil {
		fmt.Println("Failed to save registration:", err)
		return
	}
	boldGreen.Println("Registration saved.")

	config.RegistrationPath = path

	boldCyan.Println("\n======= Config generated =======")
	color.Yellow(config.YAML())

	okString, err := readString(reader, "Does the config look OK [yes/no]?", "yes")
	if err != nil {
		fmt.Println("Failed to read user Input:", err)
		return
	}
	ok = strings.ToLower(okString)
	if ok != yesShort && ok != yes {
		fmt.Println("Cancelling generation.")
		return
	}

	path, err = readString(reader, "Where should the config be saved?", "config.yaml")
	if err != nil {
		fmt.Println("Failed to read user Input:", err)
		return
	}
	err = config.Save(path)
	if err != nil {
		fmt.Println("Failed to save config:", err)
		return
	}
	boldGreen.Println("Config saved.")
}
