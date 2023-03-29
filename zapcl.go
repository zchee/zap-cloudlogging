// Copyright 2022 The zapcl Authors
// SPDX-License-Identifier: BSD-3-Clause

// Package zapcloudlogging provides the Cloud Logging integration for Zap.
package zapcl

import (
	"errors"
	"fmt"
	"io"
	"sort"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"golang.org/x/sys/unix"
	logtypepb "google.golang.org/genproto/googleapis/logging/type"

	"github.com/zchee/zapcl/internal/json"
	"github.com/zchee/zapcl/pkg/monitoredresource"
)

var levelToSeverity = map[zapcore.Level]logtypepb.LogSeverity{
	zapcore.DebugLevel:  logtypepb.LogSeverity_DEBUG,
	zapcore.InfoLevel:   logtypepb.LogSeverity_INFO,
	zapcore.WarnLevel:   logtypepb.LogSeverity_WARNING,
	zapcore.ErrorLevel:  logtypepb.LogSeverity_ERROR,
	zapcore.DPanicLevel: logtypepb.LogSeverity_CRITICAL,
	zapcore.PanicLevel:  logtypepb.LogSeverity_ALERT,
	zapcore.FatalLevel:  logtypepb.LogSeverity_EMERGENCY,
}

// NewEncoderConfig returns the logging configuration.
func NewEncoderConfig() zapcore.EncoderConfig {
	return zapcore.EncoderConfig{
		TimeKey:             "time", // https://cloud.google.com/logging/docs/agent/logging/configuration#timestamp-processing
		LevelKey:            "severity",
		NameKey:             "logger",
		CallerKey:           "caller",
		MessageKey:          "message",
		StacktraceKey:       "stacktrace",
		LineEnding:          zapcore.DefaultLineEnding,
		EncodeLevel:         levelEncoder,
		EncodeTime:          zapcore.RFC3339NanoTimeEncoder,
		EncodeDuration:      zapcore.SecondsDurationEncoder,
		EncodeCaller:        zapcore.ShortCallerEncoder,
		NewReflectedEncoder: json.NewEncoder,
	}
}

func levelEncoder(lvl zapcore.Level, enc zapcore.PrimitiveArrayEncoder) {
	enc.AppendString(levelToSeverity[lvl].Enum().String())
}

type nopWriteSyncer struct {
	io.Writer
}

func (nopWriteSyncer) Sync() error { return nil }

// Core represents a zapcor.Core that is Cloud Logging integration for Zap logger.
type Core struct {
	zapcore.LevelEnabler

	enc        zapcore.Encoder
	ws         zapcore.WriteSyncer
	fields     []zapcore.Field
	initFields map[string]interface{}
}

var _ zapcore.Core = (*Core)(nil)

func (c *Core) clone() *Core {
	newCore := &Core{
		fields: make([]zapcore.Field, len(c.fields)),
		enc:    c.enc.Clone(),
		ws:     c.ws,
	}
	copy(newCore.fields, c.fields)

	return newCore
}

func addFields(enc zapcore.ObjectEncoder, fields []zapcore.Field) {
	for i := range fields {
		fields[i].AddTo(enc)
	}
}

// With adds structured context to the Core.
//
// With implements zapcore.Core.With.
func (c *Core) With(fields []zapcore.Field) zapcore.Core {
	clone := c.clone()
	addFields(clone.enc, fields)

	return clone
}

// Check determines whether the supplied Entry should be logged (using the
// embedded LevelEnabler and possibly some extra logic). If the entry
// should be logged, the Core adds itself to the CheckedEntry and returns
// the result.
//
// Check implements zapcore.Core.Check.
func (c *Core) Check(ent zapcore.Entry, ce *zapcore.CheckedEntry) *zapcore.CheckedEntry {
	if c.Enabled(ent.Level) {
		return ce.AddCore(ent, c)
	}

	return ce
}

