// mauflag - An extendable command-line argument parser for Golang
// Copyright (C) 2016 Maunium
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.

// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU General Public License for more details.

// You should have received a copy of the GNU General Public License
// along with this program.  If not, see <http://www.gnu.org/licenses/>.

package mauflag

import (
	"fmt"
	"os"
	"strings"
)

// Set is a set of flags with certain input arguments
type Set struct {
	// The list of strings used as input
	InputArgs []string
	// Whether or not to ignore all flags after the user has entered two dashes with no flag key ("--")
	// If enabled, all arguments after two dashes with no flag key will go into the args array (@see Args())
	DoubleLineEscape bool
	// Whether or not to exit the program when there's an error
	// If enabled, the error message will be printed to `stderr` after which `os.Exit(1)` will be called.
	ExitOnError bool

	wantHelp      *bool
	basicUsage    string
	helpFirstLine string

	args  []string
	flags []*Flag
}

// New creates a new flagset
func New(args []string) *Set {
	return &Set{InputArgs: args, DoubleLineEscape: true, ExitOnError: false}
}

// Args returns the arguments that weren't associated with any flag
func (fs *Set) Args() []string {
	return fs.args
}

// Arg returns the string at the given index from the list Args() returns
// If the index does not exist, Arg will return an empty string.
func (fs *Set) Arg(i int) string {
	if len(fs.args) <= i {
		return ""
	}
	return fs.args[i]
}

// NArg returns the number of arguments not associated with any flags
func (fs *Set) NArg() int {
	return len(fs.args)
}

// MakeHelpFlag creates the -h, --help flag
func (fs *Set) MakeHelpFlag() (*bool, *Flag) {
	var flag = fs.Make().Key("h", "help").Usage("Show this help page.").UsageCategory("Help")
	fs.wantHelp = flag.Bool()
	return fs.wantHelp, flag
}

// CheckHelpFlag checks if the help flag is set and prints the help page if needed.
// Return value tells whether or not the help page was printed
func (fs *Set) CheckHelpFlag() bool {
	if fs.wantHelp != nil && *fs.wantHelp {
		fs.PrintHelp()
		return true
	}
	return false
}

// SetHelpTitles sets the first line (program name and basic explanation) and basic usage specification
func (fs *Set) SetHelpTitles(firstLine, basicUsage string) {
	fs.helpFirstLine = firstLine
	fs.basicUsage = basicUsage
}

// PrintHelp prints the help page
func (fs *Set) PrintHelp() {
	var helpSectNames = make(map[string]int)
	var helpSectIndexes = make(map[int]string)
	var helpSects = make([][]*Flag, 0)
	for _, flag := range fs.flags {
		index, ok := helpSectNames[flag.usageCat]
		if !ok {
			arr := []*Flag{flag}
			helpSects = append(helpSects, arr)
			helpSectNames[flag.usageCat] = len(helpSects) - 1
			helpSectIndexes[len(helpSects)-1] = flag.usageCat
		} else {
			helpSects[index] = append(helpSects[index], flag)
		}
	}

	data, maxLen := fs.formatFlagHelp(helpSects)

	fmt.Printf(`%s

Usage:
  %s

`, fs.helpFirstLine, fs.basicUsage)

	for sect, sData := range data {
		fmt.Print(helpSectIndexes[sect], " options:\n")

		for i, fData := range sData {
			fmt.Print(fData, strings.Repeat(" ", maxLen-len(fData)+3), helpSects[sect][i].usage, "\n")
		}

		fmt.Print("\n")
	}
}

func (fs *Set) formatFlagHelp(helpSects [][]*Flag) (data [][]string, maxLen int) {
	maxLen = 0
	data = make([][]string, len(helpSects))
	for sect, flags := range helpSects {
		var sData = make([]string, len(flags))
		for i, flag := range flags {
			var fData = []string{"  "}

			for _, key := range flag.shortKeys {
				fData = append(fData, "-")
				fData = append(fData, key)
				fData = append(fData, ", ")
			}

			for _, key := range flag.longKeys {
				fData = append(fData, "--")
				fData = append(fData, key)
				if len(flag.usageValName) > 0 {
					fData = append(fData, "=")
					fData = append(fData, flag.usageValName)
				}
				fData = append(fData, ", ")
			}

			if fData[len(fData)-1] == ", " {
				fData = fData[:len(fData)-1]
			}

			sData[i] = strings.Join(fData, "")
			if len(sData[i]) > maxLen {
				maxLen = len(sData[i])
			}
		}
		data[sect] = sData
	}
	return
}

func (fs *Set) err(format string, args ...interface{}) error {
	if fs.ExitOnError {
		fmt.Fprintf(os.Stderr, format, args...)
		fmt.Fprint(os.Stderr, "\n")
		os.Exit(1)
		return nil
	}
	return fmt.Errorf(format, args...)
}

// Parse the input arguments in this flagset into mauflag form
// Before this function is called all the flags will have either the type default or the given default value
func (fs *Set) Parse() error {
	var flag *Flag
	var key string
	var noMoreFlags = false

	for _, arg := range fs.InputArgs {
		if noMoreFlags {
			fs.args = append(fs.args, arg)
		} else if arg == "--" && fs.DoubleLineEscape {
			noMoreFlags = true
		} else if flag != nil {
			err := flag.setValue(arg)
			if err != nil {
				return fs.err("Flag %s was not a %s", key, flag.Value.Name())
			}
			flag = nil
		} else if arg[0] == '-' && len(arg) > 1 {
			arg = strings.ToLower(arg)
			var err error
			flag, key, err = fs.flagFound(arg[1:])
			if err != nil {
				return err
			}
		} else {
			fs.args = append(fs.args, arg)
		}
	}
	return nil
}

func (fs *Set) flagFound(arg string) (flag *Flag, key string, err error) {
	key = arg
	var val string
	if strings.ContainsRune(key, '=') {
		val = key[strings.Index(key, "=")+1:]
		key = key[:strings.Index(key, "=")]
	}

	var rem string
	flag, key, rem = fs.getFlag(key)
	flag, key, rem, err = fs.handleFlag(flag, key, rem, val)

	if len(rem) > 0 {
		return fs.flagFound(rem)
	}

	return
}

func (fs *Set) handleFlag(flag *Flag, key, rem, val string) (*Flag, string, string, error) {
	var err error
	var isBool bool

	if flag != nil {
		_, isBool = flag.Value.(*boolValue)
	}

	if flag == nil {
		err = fs.err("Unknown flag: %s", key)
	} else if len(val) > 0 && len(rem) == 0 {
		flag.setValue(val)
	} else if isBool {
		flag.setValue("true")
	} else if len(rem) > 0 {
		flag.setValue(rem)
		rem = ""
	} else {
		return flag, key, rem, err
	}

	return nil, "", rem, err
}

func (fs *Set) getFlag(key string) (*Flag, string, string) {
	if key[0] == '-' {
		key = key[1:]
		for _, lflag := range fs.flags {
			for _, lkey := range lflag.longKeys {
				if lkey == key {
					return lflag, lkey, ""
				}
			}
		}
		return nil, key, ""
	}
	rem := key[1:]
	key = key[0:1]
	for _, lflag := range fs.flags {
		for _, lkey := range lflag.shortKeys {
			if lkey == key {
				return lflag, lkey, rem
			}
		}
	}
	return nil, key, rem
}
