// Copyright 2022 The zapcl Authors
// SPDX-License-Identifier: BSD-3-Clause

package zapcl

import (
	"cloud.google.com/go/logging/apiv2/loggingpb"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

const (
	// OperationKey is the value of this field is also used by the Logs Explorer to group related log entries.
	//
	// operation field:
	// - https://cloud.google.com/logging/docs/reference/v2/rest/v2/LogEntry#FIELDS.operation
	// - https://cloud.google.com/logging/docs/structured-logging#special-payload-fields
	OperationKey = "logging.googleapis.com/operation"
)

// operation is the payload of Cloud Logging operation field.
type operation struct {
	*loggingpb.LogEntryOperation
}

var _ zapcore.ObjectMarshaler = (*operation)(nil)

// MarshalLogObject implements zapcore.ObjectMarshaller.MarshalLogObject.
func (op operation) MarshalLogObject(enc zapcore.ObjectEncoder) error {
	enc.AddString("id", op.GetId())
	enc.AddString("producer", op.GetProducer())
	enc.AddBool("first", op.GetFirst())
	enc.AddBool("last", op.GetLast())

	return nil
}

// Operation adds the Cloud Logging "operation" fields from args.
//
// operation: https://cloud.google.com/logging/docs/reference/v2/rest/v2/LogEntry#LogEntryOperation
func Operation(id, producer string, first, last bool) zapcore.Field {
	op := &operation{
		LogEntryOperation: &loggingpb.LogEntryOperation{
			Id:       id,
			Producer: producer,
			First:    first,
			Last:     last,
		},
	}

	return zap.Object(OperationKey, op)
}

// OperationStart is a convenience function for `Operation`.
//
// It should be called for the first operation log.
func OperationStart(id, producer string) zapcore.Field {
	return Operation(id, producer, true, false)
}

// OperationCont is a convenience function for `Operation`.
//
// It should be called for any non-start/end operation log.
func OperationCont(id, producer string) zapcore.Field {
	return Operation(id, producer, false, false)
}

// OperationEnd is a convenience function for `Operation`.
//
// It should be called for the last operation log.
func OperationEnd(id, producer string) zapcore.Field {
	return Operation(id, producer, false, true)
}
