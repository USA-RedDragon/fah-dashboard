package config

//go:generate go tool configulator -type Config

import (
	"errors"
	"fmt"
	"time"
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
	Database string   `name:"database" description:"Path to the SQLite database" default:"fah-dashboard.db"`
	Event    Event    `name:"event" description:"Competition settings"`
	Collect  Collect  `name:"collect" description:"Stats collection settings"`
}

type HTTP struct {
	Bind string `name:"bind" description:"Address to listen on. The default, [::], listens on all interfaces" default:"[::]"`
	Port int    `name:"port" description:"Port to listen on" default:"8080"`
}

type Event struct {
	Team      int    `name:"team" description:"Folding@home team ID" default:"223518"`
	Start     string `name:"start" description:"Event start time in RFC 3339 format" default:"2026-11-01T00:00:00Z"`
	End       string `name:"end" description:"Event end time in RFC 3339 format" default:"2026-12-07T00:00:00Z"`
	MinDays   int    `name:"min-days" description:"Active days needed for prizes and badges" default:"30"`
	MinPoints int64  `name:"min-points" description:"Points needed for prizes and badges" default:"1000000"`
	MinWUs    int64  `name:"min-wus" description:"Work units needed for prizes and badges" default:"50"`
}

type Collect struct {
	RosterURL      string  `name:"roster-url" description:"Comma-separated CSV export URLs of the sign-up sheet and the official daily stats sheet" default:"https://docs.google.com/spreadsheets/d/1aETM39IfUw1adcLsE51NTye8Kd37CIFTqq89jDShAVw/export?format=csv&gid=156779474,https://docs.google.com/spreadsheets/d/1D1mlywhhOAapgQFvMZ6rClPpiYFmQQeeZlmw5nBsB-Y/export?format=csv"`
	APIURL         string  `name:"api-url" description:"Root URL of the Folding@home stats API" default:"https://api.foldingathome.org"`
	PollSeconds    int     `name:"poll-seconds" description:"Seconds between checks for a new stats batch" default:"60"`
	MinSweepSecs   int     `name:"min-sweep-seconds" description:"Minimum seconds between reads of every donor" default:"300"`
	RosterSeconds  int     `name:"roster-seconds" description:"Seconds between reads of the sign-up sheet" default:"3600"`
	Aliases        string  `name:"aliases" description:"Comma-separated sign-up=donor pairs for names that differ on Folding@home"`
	RequestsPerSec float64 `name:"requests-per-second" description:"Maximum requests each second to the stats API" default:"1"`
}

var (
	ErrInvalidLogLevel = errors.New("invalid log level provided")
	ErrInvalidEvent    = errors.New("event end must follow a valid event start")
	ErrInvalidCollect  = errors.New("collection intervals and request rate must be positive")
)

func (c Config) Validate() error {
	if c.LogLevel != LogLevelDebug &&
		c.LogLevel != LogLevelInfo &&
		c.LogLevel != LogLevelWarn &&
		c.LogLevel != LogLevelError {
		return ErrInvalidLogLevel
	}

	start, end, err := c.Event.Window()
	if err != nil || !end.After(start) {
		return ErrInvalidEvent
	}

	if c.Collect.PollSeconds <= 0 || c.Collect.MinSweepSecs <= 0 ||
		c.Collect.RosterSeconds <= 0 || c.Collect.RequestsPerSec <= 0 {
		return ErrInvalidCollect
	}

	return nil
}

// Window returns the event start and end times.
func (e Event) Window() (time.Time, time.Time, error) {
	start, err := time.Parse(time.RFC3339, e.Start)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("parse event start: %w", err)
	}
	end, err := time.Parse(time.RFC3339, e.End)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("parse event end: %w", err)
	}
	return start, end, nil
}
