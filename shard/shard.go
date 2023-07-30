package shard

import (
	"fmt"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"runtime/pprof"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
	"github.com/schollz/progressbar/v3"
	"github.com/semafind/semadb/config"
	"github.com/semafind/semadb/distance"
	"github.com/semafind/semadb/models"
	"go.etcd.io/bbolt"
)

type Shard struct {
	db         *bbolt.DB
	collection models.Collection
	distFn     distance.DistFunc
	startId    uuid.UUID
}

func NewShard(shardDir string, collection models.Collection) (*Shard, error) {
	// ---------------------------
	db, err := bbolt.Open(filepath.Join(shardDir, "db"), 0666, nil)
	if err != nil {
		return nil, fmt.Errorf("could not open shard db: %w", err)
	}
	// ---------------------------
	var startId uuid.UUID
	err = db.Update(func(tx *bbolt.Tx) error {
		// ---------------------------
		// Setup buckets
		bPoints, err := tx.CreateBucketIfNotExists([]byte("points"))
		if err != nil {
			return fmt.Errorf("create bucket: %s", err)
		}
		bInternal, err := tx.CreateBucketIfNotExists([]byte("internal"))
		if err != nil {
			return fmt.Errorf("create bucket: %s", err)
		}
		// ---------------------------
		// Setup start point
		sid := bInternal.Get([]byte("startId"))
		if sid == nil {
			// There is no start point, we'll create one
			// Create random unit vector of size n
			randVector := make([]float32, collection.VectorSize)
			sum := float32(0)
			for i := range randVector {
				randVector[i] = rand.Float32()*2 - 1
				sum += randVector[i] * randVector[i]
			}
			// Normalise the vector
			norm := 1 / float32(math.Sqrt(float64(sum)))
			for i := range randVector {
				randVector[i] *= norm
			}
			// ---------------------------
			// Create start point
			randPoint := ShardPoint{
				Point: models.Point{
					Id:     uuid.New(),
					Vector: randVector,
				},
			}
			log.Debug().Str("id", randPoint.Id.String()).Msg("creating start point")
			if err := setPoint(bPoints, randPoint); err != nil {
				return fmt.Errorf("could not set start point: %w", err)
			}
			if err := bInternal.Put([]byte("startId"), randPoint.Id[:]); err != nil {
				return fmt.Errorf("could not set start point id: %w", err)
			}
			startId = randPoint.Id
			// ---------------------------
		} else {
			startId, err = uuid.FromBytes(sid)
			if err != nil {
				return fmt.Errorf("could not parse start point: %w", err)
			}
			log.Debug().Str("id", startId.String()).Msg("found start point")
		}
		// ---------------------------
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("could not initialise shard: %w", err)
	}
	// ---------------------------
	distFn := distance.EuclideanDistance
	if collection.DistMetric == "cosine" {
		distFn = distance.CosineDistance
	}
	// ---------------------------
	return &Shard{
		db:         db,
		collection: collection,
		distFn:     distFn,
		startId:    startId,
	}, nil
}

func (s *Shard) Close() error {
	return s.db.Close()
}

// ---------------------------

func (s *Shard) insertPoint(pc *PointCache, startPointId uuid.UUID, shardPoint ShardPoint) error {
	// ---------------------------
	point := pc.SetPoint(shardPoint)
	// ---------------------------
	_, visitedSet, err := s.greedySearch(pc, startPointId, point.Vector, 1, s.collection.Parameters.SearchSize)
	if err != nil {
		return fmt.Errorf("could not greedy search: %w", err)
	}
	// ---------------------------
	s.robustPrune(point, visitedSet, s.collection.Parameters.Alpha, s.collection.Parameters.DegreeBound)
	// ---------------------------
	// Add the bi-directional edges
	for _, nId := range point.Edges {
		n, err := pc.GetPoint(nId)
		if err != nil {
			return fmt.Errorf("could not get neighbour point: %w", err)
		}
		if len(n.Edges)+1 > s.collection.Parameters.DegreeBound {
			// ---------------------------
			// We need to prune the neighbour as well to keep the degree bound
			nn, err := pc.GetPointNeighbours(n)
			if err != nil {
				return fmt.Errorf("could not get neighbour neighbours: %w", err)
			}
			candidateSet := NewDistSet(n.Vector, len(n.Edges)+1, s.distFn)
			candidateSet.AddPoint(nn...)
			candidateSet.AddPoint(point)
			candidateSet.Sort()
			s.robustPrune(n, candidateSet, s.collection.Parameters.Alpha, s.collection.Parameters.DegreeBound)
		} else {
			// ---------------------------
			// Add the edge
			pc.AddNeighbour(n, point)
		}
	}
	// ---------------------------
	return nil
}

// ---------------------------

func (s *Shard) UpsertPoints(points []models.Point) (map[uuid.UUID]error, error) {
	// ---------------------------
	if config.Cfg.Debug {
		profileFile, _ := os.Create("dump/cpu.prof")
		defer profileFile.Close()
		pprof.StartCPUProfile(profileFile)
		defer pprof.StopCPUProfile()
	}
	// ---------------------------
	log.Debug().Str("component", "shard").Int("count", len(points)).Msg("UpsertPoints")
	// ---------------------------
	// Upsert points
	results := make(map[uuid.UUID]error)
	bar := progressbar.Default(int64(len(points)))
	// ---------------------------
	// Write points to shard
	err := s.db.Update(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte("points"))
		pc := NewPointCache(b)
		// ---------------------------
		currentTime := time.Now()
		// First pass, check for updates
		toPrune := make(map[uuid.UUID]struct{})
		updateSet := make(map[uuid.UUID]struct{})
		updateList := make([]*CachePoint, 0)
		for _, point := range points {
			cachePoint, err := pc.GetPoint(point.Id)
			if err != nil {
				// point does not exist, insert
				continue
			}
			// We need to remove the incoming edges from neighbours and
			// re-insert.
			updateList = append(updateList, cachePoint)
		}
		for _, point := range updateList {
			updateSet[point.Id] = struct{}{}
			for _, edgeId := range point.Edges {
				// If neighbour is also being updated, we don't need to prune
				if _, ok := updateSet[edgeId]; !ok {
					toPrune[edgeId] = struct{}{}
				}
			}
		}
		for pointId := range toPrune {
			if err := s.pruneDeleteNeighbour(pc, pointId, updateSet); err != nil {
				log.Debug().Err(err).Msg("could not prune delete neighbour")
				return fmt.Errorf("could not prune delete neighbour: %w", err)
			}
		}
		log.Debug().Str("component", "shard").Str("duration", time.Since(currentTime).String()).Int("count", len(updateList)).Msg("UpsertPoints - Update")
		// ---------------------------
		// Insert points
		for _, point := range points {
			if err := s.insertPoint(pc, s.startId, ShardPoint{Point: point}); err != nil {
				log.Debug().Err(err).Msg("could not insert point")
				return fmt.Errorf("could not insert point: %w", err)
			}
			bar.Add(1)
		}
		// ---------------------------
		currentTime = time.Now()
		err := pc.Flush()
		log.Debug().Str("component", "shard").Str("duration", time.Since(currentTime).String()).Msg("UpsertPoints - Flush")
		return err
	})
	if err != nil {
		log.Debug().Err(err).Msg("could not insert point")
		return nil, fmt.Errorf("could not insert point: %w", err)
	}
	// ---------------------------
	bar.Close()
	// ---------------------------
	return results, nil
}

