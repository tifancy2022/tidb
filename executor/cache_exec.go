package executor

import (
	"context"
	"github.com/pingcap/kvproto/pkg/coprocessor"
	"github.com/pingcap/tidb/parser/model"
	"github.com/pingcap/tidb/sessionctx"
	"github.com/pingcap/tidb/sessiontxn"
	"github.com/pingcap/tidb/types"
	"sync"

	"github.com/pingcap/tidb/table"
	"github.com/pingcap/tidb/util/chunk"
	"github.com/pingcap/tidb/util/ranger"
	"github.com/pingcap/tipb/go-tipb"
)

var StmtCacheExecManager = newStmtCacheExecutorManager()

type StmtCacheExecutorManager struct {
	sync.Mutex
	cacheRs map[string]*CacheStmtRecordSet
}

type CacheStmtRecordSet struct {
	*recordSet
}

func newStmtCacheExecutorManager() *StmtCacheExecutorManager {
	return &StmtCacheExecutorManager{
		cacheRs: make(map[string]*CacheStmtRecordSet),
	}
}

func (rs *CacheStmtRecordSet) Close() error {
	return nil
}

func (sc *StmtCacheExecutorManager) addStmtCacheExecutor(digest []byte, e Executor, rs *recordSet) error {
	exec, err := sc.replaceTableReader(e)
	if err != nil {
		return err
	}
	err = exec.Reset()
	if err != nil {
		return err
	}
	rs.executor = exec

	sc.Lock()
	defer sc.Unlock()
	sc.cacheRs[string(digest)] = &CacheStmtRecordSet{rs}
	return nil
}

func (sc *StmtCacheExecutorManager) GetStmtCacheExecutorByDigest(digest string) (*CacheStmtRecordSet, error) {
	sc.Lock()
	defer sc.Unlock()
	rs := sc.cacheRs[digest]
	if rs == nil {
		return nil, nil
	}
	err := rs.Reset()
	return rs, err
}

func (sc *StmtCacheExecutorManager) GetAllStmtCacheExecutor() map[string]*CacheStmtRecordSet {
	return sc.cacheRs
}

func (sc *StmtCacheExecutorManager) replaceTableReader(e Executor) (Executor, error) {
	switch v := e.(type) {
	case *TableReaderExecutor:
		return BuildTableSinkerExecutor(v)
	}
	for i, child := range e.base().children {
		exec, err := sc.replaceTableReader(child)
		if err != nil {
			return nil, err
		}
		e.base().children[i] = exec
	}
	return e, nil
}

type IncrementTableReaderExecutor struct {
	baseExecutor

	table table.Table

	ranges  []*ranger.Range
	dagPB   *tipb.DAGRequest
	startTS uint64
}

func BuildTableSinkerExecutor(src *TableReaderExecutor) (*IncrementTableReaderExecutor, error) {
	var err error
	ts := &IncrementTableReaderExecutor{
		baseExecutor: newBaseExecutor(src.ctx, src.schema, src.id),
		table:        src.table,
		ranges:       src.ranges,
		dagPB:        src.dagPB,
		startTS:      src.startTS,
	}
	ranges := make([]*coprocessor.KeyRange, len(src.kvRanges))
	for i, r := range src.kvRanges {
		ranges[i] = &coprocessor.KeyRange{
			Start: r.StartKey,
			End:   r.EndKey,
		}
	}
	copExec, err := buildCopExecutor(src.ctx, ranges, src.dagPB)
	if err != nil {
		return nil, err
	}
	err = copExec.Open(context.Background())
	if err != nil {
		return nil, err
	}
	ts.children = append(ts.children, copExec)
	return ts, err
}

func buildCopExecutor(ctx sessionctx.Context, ranges []*coprocessor.KeyRange, dag *tipb.DAGRequest) (Executor, error) {
	copHandler := NewCoprocessorDAGHandler(ctx)
	is := sessiontxn.GetTxnManager(ctx).GetTxnInfoSchema()
	return copHandler.buildCopExecutor(is, ranges, dag)
}

func (e *IncrementTableReaderExecutor) Next(ctx context.Context, req *chunk.Chunk) error {
	req.Reset()
	return Next(ctx, e.children[0], req)
}

type TableScanSinker struct {
	baseExecutor
	dbInfo  *model.DBInfo
	tbl     *model.TableInfo
	columns []*model.ColumnInfo

	seq      int
	executed bool
}

func (e *TableScanSinker) Next(ctx context.Context, req *chunk.Chunk) error {
	req.Reset()
	if e.executed {
		return nil
	}
	e.executed = true
	sc := e.ctx.GetSessionVars().StmtCtx
	for idx, col := range e.columns {
		v := types.NewDatum(e.seq)
		v1, err := v.ConvertTo(sc, &col.FieldType)
		if err != nil {
			return err
		}
		req.AppendDatum(idx, &v1)
		e.seq += 1
	}
	return nil
}

func (e *TableScanSinker) Reset() error {
	e.executed = false
	return nil
}
