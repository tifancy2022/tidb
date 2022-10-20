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

package changefeed

import (
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"time"

	"github.com/ngaut/log"
	"github.com/pingcap/tidb/sessionctx/stmtctx"
	"github.com/pingcap/tidb/types"
	"github.com/pingcap/tidb/util/ticdcutil"
	apiv2 "github.com/pingcap/tiflow/cdc/api/v2"
	"github.com/pingcap/tiflow/cdc/model"
	"github.com/pingcap/tiflow/cdc/sinkv2/eventsink/memory"
	apiv1client "github.com/pingcap/tiflow/pkg/api/v1"
	apiv2client "github.com/pingcap/tiflow/pkg/api/v2"
	"go.uber.org/zap"
)

const serverAddr = "127.0.0.1:8300"

func init() {
	ticdcutil.NewChangefeed = newChangfeed
}

type changefeed struct {
	id      string
	eventCh <-chan *model.RowChangedEvent
}

func newChangfeed(
	ctx context.Context,
	startTS uint64,
	dbName string,
	tableName string,
) (ticdcutil.Changefeed, error) {
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
			EnableOldValue: true,
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
	return &changefeed{
		id:      changefeedID,
		eventCh: eventCh,
	}, nil

}

func (c *changefeed) Next(ctx context.Context) (*ticdcutil.RowChangeEvent, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case event, ok := <-c.eventCh:
		if !ok {
			return nil, io.EOF
		}
		switch {
		case event.IsInsert():
			result := &ticdcutil.RowChangeEvent{
				Tp:       ticdcutil.EventTypeInsert,
				CommitTS: event.CommitTs,
			}
			for i := 0; i < len(event.Columns); i++ {
				datum, err := column2Datum(event.ColInfos[i].Ft, event.Columns[i])
				if err != nil {
					return nil, err
				}
				result.Columns = append(result.Columns, datum)
			}
			return result, nil
		case event.IsUpdate():
			result := &ticdcutil.RowChangeEvent{
				Tp:       ticdcutil.EventTypUpdate,
				CommitTS: event.CommitTs,
			}
			for i := 0; i < len(event.Columns); i++ {
				datum, err := column2Datum(event.ColInfos[i].Ft, event.Columns[i])
				if err != nil {
					return nil, err
				}
				result.Columns = append(result.Columns, datum)
			}
			for i := 0; i < len(event.PreColumns); i++ {
				datum, err := column2Datum(event.ColInfos[i].Ft, event.PreColumns[i])
				if err != nil {
					return nil, err
				}
				result.PreColumns = append(result.PreColumns, datum)
			}
			return result, nil
		case event.IsDelete():
			result := &ticdcutil.RowChangeEvent{
				Tp:       ticdcutil.EventTypeDelete,
				CommitTS: event.CommitTs,
			}
			for i := 0; i < len(event.PreColumns); i++ {
				datum, err := column2Datum(event.ColInfos[i].Ft, event.PreColumns[i])
				if err != nil {
					return nil, err
				}
				result.PreColumns = append(result.PreColumns, datum)
			}
			return result, nil
		default:
			panic(fmt.Sprintf("cannot infer event type, event: %+v", event))
		}
	default:
		return nil, nil
	}
}

// Close closes the changefeed.
func (c *changefeed) Close() error {
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

func column2Datum(ft *types.FieldType, column *model.Column) (types.Datum, error) {
	sc := &stmtctx.StatementContext{TimeZone: time.UTC}
	rawDatum := types.NewStringDatum(model.ColumnValueString(column.Value))
	return rawDatum.ConvertTo(sc, ft)
}
