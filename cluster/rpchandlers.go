package cluster

import (
	"bytes"
	"fmt"
	"path/filepath"

	"github.com/google/uuid"
	"github.com/semafind/semadb/config"
	"github.com/semafind/semadb/models"
	"github.com/semafind/semadb/shard"
	"github.com/vmihailenco/msgpack/v5"
	"go.etcd.io/bbolt"
)

// ---------------------------

// type rpcPingRequest struct {
// 	rpcRequestArgs
// 	Message string
// }

// type rpcPingResponse struct {
// 	Message string
// }

// func (c *ClusterNode) rpcPing(args *rpcPingRequest, reply *rpcPingResponse) error {
// 	c.logger.Debug().Interface("args", args).Msg("Ping")
// 	if args.Dest != c.MyHostname {
// 		return c.internalRoute("ClusterNode.Ping", args, reply)
// 	}
// 	reply.Message = fmt.Sprintf("Pong from semadb %v, message: %v", c.MyHostname, args.Message)
// 	return nil
// }

// ---------------------------

type RPCCreateCollectionRequest struct {
	RPCRequestArgs
	Collection models.Collection
}

type RPCCreateCollectionResponse struct {
	AlreadyExists bool
}

func (c *ClusterNode) RPCCreateCollection(args *RPCCreateCollectionRequest, reply *RPCCreateCollectionResponse) error {
	c.logger.Debug().Str("collectionId", args.Collection.Id).Msg("RPCCreateCollection")
	if args.Dest != c.MyHostname {
		return c.internalRoute("ClusterNode.RPCCreateCollection", args, reply)
	}
	// ---------------------------
	// Marshal collection
	colBytes, err := msgpack.Marshal(args.Collection)
	if err != nil {
		return fmt.Errorf("could not marshal collection: %w", err)
	}
	// ---------------------------
	err = c.nodedb.Update(func(tx *bbolt.Tx) error {
		b := tx.Bucket(USERCOLSKEY)
		// ---------------------------
		key := []byte(args.Collection.UserId + DBDELIMITER + args.Collection.Id)
		if b.Get(key) != nil {
			reply.AlreadyExists = true
			return nil
		}
		// ---------------------------
		if err := b.Put(key, colBytes); err != nil {
			return fmt.Errorf("could not put collection: %w", err)
		}
		return nil
	})
	return err
}

// ---------------------------

type RPCListCollectionsRequest struct {
	RPCRequestArgs
	UserId string
}

type RPCListCollectionsResponse struct {
	Collections []models.Collection
}

func (c *ClusterNode) RPCListCollections(args *RPCListCollectionsRequest, reply *RPCListCollectionsResponse) error {
	c.logger.Debug().Str("userId", args.UserId).Msg("RPCListCollections")
	if args.Dest != c.MyHostname {
		return c.internalRoute("ClusterNode.RPCListCollections", args, reply)
	}
	reply.Collections = make([]models.Collection, 0)
	err := c.nodedb.View(func(tx *bbolt.Tx) error {
		b := tx.Bucket(USERCOLSKEY)
		// ---------------------------
		c := b.Cursor()
		for k, v := c.Seek([]byte(args.UserId + DBDELIMITER)); k != nil && bytes.HasPrefix(k, []byte(args.UserId)); k, v = c.Next() {
			var col models.Collection
			if err := msgpack.Unmarshal(v, &col); err != nil {
				return fmt.Errorf("could not unmarshal collection %s: %w", k, err)
			}
			reply.Collections = append(reply.Collections, col)
		}
		return nil
	})
	return err
}

// ---------------------------

type RPCGetCollectionRequest struct {
	RPCRequestArgs
	UserId       string
	CollectionId string
}

type RPCGetCollectionResponse struct {
	Collection models.Collection
	NotFound   bool
}

