package cluster

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/rs/zerolog/log"
	"github.com/semafind/semadb/config"
	"github.com/semafind/semadb/kvstore"
)

// Different key types:
// - U/<user>/C (user collections)
// - U/<user>/C/<collection> (collection key)
// - U/<user>/C/<collection>/P/<point> (point key)

var UserCollectionsRegex = regexp.MustCompile(`^U\/\w+\/C/$`)
var CollectionKeyRegex = regexp.MustCompile(`^U\/\w+\/C\/\w+$`)

func (c *ClusterNode) KeyPlacement(key string) ([]string, error) {
	var servers []string
	switch {
	case UserCollectionsRegex.MatchString(key):
		fallthrough
	case CollectionKeyRegex.MatchString(key):
		parts := strings.Split(key, "/")
		userId := parts[1]
		c.serversMu.RLock()
		servers = RendezvousHash(userId, c.Servers, config.Cfg.GeneralReplication)
		c.serversMu.RUnlock()
	default:
		log.Error().Str("key", key).Msg("Unknown key type")
		return nil, fmt.Errorf("unknown key type %v", key)
	}
	return servers, nil
}

func (c *ClusterNode) ClusterWrite(key string, value []byte) error {
	targetServers, err := c.KeyPlacement(key)
	if err != nil {
		return fmt.Errorf("could not get target servers: %w", err)
	}
	// ---------------------------
	log.Debug().Str("key", key).Strs("targetServers", targetServers).Msg("ClusterWrite")
	results := make(chan error, len(targetServers))
	for _, server := range targetServers {
		go func(dest string) {
			writeKVReq := &WriteKVRequest{
				RequestArgs: RequestArgs{
					Source: c.MyHostname,
					Dest:   dest,
				},
				Key:   key,
				Value: value,
			}
			writeKVResp := &WriteKVResponse{}
			results <- c.RPCWrite(writeKVReq, writeKVResp)
		}(server)
	}
	// ---------------------------
	successCount := 0
	conflictCount := 0
	timeoutCount := 0
	for i := 0; i < len(targetServers); i++ {
		err := <-results
		switch {
		case err == nil:
			successCount++
		case errors.Is(err, kvstore.ErrStaleData):
			conflictCount++
		case errors.Is(err, ErrTimeout):
			timeoutCount++
		default:
			log.Error().Err(err).Msg("NewCollection")
		}
	}
	log.Debug().Int("successCount", successCount).Int("conflictCount", conflictCount).Int("timeoutCount", timeoutCount).Msg("NewCollection")
	// ---------------------------
	switch {
	case conflictCount > 0:
		// We don't need to do anything else here because we know there is a
		// newer version of the collection
		return ErrConflict
	case timeoutCount == len(targetServers):
		// Everything timed out, we can't handoff
		return ErrTimeout
	case successCount == 0:
		// Everything errored out, nothing we can do
		return ErrNoSuccess
	case successCount == len(targetServers):
		// Everything succeeded, nothing to do
		return nil
	}
	// ---------------------------
	return ErrPartialSuccess
}

// ---------------------------

type scanKVResult struct {
	entries []kvstore.KVEntry
	err     error
}

func (c *ClusterNode) ClusterScan(prefix string) ([]kvstore.KVEntry, error) {
	// Where does this prefix belong?
	targetServers, err := c.KeyPlacement(prefix)
	if err != nil {
		return nil, fmt.Errorf("could not get target servers: %w", err)
	}
	log.Debug().Str("prefix", prefix).Strs("targetServers", targetServers).Msg("ClusterScan")
	// ---------------------------
	results := make(chan scanKVResult, len(targetServers))
	for _, server := range targetServers {
		go func(dest string) {
			scanKVReq := &ScanKVRequest{
				RequestArgs: RequestArgs{
					Source: c.MyHostname,
					Dest:   dest,
				},
				Prefix: prefix,
			}
			scanKVResp := new(ScanKVResponse)
			err := c.RPCScan(scanKVReq, scanKVResp)
			results <- scanKVResult{
				entries: scanKVResp.Entries,
				err:     err,
			}
		}(server)
	}
	// ---------------------------
	// Collect results
	var entries []kvstore.KVEntry
	errorCount := 0
	timeoutCount := 0
	for i := 0; i < len(targetServers); i++ {
		result := <-results
		switch {
		case errors.Is(result.err, ErrTimeout):
			timeoutCount++
		case result.err != nil:
			log.Error().Err(result.err).Msg("ClusterScan")
			errorCount++
		default:
			entries = append(entries, result.entries...)
		}
	}
	if timeoutCount == len(targetServers) {
		return nil, ErrTimeout
	}
	if errorCount == len(targetServers) {
		return nil, ErrNoSuccess
	}
	// ---------------------------
	return entries, nil
}
