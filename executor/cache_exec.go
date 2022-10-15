package executor

import (
	"context"

	"github.com/pingcap/tidb/table"
	"github.com/pingcap/tidb/util/chunk"
	"github.com/pingcap/tidb/util/ranger"
	"github.com/pingcap/tipb/go-tipb"
)

type IncrementTableReaderExecutor struct {
	baseExecutor

	table table.Table

	ranges  []*ranger.Range
	dagPB   *tipb.DAGRequest
	startTS uint64

	// sinker interface{}
}

func NewTableSinkerExecutor(src *TableReaderExecutor) *IncrementTableReaderExecutor {
	return &IncrementTableReaderExecutor{
		baseExecutor: newBaseExecutor(src.ctx, src.schema, src.id, src.children...),
		table:        src.table,
		ranges:       src.ranges,
		dagPB:        src.dagPB,
		startTS:      src.startTS,
	}
}

func (e *IncrementTableReaderExecutor) Next(ctx context.Context, req *chunk.Chunk) error {
	req.Reset()
	return nil
}