func (c *ClusterNode) RPCGetCollection(args *RPCGetCollectionRequest, reply *RPCGetCollectionResponse) error {
	c.logger.Debug().Str("userId", args.UserId).Str("collectionId", args.CollectionId).Msg("RPCGetCollection")
	if args.Dest != c.MyHostname {
		return c.internalRoute("ClusterNode.RPCGetCollection", args, reply)
	}
	err := c.nodedb.View(func(tx *bbolt.Tx) error {
		b := tx.Bucket(USERCOLSKEY)
		// ---------------------------
		key := []byte(args.UserId + DBDELIMITER + args.CollectionId)
		value := b.Get(key)
		if value == nil {
			reply.NotFound = true
			return nil
		}
		// ---------------------------
		if err := msgpack.Unmarshal(value, &reply.Collection); err != nil {
			return fmt.Errorf("could not unmarshal collection %s: %w", key, err)
		}
		return nil
	})
	return err
}

// ---------------------------

type RPCCreateShardRequest struct {
	RPCRequestArgs
	UserId       string
	CollectionId string
}

type RPCCreateShardResponse struct {
	ShardId string
}

func (c *ClusterNode) RPCCreateShard(args *RPCCreateShardRequest, reply *RPCCreateShardResponse) error {
	c.logger.Debug().Str("userId", args.UserId).Str("collectionId", args.CollectionId).Msg("RPCCreateShard")
	if args.Dest != c.MyHostname {
		return c.internalRoute("ClusterNode.RPCCreateShard", args, reply)
	}
	err := c.nodedb.Update(func(tx *bbolt.Tx) error {
		b := tx.Bucket(USERCOLSKEY)
		// ---------------------------
		key := []byte(args.UserId + DBDELIMITER + args.CollectionId)
		value := b.Get(key)
		if value == nil {
			return fmt.Errorf("collection %s not found", key)
		}
		var col models.Collection
		if err := msgpack.Unmarshal(value, &col); err != nil {
			return fmt.Errorf("could not unmarshal collection %s: %w", key, err)
		}
		// ---------------------------
		shardId := uuid.New().String()
		reply.ShardId = shardId
		col.ShardIds = append(col.ShardIds, shardId)
		// ---------------------------
		colBytes, err := msgpack.Marshal(col)
		if err != nil {
			return fmt.Errorf("could not marshal collection: %w", err)
		}
		// ---------------------------
		if err := b.Put(key, colBytes); err != nil {
			return fmt.Errorf("could not put collection: %w", err)
		}
		// ---------------------------
		return nil
	})
	return err
}

// ---------------------------

type RPCGetShardInfoRequest struct {
	RPCRequestArgs
	UserId       string
	CollectionId string
	ShardId      string
}

type RPCGetShardInfoResponse struct {
	PointCount int64
	Size       int64
}

func (c *ClusterNode) RPCGetShardInfo(args *RPCGetShardInfoRequest, reply *RPCGetShardInfoResponse) error {
	c.logger.Debug().Str("userId", args.UserId).Str("collectionId", args.CollectionId).Str("shardId", args.ShardId).Msg("RPCGetShardInfo")
	if args.Dest != c.MyHostname {
		return c.internalRoute("ClusterNode.RPCGetShardInfo", args, reply)
	}
	// ---------------------------
	shardDir := filepath.Join(config.Cfg.RootDir, args.UserId, args.CollectionId, args.ShardId)
	return c.DoWithShard(shardDir, func(s *shard.Shard) error {
		si, err := s.Info()
		reply.PointCount = si.PointCount
		reply.Size = si.InUse
		return err
	})
}

// ---------------------------

type RPCInsertPointsRequest struct {
	RPCRequestArgs
	UserId       string
	CollectionId string
	ShardId      string
	Points       []models.Point
}

// This response is not really used, but we need to return something otherwise
// enc/glob fails. That is, we can have reply be nil. We can use this response
// in the future.
type RPCInsertPointsResponse struct {
	Count int
}

