package executor

import (
	"context"
	"fmt"
	"sync"

	"github.com/pingcap/kvproto/pkg/coprocessor"
	"github.com/pingcap/tidb/parser/model"
	plannercore "github.com/pingcap/tidb/planner/core"
	"github.com/pingcap/tidb/sessionctx"
	"github.com/pingcap/tidb/sessiontxn"
	"github.com/pingcap/tidb/table"
	"github.com/pingcap/tidb/types"
	"github.com/pingcap/tidb/util/chunk"
	"github.com/pingcap/tidb/util/logutil"
	"github.com/pingcap/tidb/util/ranger"
	"github.com/pingcap/tidb/util/ticdcutil"
	"github.com/pingcap/tipb/go-tipb"
	"go.uber.org/zap"
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

func (sc *StmtCacheExecutorManager) Close() {
	sc.Lock()
	defer sc.Unlock()
	for _, rs := range sc.cacheRs {
		err := rs.executor.Close()
		if err != nil {
			logutil.BgLogger().Info("close stmt cache executor failed", zap.String("error", err.Error()))
		}
	}
	logutil.BgLogger().Info("stmt cache manager closed---")
}

func (sc *StmtCacheExecutorManager) replaceTableReader(e Executor) (Executor, error) {
	switch v := e.(type) {
	case *TableReaderExecutor:
		err := v.Close()
		if err != nil {
			return nil, err
		}
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

func (e *IncrementTableReaderExecutor) Reset() error {
	for _, child := range e.children {
		copExec, ok := child.(CopExecutor)
		if !ok {
			msg := fmt.Sprintf("%#v is not cop executor", child)
			panic(msg)
		}
		err := copExec.ResetAndClean()
		if err != nil {
			return err
		}
	}
	return nil
}

type TableScanSinker struct {
	baseExecutor
	dbInfo  *model.DBInfo
	tbl     *model.TableInfo
	columns []*model.ColumnInfo
	sinker  ticdcutil.Changefeed
}

func BuildTableScanSinker(ctx sessionctx.Context, v *plannercore.PhysicalTableScanSinker) (*TableScanSinker, error) {
	txn, err := ctx.Txn(false)
	if err != nil {
		return nil, err
	}
	e := &TableScanSinker{
		baseExecutor: newBaseExecutor(ctx, v.Schema(), v.ID()),
		dbInfo:       v.DBInfo,
		tbl:          v.Table,
		columns:      v.Columns,
	}
	e.sinker, err = ticdcutil.NewChangefeed(context.Background(), txn.StartTS(), v.DBInfo.Name.L, v.Table.Name.L)
	if err != nil {
		return nil, err
	}
	return e, nil
}

func (e *TableScanSinker) Next(ctx context.Context, req *chunk.Chunk) error {
	req.Reset()
	//sc := e.ctx.GetSessionVars().StmtCtx
	defer func() {
		logutil.BgLogger().Info("table scan sinker next-----", zap.Int("rows", req.NumRows()))
	}()
	for {
		event, err := e.sinker.Next(ctx)
		if err != nil || event == nil {
			return nil
		}
		switch event.Tp {
		case ticdcutil.EventTypeInsert:
			//buf := bytes.NewBuffer(nil)
			//for i, v := range event.Columns {
			//	s, err := v.ToString()
			//	if err != nil {
			//		return err
			//	}
			//	if i > 0 {
			//		buf.WriteString(", ")
			//	}
			//	buf.WriteString(s)
			//}
			//logutil.BgLogger().Info("sinker receive change feed", zap.String("table", e.tbl.Name.L), zap.String("row", buf.String()))
			for idx, col := range e.columns {
				if col.Offset >= len(event.Columns) {
					return fmt.Errorf("column offset %v more than event data len %v", col.Offset, len(event.Columns))
				}
				//v, err := event.Columns[col.Offset].ConvertTo(sc, &col.FieldType)
				//if err != nil {
				//	return err
				//}
				req.AppendDatum(idx, &event.Columns[col.Offset])
				//req.AppendDatum(idx, &v)
			}
		}
		if req.IsFull() {
			return nil
		}
	}
}

func (e *TableScanSinker) Close() error {
	logutil.BgLogger().Info("close table scan sinker", zap.String("table", e.tbl.Name.L))
	return e.sinker.Close()
}

func (e *TableScanSinker) Reset() error {
	return nil
}

func (e *TableScanSinker) ResetAndClean() error {
	return nil
}

type MockTableScanSinker struct {
	baseExecutor
	dbInfo  *model.DBInfo
	tbl     *model.TableInfo
	columns []*model.ColumnInfo

	seq      int
	executed bool
}

func (e *MockTableScanSinker) Next(ctx context.Context, req *chunk.Chunk) error {
	req.Reset()
	if e.executed {
		return nil
	}
	e.executed = true
	e.seq += 1
	sc := e.ctx.GetSessionVars().StmtCtx
	for idx, col := range e.columns {
		v := types.NewDatum(e.seq)
		v1, err := v.ConvertTo(sc, &col.FieldType)
		if err != nil {
			return err
		}
		req.AppendDatum(idx, &v1)
	}
	return nil
}

func (e *MockTableScanSinker) Reset() error {
	e.executed = false
	return nil
}

func (e *MockTableScanSinker) ResetAndClean() error {
	e.executed = false
	return nil
}
