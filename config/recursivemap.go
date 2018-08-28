// mautrix-whatsapp - A Matrix-WhatsApp puppeting bridge.
// Copyright (C) 2018 Tulir Asokan
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.

package config

import (
	"strings"
)

type RecursiveMap map[interface{}]interface{}

func (rm RecursiveMap) GetDefault(path string, defVal interface{}) interface{} {
	val, ok := rm.Get(path)
	if !ok {
		return defVal
	}
	return val
}

func (rm RecursiveMap) GetMap(path string) RecursiveMap {
	val := rm.GetDefault(path, nil)
	if val == nil {
		return nil
	}

	newRM, ok := val.(map[interface{}]interface{})
	if ok {
		return RecursiveMap(newRM)
	}
	return nil
}

func (rm RecursiveMap) Get(path string) (interface{}, bool) {
	if index := strings.IndexRune(path, '.'); index >= 0 {
		key := path[:index]
		path = path[index+1:]

		submap := rm.GetMap(key)
		if submap == nil {
			return nil, false
		}
		return submap.Get(path)
	}
	val, ok := rm[path]
	return val, ok
}

func (rm RecursiveMap) GetIntDefault(path string, defVal int) int {
	val, ok := rm.GetInt(path)
	if !ok {
		return defVal
	}
	return val
}

func (rm RecursiveMap) GetInt(path string) (int, bool) {
	val, ok := rm.Get(path)
	if !ok {
		return 0, ok
	}
	intVal, ok := val.(int)
	return intVal, ok
}

func (rm RecursiveMap) GetStringDefault(path string, defVal string) string {
	val, ok := rm.GetString(path)
	if !ok {
		return defVal
	}
	return val
}

func (rm RecursiveMap) GetString(path string) (string, bool) {
	val, ok := rm.Get(path)
	if !ok {
		return "", ok
	}
	strVal, ok := val.(string)
	return strVal, ok
}

func (rm RecursiveMap) Set(path string, value interface{}) {
	if index := strings.IndexRune(path, '.'); index >= 0 {
		key := path[:index]
		path = path[index+1:]
		nextRM := rm.GetMap(key)
		if nextRM == nil {
			nextRM = make(RecursiveMap)
			rm[key] = nextRM
		}
		nextRM.Set(path, value)
		return
	}
	rm[path] = value
}

func (rm RecursiveMap) CopyFrom(otherRM RecursiveMap, path string) {
	val, ok := otherRM.Get(path)
	if ok {
		rm.Set(path, val)
	}
}