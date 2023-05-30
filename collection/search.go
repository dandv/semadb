package collection

import (
	"fmt"
)

func eucDist(x, y []float32) float32 {
	var sum float32
	for i := range x {
		diff := x[i] - y[i]
		sum += diff * diff
	}
	return sum
}

func (c *Collection) greedySearch(startNodeId string, query []float32, k int, searchSize int) (*DistSet, *DistSet, error) {
	// ---------------------------
	// Check that the search size is greater than k
	if searchSize < k {
		return nil, nil, fmt.Errorf("searchSize (%d) must be greater than k (%d)", searchSize, k)
	}
	// ---------------------------
	// Initialise distance set
	searchSet := NewDistSet(query, searchSize*2)
	visitedSet := NewDistSet(query, searchSize*2)
	// ---------------------------
	// Get the start node
	startNodeEmbedding, err := c.getNodeEmbedding(startNodeId)
	if err != nil {
		return nil, nil, fmt.Errorf("could not get start node embedding: %v", err)
	}
	searchSet.AddEntry(Entry{Id: startNodeId, Embedding: startNodeEmbedding})
	// ---------------------------
	/* This loop looks to curate the closest nodes to the query vector along the
	 * way. It is usually implemented with two sets, we try to merged them into
	 * one array with set semantics. The loop terminates when we visited all the
	 * nodes in our search list. */
	for i := 0; i < searchSet.Len(); {
		node := searchSet.items[i]
		if node.visited {
			i++
			continue
		}
		node.visited = true
		visitedSet.Add(node)
		neighbours, err := c.getNodeNeighbours(node.id)
		if err != nil {
			return nil, nil, fmt.Errorf("could not get node (%v) neighbours: %v", node.id, err)
		}
		searchSet.AddEntry(neighbours...)
		searchSet.Sort()
		if searchSet.Len() > searchSize {
			searchSet.KeepFirstK(searchSize)
		}
		i = 0
	}
	// ---------------------------
	visitedSet.Sort()
	return searchSet, visitedSet, nil
}

func (c *Collection) robustPrune(node Entry, candidateSet *DistSet, alpha float32, degreeBound int) ([]string, error) {
	// ---------------------------
	// Get the node neighbours
	nodeNeighbours, err := c.getNodeNeighbours(node.Id)
	if err != nil {
		return nil, fmt.Errorf("could not get node (%v) neighbours for pruning: %v", node.Id, err)
	}
	// ---------------------------
	// Merge neighbours into candidate set
	candidateSet.AddEntry(nodeNeighbours...)
	candidateSet.Sort()
	candidateSet.Remove(node.Id) // Exclude the node itself
	// ---------------------------
	// We will overwrite existing neighbours
	newNeighours := make([]string, 0, degreeBound)
	// ---------------------------
	for candidateSet.Len() > 0 {
		// ---------------------------
		// Get the closest node
		closestElem := candidateSet.Pop()
		newNeighours = append(newNeighours, closestElem.id)
		if len(newNeighours) >= degreeBound {
			break
		}
		// ---------------------------
		// Prune optimisitically
		for _, cand := range candidateSet.items {
			// We currently do this check because remove doesn't handle re-ordering for performance purposes
			if !candidateSet.Contains(cand.id) {
				continue
			}
			// ---------------------------
			if alpha*eucDist(closestElem.embedding, cand.embedding) <= cand.distance {
				candidateSet.Remove(cand.id)
			}
		}
	}
	// ---------------------------
	return newNeighours, nil
}
