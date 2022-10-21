package core

import (
	"github.com/pingcap/tidb/parser/model"
)

// PhysicalMemTable reads memory table.
type PhysicalTableScanSinker struct {
	physicalSchemaProducer

	DBInfo  *model.DBInfo
	Table   *model.TableInfo
	Columns []*model.ColumnInfo
}
