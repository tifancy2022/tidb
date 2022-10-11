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
	"fmt"
	"time"

	"github.com/pingcap/log"
	apiv2 "github.com/pingcap/tiflow/cdc/api/v2"
	"github.com/pingcap/tiflow/cdc/model"
	"github.com/pingcap/tiflow/cdc/sinkv2/eventsink/memory"
	apiv1client "github.com/pingcap/tiflow/pkg/api/v1"
	apiv2client "github.com/pingcap/tiflow/pkg/api/v2"
	"go.uber.org/zap"
)

const serverAddr = "127.0.0.1:8300"

func CreateChangfeed(
	ctx context.Context,
	id string,
	startTS uint64,
	dbName string,
	tableName string,
) (<-chan *model.RowChangedEvent, error) {
	apiv2Client, err := apiv2client.NewAPIClient(serverAddr, nil)
	if err != nil {
		return nil, err
	}

	changefeedConfig := &apiv2.ChangefeedConfig{
		ID:      id,
		SinkURI: memory.MakeSinkURI(id),
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
	for {
		select {
		case <-ctx.Done():
			if err := RemoveChangefeed(context.Background(), id); err != nil {
				log.Warn("failed to remove changefeed, please remove the changefeed manually", zap.String("id", id), zap.Error(err))
			}
			return nil, ctx.Err()
		case <-time.After(time.Millisecond):
			eventCh, ok := memory.ConsumeEvents(id)
			if ok {
				return eventCh, nil
			}
		}
	}
}

func RemoveChangefeed(ctx context.Context, id string) error {
	apiv1Client, err := apiv1client.NewAPIClient(serverAddr, nil)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 10)
	defer cancel()
	return apiv1Client.Changefeeds().Delete(ctx, id)
}
