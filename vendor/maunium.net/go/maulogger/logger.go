// mauLogger - A logger for Go programs
// Copyright (C) 2016-2018 Tulir Asokan
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU General Public License for more details.
//
// You should have received a copy of the GNU General Public License
// along with this program.  If not, see <http://www.gnu.org/licenses/>.

package maulogger

import (
	"fmt"
	"os"
	"time"
)

// LoggerFileFormat ...
type LoggerFileFormat func(now string, i int) string

type BasicLogger struct {
	PrintLevel         int
	FlushLineThreshold int
	FileTimeFormat     string
	FileFormat         LoggerFileFormat
	TimeFormat         string
	FileMode           os.FileMode
	DefaultSub         Logger

	writer        *os.File
	lines         int
	prefixPrinted bool
}

// Logger contains advanced logging functions and also implements io.Writer
type Logger interface {
	Sub(module string) Logger
	GetParent() Logger

	Write(p []byte) (n int, err error)

	Log(level Level, parts ...interface{})
	Logln(level Level, parts ...interface{})
	Logf(level Level, message string, args ...interface{})
	Logfln(level Level, message string, args ...interface{})

	Debug(parts ...interface{})
	Debugln(parts ...interface{})
	Debugf(message string, args ...interface{})
	Debugfln(message string, args ...interface{})
	Info(parts ...interface{})
	Infoln(parts ...interface{})
	Infof(message string, args ...interface{})
	Infofln(message string, args ...interface{})
	Warn(parts ...interface{})
	Warnln(parts ...interface{})
	Warnf(message string, args ...interface{})
	Warnfln(message string, args ...interface{})
	Error(parts ...interface{})
	Errorln(parts ...interface{})
	Errorf(message string, args ...interface{})
	Errorfln(message string, args ...interface{})
	Fatal(parts ...interface{})
	Fatalln(parts ...interface{})
	Fatalf(message string, args ...interface{})
	Fatalfln(message string, args ...interface{})
}

// Create a Logger
func Create() Logger {
	var log = &BasicLogger{
		PrintLevel:         10,
		FileTimeFormat:     "2006-01-02",
		FileFormat:         func(now string, i int) string { return fmt.Sprintf("%[1]s-%02[2]d.log", now, i) },
		TimeFormat:         "15:04:05 02.01.2006",
		FileMode:           0600,
		FlushLineThreshold: 5,
		lines:              0,
	}
	log.DefaultSub = log.Sub("")
	return log
}

func (log *BasicLogger) GetParent() Logger {
	return nil
}

// SetWriter formats the given parts with fmt.Sprint and logs the result with the SetWriter level
func (log *BasicLogger) SetWriter(w *os.File) {
	log.writer = w
}

// OpenFile formats the given parts with fmt.Sprint and logs the result with the OpenFile level
func (log *BasicLogger) OpenFile() error {
	now := time.Now().Format(log.FileTimeFormat)
	i := 1
	for ; ; i++ {
		if _, err := os.Stat(log.FileFormat(now, i)); os.IsNotExist(err) {
			break
		} else if i == 99 {
			i = 1
			break
		}
	}
	var err error
	log.writer, err = os.OpenFile(log.FileFormat(now, i), os.O_WRONLY|os.O_CREATE|os.O_APPEND, log.FileMode)
	if err != nil {
		return err
	} else if log.writer == nil {
		return os.ErrInvalid
	}
	return nil
}

// Close formats the given parts with fmt.Sprint and logs the result with the Close level
func (log *BasicLogger) Close() {
	if log.writer != nil {
		log.writer.Close()
	}
}

// Raw formats the given parts with fmt.Sprint and logs the result with the Raw level
func (log *BasicLogger) Raw(level Level, module, message string) {
	if !log.prefixPrinted {
		if len(module) == 0 {
			message = fmt.Sprintf("[%s] [%s] %s", time.Now().Format(log.TimeFormat), level.Name, message)
		} else {
			message = fmt.Sprintf("[%s] [%s/%s] %s", time.Now().Format(log.TimeFormat), module, level.Name, message)
		}
	}

	log.prefixPrinted = message[len(message)-1] != '\n'

	if log.writer != nil {
		_, err := log.writer.WriteString(message)
		if err != nil {
			fmt.Println("Failed to write to log file:", err)
		}
	}

	if level.Severity >= log.PrintLevel {
		if level.Severity >= LevelError.Severity {
			os.Stderr.Write(level.GetColor())
			os.Stderr.WriteString(message)
			os.Stderr.Write(level.GetReset())
		} else {
			os.Stdout.Write(level.GetColor())
			os.Stdout.WriteString(message)
			os.Stdout.Write(level.GetReset())
		}
	}
}
