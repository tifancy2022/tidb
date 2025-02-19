// Copyright 2021 PingCAP, Inc. Licensed under Apache-2.0.

package restore

import (
	"context"
	"math"
	"strconv"
	"sync"

	"github.com/pingcap/errors"
	backuppb "github.com/pingcap/kvproto/pkg/brpb"
	"github.com/pingcap/log"
	berrors "github.com/pingcap/tidb/br/pkg/errors"
	"github.com/pingcap/tidb/br/pkg/storage"
	"github.com/pingcap/tidb/br/pkg/stream"
	"github.com/pingcap/tidb/util/mathutil"
	"go.uber.org/zap"
)

type StreamMetadataSet struct {
	metadata map[string]*backuppb.Metadata
	// The metadata after changed that needs to be write back.
	writeback map[string]*backuppb.Metadata

	Helper *stream.MetadataHelper

	BeforeDoWriteBack func(path string, last, current *backuppb.Metadata) (skip bool)
}

// LoadUntil loads the metadata until the specified timestamp.
// This would load all metadata files that *may* contain data from transaction committed before that TS.
// Note: maybe record the timestamp and reject reading data files after this TS?
func (ms *StreamMetadataSet) LoadUntil(ctx context.Context, s storage.ExternalStorage, until uint64) error {
	metadataMap := struct {
		sync.Mutex
		metas map[string]*backuppb.Metadata
	}{}
	ms.writeback = make(map[string]*backuppb.Metadata)
	metadataMap.metas = make(map[string]*backuppb.Metadata)
	err := stream.FastUnmarshalMetaData(ctx, s, func(path string, raw []byte) error {
		m, err := ms.Helper.ParseToMetadataHard(raw)
		if err != nil {
			return err
		}
		metadataMap.Lock()
		// If the meta file contains only files with ts grater than `until`, when the file is from
		// `Default`: it should be kept, because its corresponding `write` must has commit ts grater than it, which should not be considered.
		// `Write`: it should trivially not be considered.
		if m.MinTs <= until {
			metadataMap.metas[path] = m
		}
		metadataMap.Unlock()
		return nil
	})
	if err != nil {
		return errors.Trace(err)
	}
	ms.metadata = metadataMap.metas
	return nil
}

// LoadFrom loads data from an external storage into the stream metadata set.
func (ms *StreamMetadataSet) LoadFrom(ctx context.Context, s storage.ExternalStorage) error {
	return ms.LoadUntil(ctx, s, math.MaxUint64)
}

func (ms *StreamMetadataSet) iterateDataFiles(f func(d *backuppb.DataFileGroup) (shouldBreak bool)) {
	for _, m := range ms.metadata {
		for _, d := range m.FileGroups {
			if f(d) {
				return
			}
		}
	}
}

// CalculateShiftTS calculates the shift-ts.
func (ms *StreamMetadataSet) CalculateShiftTS(startTS uint64) uint64 {
	metadatas := make([]*backuppb.Metadata, 0, len(ms.metadata))
	for _, m := range ms.metadata {
		metadatas = append(metadatas, m)
	}

	minBeginTS, exist := CalculateShiftTS(metadatas, startTS, mathutil.MaxUint)
	if !exist {
		minBeginTS = startTS
	}
	log.Warn("calculate shift-ts", zap.Uint64("start-ts", startTS), zap.Uint64("shift-ts", minBeginTS))
	return minBeginTS
}

// IterateFilesFullyBefore runs the function over all files contain data before the timestamp only.
//
//	0                                          before
//	|------------------------------------------|
//	 |-file1---------------| <- File contains records in this TS range would be found.
//	                               |-file2--------------| <- File contains any record out of this won't be found.
//
// This function would call the `f` over file1 only.
func (ms *StreamMetadataSet) IterateFilesFullyBefore(before uint64, f func(d *backuppb.DataFileGroup) (shouldBreak bool)) {
	ms.iterateDataFiles(func(d *backuppb.DataFileGroup) (shouldBreak bool) {
		if d.MaxTs >= before {
			return false
		}
		return f(d)
	})
}

// RemoveDataBefore would find files contains only records before the timestamp, mark them as removed from meta,
// and returning their information.
func (ms *StreamMetadataSet) RemoveDataBefore(from uint64) []*backuppb.DataFileGroup {
	removed := []*backuppb.DataFileGroup{}
	for metaPath, m := range ms.metadata {
		remainedDataFiles := make([]*backuppb.DataFileGroup, 0)
		// can we assume those files are sorted to avoid traversing here? (by what?)
		for _, ds := range m.FileGroups {
			if ds.MaxTs < from {
				removed = append(removed, ds)
			} else {
				remainedDataFiles = append(remainedDataFiles, ds)
			}
		}
		if len(remainedDataFiles) != len(m.FileGroups) {
			mCopy := *m
			mCopy.FileGroups = remainedDataFiles
			ms.WriteBack(metaPath, &mCopy)
		}
	}
	return removed
}

