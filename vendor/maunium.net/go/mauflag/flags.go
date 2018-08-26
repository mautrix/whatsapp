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

// Flag represents a single flag
type Flag struct {
	longKeys     []string
	shortKeys    []string
	Value        Value
	defaul       string
	usage        string
	usageCat     string
	usageValName string
}

// Make creates and registers a flag
func (fs *Set) Make() *Flag {
	flag := &Flag{}
	val := stringValue("")
	flag.Value = &val
	flag.usageCat = "Application"
	flag.activateDefaultValue()
	fs.flags = append(fs.flags, flag)
	return flag
}

// MakeKey creates and registers a flag with the given short and long keys
func (fs *Set) MakeKey(short, long string) *Flag {
	return fs.Make().Key(short, long)
}

// MakeFull creates and registers a flag with the given short and long keys, usage string and default value
func (fs *Set) MakeFull(short, long, usage, defVal string) *Flag {
	return fs.MakeKey(short, long).Usage(usage).Default(defVal)
}

func (flag *Flag) setValue(val string) error {
	return flag.Value.Set(val)
}

// Usage sets the usage of this Flag
func (flag *Flag) Usage(usage string) *Flag {
	flag.usage = usage
	return flag
}

// UsageCategory sets the category of this flag (e.g. Application or Help)
func (flag *Flag) UsageCategory(category string) *Flag {
	flag.usageCat = category
	return flag
}

// ValueName sets the value name in the usage page
func (flag *Flag) ValueName(valname string) *Flag {
	flag.usageValName = valname
	return flag
}

// Default sets the default value of this Flag
// The value given is passed to the Value container of this flag using `Set()`
func (flag *Flag) Default(defaul string) *Flag {
	flag.defaul = defaul
	flag.activateDefaultValue()
	return flag
}

func (flag *Flag) activateDefaultValue() {
	if len(flag.defaul) > 0 && flag.Value != nil {
		flag.Value.Set(flag.defaul)
	}
}

// LongKey adds a long key to this Flag
func (flag *Flag) LongKey(key string) *Flag {
	flag.longKeys = append(flag.longKeys, key)
	return flag
}

// ShortKey adds a short key to this Flag
func (flag *Flag) ShortKey(key string) *Flag {
	flag.shortKeys = append(flag.shortKeys, key)
	return flag
}

// Key adds a long and a short key to this Flag
func (flag *Flag) Key(short, long string) *Flag {
	return flag.ShortKey(short).LongKey(long)
}

// Custom sets a custom object that implemets Value as the value of this flag
func (flag *Flag) Custom(val Value) {
	flag.Value = val
	flag.activateDefaultValue()
}

// Bool sets the type of this flag to a boolean and returns a pointer to the value
func (flag *Flag) Bool() *bool {
	val := boolValue(false)
	flag.Value = &val
	flag.activateDefaultValue()
	return (*bool)(&val)
}

// String sets the type of this flag to a string and returns a pointer to the value
func (flag *Flag) String() *string {
	val := stringValue("")
	flag.Value = &val
	flag.activateDefaultValue()
	return (*string)(&val)
}

// StringMap sets the type of this flag to a string-string map and returns a pointer to the value
func (flag *Flag) StringMap() *map[string]string {
	val := ssMapValue{}
	flag.Value = &val
	flag.activateDefaultValue()
	return (*map[string]string)(&val)
}

// StringArray sets the type of this flag to a string array and returns a pointer to the value
func (flag *Flag) StringArray() *[]string {
	val := stringArrayValue{}
	flag.Value = &val
	flag.activateDefaultValue()
	return (*[]string)(&val)
}

// IntArray sets the type of this flag to a signed default-length integer array and returns a pointer to the value
func (flag *Flag) IntArray() *[]int {
	val := intArrayValue{}
	flag.Value = &val
	flag.activateDefaultValue()
	return (*[]int)(&val)
}

