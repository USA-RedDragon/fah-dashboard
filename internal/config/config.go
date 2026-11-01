package config

//go:generate go tool configulator -type Config

import (
	"errors"
)

type LogLevel string

const (
	LogLevelDebug LogLevel = "debug"
	LogLevelInfo  LogLevel = "info"
	LogLevelWarn  LogLevel = "warn"
	LogLevelError LogLevel = "error"
)

type Config struct {
	LogLevel LogLevel `name:"log-level" description:"Logging level for the application. One of debug, info, warn, or error" default:"info"`
	HTTP     HTTP     `name:"http" description:"HTTP server configuration"`
}

type HTTP struct {
	Bind string `name:"bind" description:"Address to listen on. The default, [::], listens on all interfaces" default:"[::]"`
	Port int    `name:"port" description:"Port to listen on" default:"8080"`
}

var (
	ErrInvalidLogLevel = errors.New("invalid log level provided")
)

func (c Config) Validate() error {
	if c.LogLevel != LogLevelDebug &&
		c.LogLevel != LogLevelInfo &&
		c.LogLevel != LogLevelWarn &&
		c.LogLevel != LogLevelError {
		return ErrInvalidLogLevel
	}

	return nil
}