func (ms *StreamMetadataSet) WriteBack(path string, file *backuppb.Metadata) {
	ms.writeback[path] = file
}

func (ms *StreamMetadataSet) doWriteBackForFile(ctx context.Context, s storage.ExternalStorage, path string) error {
	data, ok := ms.writeback[path]
	if !ok {
		return errors.Annotatef(berrors.ErrInvalidArgument, "There is no write back for path %s", path)
	}
	// If the metadata file contains no data file, remove it due to it is meanless.
	if len(data.FileGroups) == 0 {
		if err := s.DeleteFile(ctx, path); err != nil {
			return errors.Annotatef(err, "failed to remove the empty meta %s", path)
		}
		return nil
	}

	bs, err := ms.Helper.Marshal(data)
	if err != nil {
		return errors.Annotatef(err, "failed to marshal the file %s", path)
	}
	return truncateAndWrite(ctx, s, path, bs)
}

func (ms *StreamMetadataSet) DoWriteBack(ctx context.Context, s storage.ExternalStorage) error {
	for path := range ms.writeback {
		if ms.BeforeDoWriteBack != nil && ms.BeforeDoWriteBack(path, ms.metadata[path], ms.writeback[path]) {
			return nil
		}
		err := ms.doWriteBackForFile(ctx, s, path)
		// NOTE: Maybe we'd better roll back all writebacks? (What will happen if roll back fails too?)
		if err != nil {
			return errors.Annotatef(err, "failed to write back file %s", path)
		}

		delete(ms.writeback, path)
	}
	return nil
}

func truncateAndWrite(ctx context.Context, s storage.ExternalStorage, path string, data []byte) error {
	// Performance hack: the `Write` implemention would truncate the file if it exists.
	if err := s.WriteFile(ctx, path, data); err != nil {
		return errors.Annotatef(err, "failed to save the file %s to %s", path, s.URI())
	}
	return nil
}

const (
	// TruncateSafePointFileName is the filename that the ts(the log have been truncated) is saved into.
	TruncateSafePointFileName = "v1_stream_trancate_safepoint.txt"
)

// GetTSFromFile gets the current truncate safepoint.
// truncate safepoint is the TS used for last truncating:
// which means logs before this TS would probably be deleted or incomplete.
func GetTSFromFile(
	ctx context.Context,
	s storage.ExternalStorage,
	filename string,
) (uint64, error) {
	exists, err := s.FileExists(ctx, filename)
	if err != nil {
		return 0, err
	}
	if !exists {
		return 0, nil
	}
	data, err := s.ReadFile(ctx, filename)
	if err != nil {
		return 0, err
	}
	value, err := strconv.ParseUint(string(data), 10, 64)
	if err != nil {
		return 0, errors.Annotatef(berrors.ErrInvalidMetaFile, "failed to parse the truncate safepoint")
	}
	return value, nil
}

// SetTSToFile overrides the current truncate safepoint.
// truncate safepoint is the TS used for last truncating:
// which means logs before this TS would probably be deleted or incomplete.
func SetTSToFile(
	ctx context.Context,
	s storage.ExternalStorage,
	safepoint uint64,
	filename string,
) error {
	content := strconv.FormatUint(safepoint, 10)
	return truncateAndWrite(ctx, s, filename, []byte(content))
}

func UpdateShiftTS(m *backuppb.Metadata, startTS uint64, restoreTS uint64) (uint64, bool) {
	var (
		minBeginTS uint64
		isExist    bool
	)
	if len(m.FileGroups) == 0 || m.MinTs > restoreTS || m.MaxTs < startTS {
		return 0, false
	}

	for _, ds := range m.FileGroups {
		for _, d := range ds.DataFilesInfo {
			if d.Cf == stream.DefaultCF || d.MinBeginTsInDefaultCf == 0 {
				continue
			}
			if d.MinTs > restoreTS || d.MaxTs < startTS {
				continue
			}
			if d.MinBeginTsInDefaultCf < minBeginTS || !isExist {
				isExist = true
				minBeginTS = d.MinBeginTsInDefaultCf
			}
		}
	}
	return minBeginTS, isExist
}

// CalculateShiftTS gets the minimal begin-ts about transaction according to the kv-event in write-cf.
func CalculateShiftTS(
	metas []*backuppb.Metadata,
	startTS uint64,
	restoreTS uint64,
) (uint64, bool) {
	var (
		minBeginTS uint64
		isExist    bool
	)
	for _, m := range metas {
		if len(m.FileGroups) == 0 || m.MinTs > restoreTS || m.MaxTs < startTS {
			continue
		}
		ts, ok := UpdateShiftTS(m, startTS, restoreTS)
		if ok && (!isExist || ts < minBeginTS) {
			minBeginTS = ts
			isExist = true
		}
	}

	return minBeginTS, isExist
}
