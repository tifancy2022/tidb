// Copyright 2022 PingCAP, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package ticdcutil

import (
	"context"
	"errors"

	"github.com/pingcap/tidb/types"
)

type EventType string

const (
	EventTypeInsert EventType = "insert"
	EventTypUpdate  EventType = "update"
	EventTypeDelete EventType = "delete"
)

type RowChangeEvent struct {
	Tp       EventType
	CommitTS uint64
	Columns  []types.Datum
	// PreColumns is the old value of the row, only available when EventType is update or delete.
	PreColumns []types.Datum
}

type Changefeed interface {
	// Next returns the next RowChangeEvent. If there is no new event, it will
	// be blocked until ctx is canceled or changefeed is finished or closed.
	// On changefeed finished or closed, it will return io.EOF.
	Next(ctx context.Context) (*RowChangeEvent, error)
	// Close closes the changefeed.
	Close() error
}

// NewChangefeed creates a new changefeed.
// Must import github.com/pingcap/tidb/util/ticdcutil/changefeed to initialize this function before using it.
var NewChangefeed = func(ctx context.Context, startTS uint64, dbName string, tableName string) (Changefeed, error) {
	return nil, errors.New("function NewChangeFeed is not initialized, please import github.com/pingcap/tidb/util/ticdcutil/changefeed to initialize it")
}