// Int64Array sets the type of this flag to a signed 64-bit integer array and returns a pointer to the value
func (flag *Flag) Int64Array() *[]int64 {
	val := int64ArrayValue{}
	flag.Value = &val
	flag.activateDefaultValue()
	return (*[]int64)(&val)
}

// Int sets the type of this flag to a signed default-length integer and returns a pointer to the value
func (flag *Flag) Int() *int {
	val := intValue(0)
	flag.Value = &val
	flag.activateDefaultValue()
	return (*int)(&val)
}

// Uint sets the type of this flag to an unsigned default-length integer and returns a pointer to the value
func (flag *Flag) Uint() *uint {
	val := uintValue(0)
	flag.Value = &val
	flag.activateDefaultValue()
	return (*uint)(&val)
}

// Int8 sets the type of this flag to a signed 8-bit integer and returns a pointer to the value
func (flag *Flag) Int8() *int8 {
	val := int8Value(0)
	flag.Value = &val
	flag.activateDefaultValue()
	return (*int8)(&val)
}

// Uint8 sets the type of this flag to an unsigned 8-bit integer and returns a pointer to the value
func (flag *Flag) Uint8() *uint8 {
	val := uint8Value(0)
	flag.Value = &val
	flag.activateDefaultValue()
	return (*uint8)(&val)
}

// Byte sets the type of this flag to a byte (unsigned 8-bit integer) and returns a pointer to the value
func (flag *Flag) Byte() *byte {
	val := byteValue(0)
	flag.Value = &val
	flag.activateDefaultValue()
	return (*byte)(&val)
}

// Int16 sets the type of this flag to a signed 16-bit integer and returns a pointer to the value
func (flag *Flag) Int16() *int16 {
	val := int16Value(0)
	flag.Value = &val
	flag.activateDefaultValue()
	return (*int16)(&val)
}

// Uint16 sets the type of this flag to an unsigned 16-bit integer and returns a pointer to the value
func (flag *Flag) Uint16() *uint16 {
	val := uint16Value(0)
	flag.Value = &val
	flag.activateDefaultValue()
	return (*uint16)(&val)
}

// Int32 sets the type of this flag to a signed 32-bit integer and returns a pointer to the value
func (flag *Flag) Int32() *int32 {
	val := int32Value(0)
	flag.Value = &val
	flag.activateDefaultValue()
	return (*int32)(&val)
}

// Rune sets the type of this flag to a rune (signed 32-bit integer) and returns a pointer to the value
func (flag *Flag) Rune() *rune {
	val := runeValue(0)
	flag.Value = &val
	flag.activateDefaultValue()
	return (*rune)(&val)
}

// Uint32 sets the type of this flag to an unsigned 32-bit integer and returns a pointer to the value
func (flag *Flag) Uint32() *uint32 {
	val := uint32Value(0)
	flag.Value = &val
	flag.activateDefaultValue()
	return (*uint32)(&val)
}

// Int64 sets the type of this flag to a signed 64-bit integer and returns a pointer to the value
func (flag *Flag) Int64() *int64 {
	val := int64Value(0)
	flag.Value = &val
	flag.activateDefaultValue()
	return (*int64)(&val)
}

// Uint64 sets the type of this flag to an unsigned 64-bit integer and returns a pointer to the value
func (flag *Flag) Uint64() *uint64 {
	val := uint64Value(0)
	flag.Value = &val
	flag.activateDefaultValue()
	return (*uint64)(&val)
}

// Float32 sets the type of this flag to an 32-bit float and returns a pointer to the value
func (flag *Flag) Float32() *float32 {
	val := float32Value(0)
	flag.Value = &val
	flag.activateDefaultValue()
	return (*float32)(&val)
}

// Float64 sets the type of this flag to an 64-bit float and returns a pointer to the value
func (flag *Flag) Float64() *float64 {
	val := float64Value(0)
	flag.Value = &val
	flag.activateDefaultValue()
	return (*float64)(&val)
}
