package cache

import (
	"fmt"

	"github.com/google/uuid"
	"go.etcd.io/bbolt"
)

// ---------------------------

/* We are creating these two type interfaces so that the compiler can stop us
 * from accidentally writing to a read only cache. This way, things will be
 * enforced at compile time. */

// A read only cache that only exposes the functions that are safe to use in
// read only mode.
type ReadOnlyCache interface {
	GetPoint(uint64) (*CachePoint, error)
	GetPointByUUID(uuid.UUID) (*CachePoint, error)
	GetMetadata(uint64) ([]byte, error)
	WithReadOnlyPointNeighbours(*CachePoint, func([]*CachePoint) error) error
}

type ReadWriteCache interface {
	ReadOnlyCache
	SetPoint(ShardPoint) (*CachePoint, error)
	WithPointNeighbours(*CachePoint, bool, func([]*CachePoint) error) error
	EdgeScan(map[uint64]struct{}) ([]uint64, error)
	Flush() error
}

// ---------------------------

type PointCache struct {
	bucket *bbolt.Bucket
	store  *sharedInMemStore
}

func newPointCache(bucket *bbolt.Bucket, store *sharedInMemStore) *PointCache {
	return &PointCache{
		bucket: bucket,
		store:  store,
	}
}

func (pc *PointCache) GetPoint(nodeId uint64) (*CachePoint, error) {
	pc.store.pointsMu.Lock()
	defer pc.store.pointsMu.Unlock()
	// ---------------------------
	if point, ok := pc.store.points[nodeId]; ok {
		return point, nil
	}
	// ---------------------------
	point, err := getNode(pc.bucket, nodeId)
	if err != nil {
		return nil, err
	}
	newPoint := &CachePoint{
		ShardPoint: point,
	}
	pc.store.points[nodeId] = newPoint
	pc.store.estimatedSize.Add(newPoint.estimateSize())
	return newPoint, nil
}

func (pc *PointCache) GetPointByUUID(pointId uuid.UUID) (*CachePoint, error) {
	pc.store.pointsMu.Lock()
	defer pc.store.pointsMu.Unlock()
	point, err := getPointByUUID(pc.bucket, pointId)
	if err != nil {
		return nil, err
	}
	newPoint := &CachePoint{
		ShardPoint: point,
	}
	pc.store.points[point.NodeId] = newPoint
	pc.store.estimatedSize.Add(newPoint.estimateSize())
	return newPoint, nil
}

// Operate with a lock on the point neighbours. If the neighbours are not
// loaded, load them from the database.
func (pc *PointCache) WithPointNeighbours(point *CachePoint, readOnly bool, fn func([]*CachePoint) error) error {
	/* We have to lock here because we can't have another goroutine changing the
	 * edges while we are using them. The read only case mainly occurs in
	 * searching whereas the writes happen for pruning edges. By using
	 * read-write lock, we are hoping the search doesn't get blocked too much in
	 * case there are concurrent insert, update, delete operations.
	 *
	 * Why are we not just locking each point, reading the neighbours and
	 * unlocking as opposed to locking throughout an operation. This is because
	 * if we know a goroutine has a chance to change the neighbours, another go
	 * routine might read outdated edges that might lead to disconnected graph.
	 * Consider the base case, 1 node with no edges, 2 go routines trying to
	 * insert. If locked only for reading, they'll both think there are no edges
	 * and race to add the first connection.
	 *
	 * Hint: to check if things are working, run:
	 * go test -race ./shard */
	// ---------------------------
	point.loadMu.Lock()
	// This check needs to be syncronized because we don't want two go routines
	// to load the neighbours at the same time.
	if point.loadedNeighbours {
		// Early return if the neighbours are already loaded, what would the
		// goroutine like to do?
		point.loadMu.Unlock()
		if readOnly {
			point.neighboursMu.RLock()
			defer point.neighboursMu.RUnlock()
		} else {
			point.neighboursMu.Lock()
			defer point.neighboursMu.Unlock()
		}
		return fn(point.neighbours)
	}
	defer point.loadMu.Unlock()
	// ---------------------------
	neighbours := make([]*CachePoint, 0, len(point.edges))
	for _, edgeId := range point.edges {
		edge, err := pc.GetPoint(edgeId)
		if err != nil {
			return err
		}
		neighbours = append(neighbours, edge)
	}
	point.neighbours = neighbours
	point.loadedNeighbours = true
	// Technically we can unlock loading lock here and use the neighboursMu lock
	// to have even more fine grain control. But that seems overkill for what is
	// to happen once.
	return fn(point.neighbours)
}

// These two functions are the same except for the lock type. This is to fit the
// interface types.
func (pc *PointCache) WithReadOnlyPointNeighbours(point *CachePoint, fn func([]*CachePoint) error) error {
	return pc.WithPointNeighbours(point, true, fn)
}

func (pc *PointCache) SetPoint(point ShardPoint) (*CachePoint, error) {
	pc.store.pointsMu.Lock()
	defer pc.store.pointsMu.Unlock()
	newPoint := &CachePoint{
		ShardPoint: point,
		isDirty:    true,
	}
	if newPoint.NodeId == 0 {
		return nil, fmt.Errorf("node id cannot be 0")
	}
	pc.store.points[newPoint.NodeId] = newPoint
	pc.store.estimatedSize.Add(newPoint.estimateSize())
	return newPoint, nil
}

func (pc *PointCache) GetMetadata(nodeId uint64) ([]byte, error) {
	cp, err := pc.GetPoint(nodeId)
	if err != nil {
		return nil, err
	}
	// Backfill metadata if it's not set
	if cp.Metadata == nil {
		mdata, err := getPointMetadata(pc.bucket, nodeId)
		if err != nil {
			return nil, err
		}
		cp.Metadata = mdata
	}
	pc.store.estimatedSize.Add(int32(len(cp.Metadata)))
	return cp.Metadata, nil
}

func (pc *PointCache) EdgeScan(deleteSet map[uint64]struct{}) ([]uint64, error) {
	return scanPointEdges(pc.bucket, deleteSet)
}

func (pc *PointCache) Flush() error {
	pc.store.pointsMu.Lock()
	defer pc.store.pointsMu.Unlock()
	for _, point := range pc.store.points {
		if point.isDeleted {
			if err := deletePoint(pc.bucket, point.ShardPoint); err != nil {
				return err
			}
			delete(pc.store.points, point.NodeId)
			pc.store.estimatedSize.Add(-point.estimateSize())
			continue
		}
		if point.isDirty {
			if err := setPoint(pc.bucket, point.ShardPoint); err != nil {
				return err
			}
			// Only one goroutine flushes the point cache so we are not locking
			// here.
			point.isDirty = false
			point.isEdgeDirty = false
			continue
		}
		if point.isEdgeDirty {
			if err := setPointEdges(pc.bucket, point.ShardPoint); err != nil {
				return err
			}
			point.isEdgeDirty = false
		}
	}
	return nil
}