// Write serializes the Entry and any Fields supplied at the log site and
// writes them to their destination.
//
// Write implemenns zapcore.Core.Write.
func (c *Core) Write(ent zapcore.Entry, fields []zapcore.Field) error {
	for _, field := range c.fields {
		field.AddTo(c.enc)
	}

	buf, err := c.enc.EncodeEntry(ent, fields)
	if err != nil {
		return fmt.Errorf("could not encode entry: %w", err)
	}

	_, err = c.ws.Write(buf.Bytes())
	buf.Free()
	if err != nil {
		return fmt.Errorf("could not write buf: %w", err)
	}

	if ent.Level > zapcore.ErrorLevel {
		// Since we may be crashing the program, sync the output. Ignore Sync
		// errors, pending a clean solution to issue #370.
		c.Sync() //nolint:errcheck
	}

	return nil
}

// Sync flushes buffered logs if any.
//
// Sync implemenns zapcore.Core.Sync.
func (c *Core) Sync() error {
	if err := c.ws.Sync(); err != nil {
		if !knownSyncError(err) {
			return fmt.Errorf("faild to sync logger: %w", err)
		}
	}

	return nil
}

// knownSyncError reports whether the given error is one of the known
// non-actionable errors returned by Sync on Linux and macOS.
//
// Linux:
// - sync /dev/stdout: invalid argument
//
// macOS:
// - sync /dev/stdout: inappropriate ioctl for device
//
// This code was borrowed from:
// - https://github.com/open-telemetry/opentelemetry-collector/blob/v0.46.0/exporter/loggingexporter/known_sync_error.go#L24-L39.
func knownSyncError(err error) bool {
	switch {
	case errors.Is(err, unix.EINVAL),
		errors.Is(err, unix.ENOTSUP),
		errors.Is(err, unix.ENOTTY),
		errors.Is(err, unix.EBADF):

		return true
	}

	return false
}

// Option configures a core.
type Option interface {
	apply(*Core)
}

// optionFunc wraps a func so it satisfies the Option interface.
type optionFunc func(*Core)

func (f optionFunc) apply(c *Core) {
	f(c)
}

// WithInitialFields configures the zap InitialFields.
func WithInitialFields(fields map[string]interface{}) Option {
	return optionFunc(func(c *Core) {
		c.initFields = fields
	})
}

// WithWriteSyncer configures the zapcore.WriteSyncer.
func WithWriteSyncer(ws zapcore.WriteSyncer) Option {
	return optionFunc(func(c *Core) {
		c.ws = ws
	})
}

func newCore(ws zapcore.WriteSyncer, enab zapcore.LevelEnabler, opts ...Option) *Core {
	core := &Core{
		LevelEnabler: enab,
		enc:          zapcore.NewJSONEncoder(NewEncoderConfig()),
		ws:           ws,
	}
	for _, opt := range opts {
		opt.apply(core)
	}

	res := monitoredresource.Detect()
	core.fields = []zapcore.Field{
		zap.String(res.Type, res.LogID),
		zap.Inline(res),
	}

	// handling initFields option
	if len(core.initFields) > 0 {
		fs := make([]zapcore.Field, 0, len(core.initFields))
		keys := make([]string, 0, len(core.initFields))
		for k := range core.initFields {
			keys = append(keys, k)
		}
		sort.Strings(keys)

		for _, k := range keys {
			fs = append(fs, zap.Any(k, core.initFields[k]))
		}
		core.fields = append(core.fields, fs...)
	}

	return core
}

// NewCore creates a Core that writes logs to a WriteSyncer.
func NewCore(ws zapcore.WriteSyncer, enab zapcore.LevelEnabler, opts ...Option) zapcore.Core {
	core := newCore(ws, enab, opts...)

	return zapcore.NewCore(core.enc, core.ws, core.LevelEnabler)
}

// WrapCore wraps or replaces the Logger's underlying zapcore.Core.
func WrapCore(opts ...Option) zap.Option {
	return zap.WrapCore(func(c zapcore.Core) zapcore.Core {
		core := newCore(nopWriteSyncer{}, c, opts...)

		return zapcore.NewCore(core.enc, core.ws, core.LevelEnabler)
	})
}
