package executor

import (
	"bytes"
	"context"
	"github.com/pingcap/tidb/expression"
	"github.com/pingcap/tidb/sessionctx"
	"github.com/pingcap/tidb/stmtcache"
	"github.com/pingcap/tidb/util/chunk"
	"github.com/pingcap/tidb/util/logutil"
	"go.uber.org/zap"
	"strings"
	"sync"
	"sync/atomic"

	plannercore "github.com/pingcap/tidb/planner/core"
)

func TryCacheAggPlan(p plannercore.PhysicalPlan, e Executor) (Executor, bool, error) {
	if _, ok := e.(*CacheExecutor); ok {
		return e, false, nil
	}
	switch p.(type) {
	case *plannercore.PhysicalStreamAgg, *plannercore.PhysicalHashAgg:
		children := p.Children()
		if len(children) > 1 {
			return e, false, nil
		}
		if !isSupportedPlan(children[0]) {
			return e, false, nil
		}
		newE := &CacheExecutor{Executor: e}
		return newE, true, CacheExecManager.addCacheExecutor(p, e)
	default:
		children := p.Children()
		childrenExec := e.base().children
		for i, child := range children {
			if i >= len(childrenExec) {
				return e, false, nil
			}
			_, cached, err := TryCacheAggPlan(child, childrenExec[i])
			if err != nil {
				return e, false, err
			}
			if cached {
				newE := &CacheExecutor{Executor: childrenExec[i]}
				childrenExec[i] = newE
			}
		}
	}
	return e, false, nil
}

func isSupportedPlan(p plannercore.PhysicalPlan) bool {
	switch p.(type) {
	case *plannercore.PhysicalStreamAgg, *plannercore.PhysicalHashAgg, *plannercore.PhysicalTopN:
		for _, child := range p.Children() {
			ok := isSupportedPlan(child)
			if !ok {
				return false
			}
		}
		return true
	case *plannercore.PhysicalTableReader:
		return true
	default:
		return false
	}
}

func GetPlanTreeHash(p plannercore.PhysicalPlan) (string, []byte) {
	var planTree string
	hash := stmtcache.CalculateHash(func(buf *bytes.Buffer) {
		appendPlanInfo(buf, p, 0, false)
		planTree = buf.String()
	})
	return planTree, hash
}

func appendPlanInfo(buf *bytes.Buffer, p plannercore.PhysicalPlan, depth int, isCop bool) {
	if buf.Len() > 0 {
		buf.WriteString("\n")
	}
	buf.WriteString(strings.Repeat("  ", depth))
	buf.WriteString(p.TP())
	buf.WriteByte('\t')
	var access, operatorInfo string
	access, operatorInfo = plannercore.GetPlanInfo(p)
	buf.WriteString(access)
	buf.WriteByte('\t')
	buf.WriteString(operatorInfo)
	buf.WriteByte('\t')
	buf.WriteString("columns:")
	schema := p.Schema()
	for i, col := range schema.Columns {
		if i > 0 {
			buf.WriteByte(',')
		}
		buf.WriteString(col.String())
	}
	switch v := p.(type) {
	case *plannercore.PhysicalTableReader:
		appendPlanInfo(buf, v.GetTablePlan(), depth+1, true)
		return
	}
	for _, child := range p.Children() {
		appendPlanInfo(buf, child, depth+1, isCop)
	}
}

var CacheExecManager = newCacheExecutorManager()

type CacheExecutorManager struct {
	sync.Mutex
	cacheExecMap map[string]*CacheExecutor
}

type CacheExecutor struct {
	mu       sync.Mutex
	started  atomic.Bool
	planTree string
	p        plannercore.PhysicalPlan
	Executor
}

func newCacheExecutorManager() *CacheExecutorManager {
	return &CacheExecutorManager{
		cacheExecMap: make(map[string]*CacheExecutor),
	}
}

func (e *CacheExecutor) Open(ctx context.Context) error {
	return nil
}

func (e *CacheExecutor) Next(ctx context.Context, req *chunk.Chunk) error {
	return e.Executor.Next(ctx, req)
}

func (e *CacheExecutor) Schema() *expression.Schema {
	return e.Executor.Schema()
}

func (e *CacheExecutor) Reset() error {
	return e.Executor.Reset()
}

func (e *CacheExecutor) Begin() {
	e.mu.Lock()
	e.started.Store(true)
}

func (e *CacheExecutor) Close() error {
	if !e.started.Load() {
		return nil
	}
	e.started.Store(false)
	e.mu.Unlock()
	return nil
}

func (sc *CacheExecutorManager) addCacheExecutor(p plannercore.PhysicalPlan, e Executor) error {
	planTree, digest := GetPlanTreeHash(p)
	exec, err := replaceTableReader(e)
	if err != nil {
		return err
	}
	err = exec.Reset()
	if err != nil {
		return err
	}

	sc.Lock()
	defer sc.Unlock()
	sc.cacheExecMap[string(digest)] = &CacheExecutor{planTree: planTree, p: p, Executor: e}
	return nil
}

func (sc *CacheExecutorManager) GetCacheExecutorByDigest(digest string, ctx sessionctx.Context, p plannercore.PhysicalPlan) (*CacheExecutor, error) {
	sc.Lock()
	defer sc.Unlock()
	e := sc.cacheExecMap[digest]
	if e == nil {
		return nil, nil
	}
	err := e.Executor.Reset()
	if err != nil {
		return nil, err
	}
	err = e.ResetCtx(ctx, p)
	if err != nil {
		logutil.BgLogger().Info("ResetCtx cached executor failed----------cs-------------", zap.String("err", err.Error()))
		return nil, err
	}
	e.Begin()
	return e, err
}

func (sc *CacheExecutorManager) Reset() {
	sc.Lock()
	defer sc.Unlock()
	for _, e := range sc.cacheExecMap {
		err := e.Executor.Close()
		if err != nil {
			logutil.BgLogger().Info("close cache executor failed", zap.String("error", err.Error()))
		}
	}
	sc.cacheExecMap = make(map[string]*CacheExecutor)
	logutil.BgLogger().Info("cache executor manager has been reset---")
}

func (sc *CacheExecutorManager) Close() {
	sc.Lock()
	defer sc.Unlock()
	for _, rs := range sc.cacheExecMap {
		err := rs.Executor.Close()
		if err != nil {
			logutil.BgLogger().Info("close cache executor failed", zap.String("error", err.Error()))
		}
	}
	logutil.BgLogger().Info("cache executor manager closed---")
}

func (sc *CacheExecutorManager) Size() int {
	return len(sc.cacheExecMap)
}

type FakeExecutor struct{ *baseExecutor }

func (f *FakeExecutor) base() *baseExecutor {
	return nil
}

func (f *FakeExecutor) Open(ctx context.Context) error {
	return nil
}

func (f *FakeExecutor) Next(ctx context.Context, req *chunk.Chunk) error {
	return nil
}

func (f *FakeExecutor) Close() error {
	return nil
}

func (f *FakeExecutor) Schema() *expression.Schema {
	return nil
}

func (f *FakeExecutor) Reset() error {
	return nil
}
