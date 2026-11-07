package app

import (
	"fmt"

	"github.com/rs/zerolog"
)

// zerologLogger adapts DysonFS's zerolog to the shared discovery registration
// logger, so the register/renew loop exists once for the whole fleet.
type zerologLogger struct {
	log zerolog.Logger
}

func (l zerologLogger) Info(msg string, kv ...any) { l.emit(l.log.Info(), msg, kv) }
func (l zerologLogger) Warn(msg string, kv ...any) { l.emit(l.log.Warn(), msg, kv) }

func (l zerologLogger) emit(event *zerolog.Event, msg string, kv []any) {
	fields := make(map[string]any, len(kv)/2)
	for i := 0; i+1 < len(kv); i += 2 {
		fields[fmt.Sprint(kv[i])] = kv[i+1]
	}
	event.Fields(fields).Msg(msg)
}
