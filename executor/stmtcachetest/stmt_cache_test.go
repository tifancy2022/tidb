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

package stmtcachetest

import (
	"context"
	"fmt"
	"strconv"
	"testing"

	"github.com/pingcap/tidb/executor"
	"github.com/pingcap/tidb/parser/auth"
	"github.com/pingcap/tidb/stmtcache"
	"github.com/pingcap/tidb/testkit"
	"github.com/pingcap/tidb/util/chunk"
	"github.com/pingcap/tidb/util/sqlexec"
	"github.com/stretchr/testify/require"
)

func TestStmtCache(t *testing.T) {
	store := testkit.CreateMockStore(t)
	tk := testkit.NewTestKit(t, store)
	tk.MustExec("use test")
	tk.MustExec("create user 'u1'@'%' identified by '';")
	tk.MustExec("grant all privileges on *.* to 'u1'@'%';")
	err := tk.Session().Auth(&auth.UserIdentity{Username: "u1", Hostname: "localhost", CurrentUser: true, AuthUsername: "u1", AuthHostname: "%"}, nil, []byte("012345678901234567890"))
	require.NoError(t, err)
	require.NotNil(t, tk.Session().GetSessionVars().User)

	tk.MustExec("create table t1 (a int, b int, index(a))")
	cnt := int(executor.CacheMinProcessKeys) + 1
	for i := 0; i < cnt; i++ {
		tk.MustExec(fmt.Sprintf("insert into t1 values (%v, %v)", i, i))
	}
	tk.MustQuery("select count(*) from t1").Check(testkit.Rows(strconv.Itoa(cnt)))
	require.Equal(t, 1, stmtcache.StmtCache.Size())
	tk.MustQuery("select count(*) from INFORMATION_SCHEMA.tables").Check(testkit.Rows("789"))
	tk.MustQuery("select schema_name, query from INFORMATION_SCHEMA.statement_cached").Check(testkit.RowsWithSep("|", "test|select count(*) from t1"))
	tk.MustQuery("select schema_name, query from INFORMATION_SCHEMA.statement_cached").Check(testkit.RowsWithSep("|", "test|select count(*) from t1"))
}

func TestStmtCacheExecutor(t *testing.T) {
	store := testkit.CreateMockStore(t)
	tk := testkit.NewTestKit(t, store)
	tk.MustExec("use test")
	tk.MustExec("create user 'u1'@'%' identified by '';")
	tk.MustExec("grant all privileges on *.* to 'u1'@'%';")
	err := tk.Session().Auth(&auth.UserIdentity{Username: "u1", Hostname: "localhost", CurrentUser: true, AuthUsername: "u1", AuthHostname: "%"}, nil, []byte("012345678901234567890"))
	require.NoError(t, err)
	require.NotNil(t, tk.Session().GetSessionVars().User)

	tk.MustExec("create table t1 (a int, b int)")
	totalRows := int64(10000)
	for i := int64(0); i < totalRows; i += 2 {
		tk.MustExec(fmt.Sprintf("insert into t1 values (%[1]v, %[1]v), (%[2]v, %[2]v)", i, i+1))
	}

	ctx := context.Background()
	sql := "select count(*) from t1"
	se := tk.Session()
	stmts, err := se.Parse(ctx, sql)
	require.NoError(t, err)
	require.Equal(t, 1, len(stmts))
	rs, err := tk.Session().ExecuteStmt(ctx, stmts[0])
	require.NoError(t, err)
	req := rs.NewChunk(nil)

	for {
		err := rs.Next(ctx, req)
		require.NoError(t, err)
		if req.NumRows() == 0 {
			break
		}
		iter := chunk.NewIterator4Chunk(req.CopyConstruct())
		for row := iter.Begin(); row != iter.End(); row = iter.Next() {
			cnt := row.GetInt64(0)
			require.Equal(t, totalRows, cnt)
		}
	}

	irs, ok := rs.(sqlexec.IncrementRecordSet)
	require.True(t, ok)
	err = irs.Reset()
	require.Nil(t, err)

	err = rs.Next(ctx, req)
	require.NoError(t, err)
	require.Equal(t, 1, req.NumRows())
	require.Equal(t, totalRows, req.GetRow(0).GetInt64(0))
}
