package cluster

import (
	"fmt"
	"path/filepath"
	"time"

	"github.com/semafind/semadb/config"
	"github.com/semafind/semadb/shard"
)

type loadedShard struct {
	shard     *shard.Shard
	resetChan chan bool
}

func (c *ClusterNode) LoadShard(shardDir string) (*shard.Shard, error) {
	c.logger.Debug().Str("shardDir", shardDir).Msg("LoadShard")
	c.shardLock.Lock()
	defer c.shardLock.Unlock()
	if ls, ok := c.shardStore[shardDir]; ok {
		// We reset the timer here so that the shard is not unloaded prematurely
		c.logger.Debug().Str("shardDir", shardDir).Msg("Returning cached shard")
		ls.resetChan <- true
		return ls.shard, nil
	}
	// ---------------------------
	// Load corresponding collection
	colPath := filepath.Dir(shardDir)
	collectionId := filepath.Base(colPath)
	userPath := filepath.Dir(colPath)
	userId := filepath.Base(userPath)
	col, err := c.GetCollection(userId, collectionId)
	if err != nil {
		return nil, fmt.Errorf("could not load collection: %w", err)
	}
	// ---------------------------
	// Open shard
	shard, err := shard.NewShard(shardDir, col)
	if err != nil {
		return nil, fmt.Errorf("could not open shard: %w", err)
	}
	ls := loadedShard{
		shard:     shard,
		resetChan: make(chan bool),
	}
	c.shardStore[shardDir] = ls
	// ---------------------------
	// Setup cleanup goroutine
	go func() {
		timeoutDuration := time.Duration(config.Cfg.ShardTimeout) * time.Second
		for {
			select {
			case resetVal := <-ls.resetChan:
				if !resetVal {
					c.logger.Debug().Str("shardDir", shardDir).Msg("Exiting shard reset goroutine")
					return
				}
				c.logger.Debug().Str("shardDir", shardDir).Msg("Resetting shard timeout")
			case <-time.After(timeoutDuration):
				c.logger.Debug().Str("shardDir", shardDir).Msg("Unloading shard")
				c.shardLock.Lock()
				if err := ls.shard.Close(); err != nil {
					c.logger.Error().Err(err).Str("shardDir", shardDir).Msg("Failed to close shard")
				} else {
					c.logger.Debug().Str("shardDir", shardDir).Msg("Closed shard")
					delete(c.shardStore, shardDir)
				}
				c.shardLock.Unlock()
				return
			}
		}
	}()
	return shard, nil
}
