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
	"fmt"
	"strconv"
	"testing"

	"github.com/pingcap/tidb/executor"
	"github.com/pingcap/tidb/parser/auth"
	"github.com/pingcap/tidb/stmtcache"
	"github.com/pingcap/tidb/testkit"
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
	tk.MustExec("create database test2")
	tk.MustExec("use test2")
	tk.MustExec("create user 'u1'@'%' identified by '';")
	tk.MustExec("grant all privileges on *.* to 'u1'@'%';")
	err := tk.Session().Auth(&auth.UserIdentity{Username: "u1", Hostname: "localhost", CurrentUser: true, AuthUsername: "u1", AuthHostname: "%"}, nil, []byte("012345678901234567890"))
	require.NoError(t, err)
	require.NotNil(t, tk.Session().GetSessionVars().User)

	tk.MustExec("create table t1 (a int, b int, c int)")
	totalRows := int64(100)
	for i := int64(0); i < totalRows; i += 1 {
		tk.MustExec(fmt.Sprintf("insert into t1 values (%[1]v, %[1]v, %[2]v)", i, i%3))
	}
	tk.MustQuery("select count(*) from t1").Check(testkit.Rows(strconv.FormatInt(totalRows, 10)))
	tk.MustQuery("select count(*) from t1").Check(testkit.Rows(strconv.FormatInt(totalRows+1, 10)))
	tk.MustQuery("select count(*) from t1").Check(testkit.Rows(strconv.FormatInt(totalRows+2, 10)))

	tk.MustQuery("select avg(a) from t1").Check(testkit.Rows("49.5000"))
	tk.MustQuery("select avg(a) from t1").Check(testkit.Rows("49.0198")) //  ((49.5*100)+1)/101 = 49.01980198019802

	tk.MustQuery("select sum(a) from t1").Check(testkit.Rows("4950"))
	tk.MustQuery("select sum(a) from t1").Check(testkit.Rows("4951"))

	tk.MustQuery("select min(a) from t1").Check(testkit.Rows("0"))
	tk.MustQuery("select min(a) from t1").Check(testkit.Rows("0"))

	tk.MustQuery("select max(a) from t1").Check(testkit.Rows("99"))
	tk.MustQuery("select max(a) from t1").Check(testkit.Rows("99"))
	for i := 1; i < 99; i++ {
		tk.MustQuery("select max(a) from t1").Check(testkit.Rows("99"))
	}
	tk.MustQuery("select max(a) from t1").Check(testkit.Rows("100"))
	tk.MustQuery("select max(a) from t1").Check(testkit.Rows("101"))

	tk.MustQuery("select sum(a+b) from t1").Check(testkit.Rows("9900"))
	tk.MustQuery("select sum(a+b) from t1").Check(testkit.Rows("9902"))

	// test agg with group by.
	tk.MustQuery("select c, count(*) from t1 group by c order by c").Check(testkit.Rows("0 34", "1 33", "2 33"))
	// todo: fix me(HashAggExec)
	//tk.MustQuery("select c, count(*) from t1 group by c order by c").Check(testkit.Rows("0 34", "1 33", "2 33"))
}
