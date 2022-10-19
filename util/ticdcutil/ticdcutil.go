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
	"crypto/rand"
	"fmt"
	"io"
	"time"

	"github.com/ngaut/log"
	"github.com/pingcap/tidb/types"
	apiv2 "github.com/pingcap/tiflow/cdc/api/v2"
	"github.com/pingcap/tiflow/cdc/model"
	"github.com/pingcap/tiflow/cdc/sinkv2/eventsink/memory"
	apiv1client "github.com/pingcap/tiflow/pkg/api/v1"
	apiv2client "github.com/pingcap/tiflow/pkg/api/v2"
	"go.uber.org/zap"
)

const serverAddr = "127.0.0.1:8300"

type EventType string

const (
	EventTypeInsert EventType = "insert"
	EventTypUpdate  EventType = "update"
	EventTypeDelete EventType = "delete"
)

type RowChangeEvent struct {
	Tp         EventType
	CommitTS   uint64
	Columns    []types.Datum
	PreColumns []types.Datum
}

type Changefeed struct {
	id      string
	eventCh <-chan *model.RowChangedEvent
}

func NewChangfeed(
	ctx context.Context,
	startTS uint64,
	dbName string,
	tableName string,
) (*Changefeed, error) {
	apiv2Client, err := apiv2client.NewAPIClient(serverAddr, nil)
	if err != nil {
		return nil, err
	}

	changefeedID := generateChangefeedID(dbName, tableName)
	changefeedConfig := &apiv2.ChangefeedConfig{
		ID:      changefeedID,
		SinkURI: memory.MakeSinkURI(changefeedID),
		StartTs: startTS,
		ReplicaConfig: &apiv2.ReplicaConfig{
			Filter: &apiv2.FilterConfig{
				Rules: []string{fmt.Sprintf("%s.%s", dbName, tableName)},
			},
		},
	}

	if _, err := apiv2Client.Changefeeds().Create(ctx, changefeedConfig); err != nil {
		return nil, err
	}

	// Wait for the memory sink to be ready.
	var eventCh <-chan *model.RowChangedEvent
loop:
	for {
		select {
		case <-ctx.Done():
			if err := removeChangefeed(context.Background(), changefeedID); err != nil {
				log.Warn("failed to remove changefeed, please remove the changefeed manually", zap.String("id", changefeedID), zap.Error(err))
			}
			return nil, ctx.Err()
		case <-time.After(time.Millisecond):
			var ok bool
			eventCh, ok = memory.ConsumeEvents(changefeedID)
			if ok {
				break loop
			}
		}
	}
	return &Changefeed{
		id:      changefeedID,
		eventCh: eventCh,
	}, nil

}

// Next returns the next RowChangeEvent. If there is no new event, it will
// be blocked until ctx is canceled or changefeed is finished or closed.
func (c *Changefeed) Next(ctx context.Context) (*RowChangeEvent, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case event, ok := <-c.eventCh:
		if !ok {
			return nil, io.EOF
		}
		switch {
		case event.IsInsert():
			return &RowChangeEvent{
				Tp:       EventTypeInsert,
				CommitTS: event.CommitTs,
			}, nil
		case event.IsUpdate():
			return &RowChangeEvent{
				Tp:       EventTypUpdate,
				CommitTS: event.CommitTs,
			}, nil
		case event.IsDelete():
			return &RowChangeEvent{
				Tp:       EventTypeDelete,
				CommitTS: event.CommitTs,
			}, nil
		default:
			panic(fmt.Sprintf("cannot infer event type, event: %+v", event))
		}
	}
}

// Close closes the changefeed.
func (c *Changefeed) Close() error {
	return removeChangefeed(context.Background(), c.id)
}

func generateChangefeedID(dbName, tableName string) string {
	var buf [4]byte
	if _, err := rand.Read(buf[:]); err != nil {
		panic(err)
	}
	return fmt.Sprintf("changefeed-%s-%s-%x", dbName, tableName, buf)
}

func removeChangefeed(ctx context.Context, id string) error {
	apiv1Client, err := apiv1client.NewAPIClient(serverAddr, nil)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 10)
	defer cancel()
	return apiv1Client.Changefeeds().Delete(ctx, id)
}