// ---------------------------

func (s *Shard) SearchPoints(query []float32, k int) ([]models.Point, error) {
	// ---------------------------
	// Perform search
	var searchSet DistSet
	err := s.db.View(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte("points"))
		pc := NewPointCache(b)
		var err error
		searchSet, _, err = s.greedySearch(pc, s.startId, query, k, s.collection.Parameters.SearchSize)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("could not perform search: %w", err)
	}
	// ---------------------------
	// Clean up results and backfill metadata
	searchSet.KeepFirstK(k)
	results := make([]models.Point, len(searchSet.items))
	err = s.db.View(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte("points"))
		for i, distElem := range searchSet.items {
			results[i] = distElem.point.Point
			mdata, err := getPointMetadata(b, distElem.point.Id)
			if err != nil {
				return fmt.Errorf("could not get point metadata: %w", err)
			}
			results[i].Metadata = mdata
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("could not get point metadata: %w", err)
	}
	return results, nil
}

// ---------------------------

func (s *Shard) pruneDeleteNeighbour(pc *PointCache, id uuid.UUID, deleteSet map[uuid.UUID]struct{}) error {
	// ---------------------------
	point, err := pc.GetPoint(id)
	if err != nil {
		return fmt.Errorf("could not get point: %w", err)
	}
	// ---------------------------
	pointNeighbours, err := pc.GetPointNeighbours(point)
	if err != nil {
		return fmt.Errorf("could not get point neighbours: %w", err)
	}
	// ---------------------------
	// We are going to build a new candidate list of neighbours and then robust
	// prune it
	candidateSet := NewDistSet(point.Vector, len(point.Edges)*2, s.distFn)
	for _, neighbour := range pointNeighbours {
		if _, ok := deleteSet[neighbour.Id]; ok {
			// Pull the neighbours of the deleted neighbour, and add them to the candidate set
			deletedPoint, err := pc.GetPoint(neighbour.Id)
			if err != nil {
				return fmt.Errorf("could not get deleted point: %w", err)
			}
			deletedPointNeighbours, err := pc.GetPointNeighbours(deletedPoint)
			if err != nil {
				return fmt.Errorf("could not get deleted point neighbours: %w", err)
			}
			candidateSet.AddPoint(deletedPointNeighbours...)
		} else {
			candidateSet.AddPoint(neighbour)
		}
	}
	candidateSet.Sort()
	// ---------------------------
	s.robustPrune(point, candidateSet, s.collection.Parameters.Alpha, s.collection.Parameters.DegreeBound)
	// ---------------------------
	return nil
}

// ---------------------------

func (s *Shard) DeletePoints(deleteSet map[uuid.UUID]struct{}) error {
	// ---------------------------
	err := s.db.Update(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte("points"))
		pc := NewPointCache(b)
		// ---------------------------
		// Collect all the neighbours of the points to be deleted
		toPrune := make(map[uuid.UUID]struct{}, len(deleteSet))
		for pointId := range deleteSet {
			point, err := pc.GetPoint(pointId)
			if err != nil {
				// If the point doesn't exist, we can skip it
				log.Debug().Err(err).Msg("could not get point for deletion")
				continue
			}
			point.isDeleted = true
			for _, edgeId := range point.Edges {
				if _, ok := deleteSet[edgeId]; !ok {
					toPrune[edgeId] = struct{}{}
				}
			}
		}
		// ---------------------------
		for pointId := range toPrune {
			if err := s.pruneDeleteNeighbour(pc, pointId, deleteSet); err != nil {
				log.Debug().Err(err).Msg("could not prune delete neighbour")
				return fmt.Errorf("could not prune delete neighbour: %w", err)
			}
		}
		// ---------------------------
		return pc.Flush()
	})
	if err != nil {
		return fmt.Errorf("could not delete points: %w", err)
	}
	return nil
}

// ---------------------------