func (c *ClusterNode) RPCInsertPoints(args *RPCInsertPointsRequest, reply *RPCInsertPointsResponse) error {
	c.logger.Debug().Str("userId", args.UserId).Str("collectionId", args.CollectionId).Str("shardId", args.ShardId).Msg("RPCInsertPoints")
	if args.Dest != c.MyHostname {
		return c.internalRoute("ClusterNode.RPCInsertPoints", args, reply)
	}
	// ---------------------------
	shardDir := filepath.Join(config.Cfg.RootDir, args.UserId, args.CollectionId, args.ShardId)
	return c.DoWithShard(shardDir, func(s *shard.Shard) error {
		reply.Count = len(args.Points)
		return s.InsertPoints(args.Points)
	})
}

// ---------------------------

type RPCUpdatePointsRequest struct {
	RPCRequestArgs
	UserId       string
	CollectionId string
	ShardId      string
	Points       []models.Point
}

type RPCUpdatePointsResponse struct {
	UpdatedIds []uuid.UUID
}

func (c *ClusterNode) RPCUpdatePoints(args *RPCUpdatePointsRequest, reply *RPCUpdatePointsResponse) error {
	c.logger.Debug().Str("userId", args.UserId).Str("collectionId", args.CollectionId).Str("shardId", args.ShardId).Msg("RPCUpdatePoints")
	if args.Dest != c.MyHostname {
		return c.internalRoute("ClusterNode.RPCUpdatePoints", args, reply)
	}
	// ---------------------------
	shardDir := filepath.Join(config.Cfg.RootDir, args.UserId, args.CollectionId, args.ShardId)
	return c.DoWithShard(shardDir, func(s *shard.Shard) error {
		updatedIds, err := s.UpdatePoints(args.Points)
		reply.UpdatedIds = updatedIds
		return err
	})
}

// ---------------------------

type RPCDeletePointsRequest struct {
	RPCRequestArgs
	UserId       string
	CollectionId string
	ShardId      string
	Ids          []uuid.UUID
}

type RPCDeletePointsResponse struct {
	Count int
}

func (c *ClusterNode) RPCDeletePoints(args *RPCDeletePointsRequest, reply *RPCDeletePointsResponse) error {
	c.logger.Debug().Str("userId", args.UserId).Str("collectionId", args.CollectionId).Str("shardId", args.ShardId).Msg("RPCDeletePoints")
	if args.Dest != c.MyHostname {
		return c.internalRoute("ClusterNode.RPCDeletePoints", args, reply)
	}
	// ---------------------------
	deleteSet := make(map[uuid.UUID]struct{})
	for _, id := range args.Ids {
		deleteSet[id] = struct{}{}
	}
	shardDir := filepath.Join(config.Cfg.RootDir, args.UserId, args.CollectionId, args.ShardId)
	return c.DoWithShard(shardDir, func(s *shard.Shard) error {
		reply.Count = len(deleteSet)
		return s.DeletePoints(deleteSet)
	})
}

// ---------------------------

type RPCSearchPointsRequest struct {
	RPCRequestArgs
	UserId       string
	CollectionId string
	ShardId      string
	Vector       []float32
	Limit        int
}

type RPCSearchPointsResponse struct {
	Points []shard.SearchPoint
}

func (c *ClusterNode) RPCSearchPoints(args *RPCSearchPointsRequest, reply *RPCSearchPointsResponse) error {
	c.logger.Debug().Str("userId", args.UserId).Str("collectionId", args.CollectionId).Str("shardId", args.ShardId).Msg("RPCSearchPoints")
	if args.Dest != c.MyHostname {
		return c.internalRoute("ClusterNode.RPCSearchPoints", args, reply)
	}
	// ---------------------------
	shardDir := filepath.Join(config.Cfg.RootDir, args.UserId, args.CollectionId, args.ShardId)
	return c.DoWithShard(shardDir, func(s *shard.Shard) error {
		points, err := s.SearchPoints(args.Vector, args.Limit)
		reply.Points = points
		return err
	})
}

// ---------------------------
