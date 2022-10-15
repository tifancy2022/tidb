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

package stmtcache

import (
	"bytes"
	"crypto/sha256"
	"hash"
	"sync"
	"time"

	"github.com/pingcap/tidb/parser/mysql"
	"github.com/pingcap/tidb/types"
	"github.com/pingcap/tidb/util/kvcache"
)

var (
	maxCacheCount uint = 1000
)

var StmtCache = newStmtSummaryByDigestMap()

type stmtCache struct {
	sync.Mutex

	cache *kvcache.SimpleLRUCache
}

func (sc *stmtCache) AddStatement(stmt *StmtElement) ([]byte, bool) {
	stmt.ts = time.Now().Unix()
	stmt.Hash()

	sc.Lock()
	defer sc.Unlock()

	_, ok := sc.cache.Get(stmt)
	if !ok {
		sc.cache.Put(stmt, stmt)
	}
	return stmt.Hash(), !ok
}

func (sc *stmtCache) Size() int {
	sc.Lock()
	defer sc.Unlock()

	return sc.cache.Size()
}

func (sc *stmtCache) GetAllStmtCached() [][]types.Datum {
	sc.Lock()
	defer sc.Unlock()

	values := sc.cache.Values()
	rows := make([][]types.Datum, 0, len(values))
	for _, v := range values {
		stmt := v.(*StmtElement)

		ts := types.NewTime(types.FromGoTime(time.Unix(stmt.ts, 0)), mysql.TypeTimestamp, types.DefaultFsp)
		row := types.MakeDatums(
			ts,
			stmt.SchemaName,
			stmt.SQL,
		)
		rows = append(rows, row)
	}
	return rows
}

func newStmtSummaryByDigestMap() *stmtCache {
	return &stmtCache{
		cache: kvcache.NewSimpleLRUCache(maxCacheCount, 0, 0),
	}
}

type StmtElement struct {
	SchemaName string
	SQL        string
	ts         int64
	hash       []byte
}

func (key *StmtElement) Hash() []byte {
	if len(key.hash) == 0 {
		d := digesterPool.Get().(*hashGenerator)
		defer func() {
			d.buf.Reset()
			d.hasher.Reset()
			digesterPool.Put(d)
		}()
		d.buf.WriteString(key.SchemaName)
		d.buf.WriteString(key.SQL)
		d.hasher.Write(d.buf.Bytes())
		key.hash = d.hasher.Sum(nil)
	}
	return key.hash
}

var digesterPool = sync.Pool{
	New: func() interface{} {
		return &hashGenerator{
			hasher: sha256.New(),
		}
	},
}

type hashGenerator struct {
	buf    bytes.Buffer
	hasher hash.Hash
}
