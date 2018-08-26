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
	"os"
)

var defaultSet = New(os.Args[1:])

// DefaultSet returns the default flagset which takes its arguments from os.Args
func DefaultSet() *Set {
	return defaultSet
}

// Make creates and registers a flag
func Make() *Flag {
	return DefaultSet().Make()
}

// MakeKey creates and registers a flag with the given short and long keys
func MakeKey(short, long string) *Flag {
	return DefaultSet().MakeKey(short, long)
}

// MakeFull creates and registers a flag with the given short and long keys, usage string and default value
func MakeFull(short, long, usage, defVal string) *Flag {
	return DefaultSet().MakeFull(short, long, usage, defVal)
}

// Parse the command line arguments into mauflag form
func Parse() error {
	return DefaultSet().Parse()
}

// Args returns the arguments that weren't associated with any flag
func Args() []string {
	return DefaultSet().Args()
}

// Arg returns the string at the given index from the list Args() returns
// If the index does not exist, Arg will return an empty string.
func Arg(i int) string {
	return DefaultSet().Arg(i)
}

// NArg returns the number of arguments not associated with any flags
func NArg() int {
	return len(DefaultSet().args)
}

// MakeHelpFlag creates the -h, --help flag
func MakeHelpFlag() (*bool, *Flag) {
	return DefaultSet().MakeHelpFlag()
}

// CheckHelpFlag checks if the help flag is set and prints the help page if needed.
// Return value tells whether or not the help page was printed
func CheckHelpFlag() bool {
	return DefaultSet().CheckHelpFlag()
}

// SetHelpTitles sets the first line (program name and basic explanation) and basic usage specification
func SetHelpTitles(firstLine, basicUsage string) {
	DefaultSet().SetHelpTitles(firstLine, basicUsage)
}

// PrintHelp prints the help page
func PrintHelp() {
	DefaultSet().PrintHelp()
}
