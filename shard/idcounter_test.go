package shard_test

import (
	"path/filepath"
	"testing"

	"github.com/semafind/semadb/diskstore"
	"github.com/semafind/semadb/shard"
	"github.com/stretchr/testify/require"
)

func tempDB(t *testing.T) diskstore.DiskStore {
	dbpath := filepath.Join(t.TempDir(), "test.bbolt")
	db, err := diskstore.Open(dbpath)
	require.NoError(t, err)
	return db
}

func withCounter(t *testing.T, db diskstore.DiskStore, f func(*shard.IdCounter)) {
	if db == nil {
		db = tempDB(t)
		err := db.CreateBucketsIfNotExists([]string{"testing"})
		require.NoError(t, err)
	}
	db.Write("testing", func(b diskstore.Bucket) error {
		counter, err := shard.NewIdCounter(b, []byte("freeIds"), []byte("nextFreeId"))
		require.NoError(t, err)
		f(counter)
		return nil
	})
}

func TestCounterBlank(t *testing.T) {
	// ---------------------------
	withCounter(t, nil, func(counter *shard.IdCounter) {
		require.Equal(t, uint64(1), counter.NextId())
		require.Equal(t, uint64(2), counter.NextId())
	})
}

func TestCounterPersistance(t *testing.T) {
	// ---------------------------
	db := tempDB(t)
	withCounter(t, db, func(counter *shard.IdCounter) {
		require.Equal(t, uint64(1), counter.NextId())
		require.Equal(t, uint64(2), counter.NextId())
		require.NoError(t, counter.Flush())
	})
	// ---------------------------
	withCounter(t, db, func(counter *shard.IdCounter) {
		require.Equal(t, uint64(3), counter.NextId())
		require.Equal(t, uint64(4), counter.NextId())
	})
}

func TestCounterFreeing(t *testing.T) {
	// ---------------------------
	db := tempDB(t)
	withCounter(t, db, func(counter *shard.IdCounter) {
		require.Equal(t, uint64(1), counter.NextId())
		require.Equal(t, uint64(2), counter.NextId())
		counter.FreeId(1)
		require.NoError(t, counter.Flush())
	})
	// ---------------------------
	withCounter(t, db, func(counter *shard.IdCounter) {
		require.Equal(t, uint64(1), counter.NextId())
		require.Equal(t, uint64(3), counter.NextId())
	})
}
