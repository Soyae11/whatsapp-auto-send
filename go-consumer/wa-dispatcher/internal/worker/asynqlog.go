package worker

import (
	"fmt"
	"log/slog"
)

type asynqLogger struct {
	log *slog.Logger
}

func (l *asynqLogger) Debug(args ...any) { l.log.Debug(fmt.Sprint(args...)) }
func (l *asynqLogger) Info(args ...any)  { l.log.Info(fmt.Sprint(args...)) }
func (l *asynqLogger) Warn(args ...any)  { l.log.Warn(fmt.Sprint(args...)) }
func (l *asynqLogger) Error(args ...any) { l.log.Error(fmt.Sprint(args...)) }
func (l *asynqLogger) Fatal(args ...any) {
	l.log.Error(fmt.Sprint(args...), "asynq_level", "fatal")
}
