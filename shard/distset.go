package shard

import (
	"cmp"
	"fmt"
	"slices"

	"github.com/google/uuid"
)

type DistSetElem struct {
	point    *CachePoint
	distance float32
}

type DistSet struct {
	items       []DistSetElem
	set         map[uuid.UUID]struct{} // struct{} is a zero byte type, so it takes up no space
	queryVector []float32
	distFn      func([]float32, []float32) float32
}

func NewDistSet(queryVector []float32, capacity int, distFn func([]float32, []float32) float32) DistSet {
	return DistSet{queryVector: queryVector, items: make([]DistSetElem, 0, capacity), set: make(map[uuid.UUID]struct{}, capacity), distFn: distFn}
}

// ---------------------------

func (ds *DistSet) Len() int {
	return len(ds.set)
}

func (ds *DistSet) String() string {
	return fmt.Sprintf("DistSet{items: %+v, set: %+v}", ds.items, ds.set)
}

// ---------------------------

// Adding entries only computes distance if needed
func (ds *DistSet) AddPoint(points ...*CachePoint) {
	for _, p := range points {
		if _, ok := ds.set[p.Id]; ok {
			continue
		}
		ds.set[p.Id] = struct{}{}
		distance := ds.distFn(p.Vector, ds.queryVector)
		ds.items = append(ds.items, DistSetElem{distance: distance, point: p})
	}
}

// Add item to distance set if it is not already present
func (ds *DistSet) Add(items ...DistSetElem) {
	for _, item := range items {
		if _, ok := ds.set[item.point.Id]; ok {
			continue
		}
		ds.set[item.point.Id] = struct{}{}
		ds.items = append(ds.items, item)
	}
}

func (ds *DistSet) Sort() {
	slices.SortFunc(ds.items, func(a, b DistSetElem) int {
		return cmp.Compare(a.distance, b.distance)
	})
}

func (ds *DistSet) Contains(id uuid.UUID) bool {
	_, ok := ds.set[id]
	return ok
}

func (ds *DistSet) Pop() DistSetElem {
	// Find the first element in list that is still in set
	i := 0
	var toReturn DistSetElem
	for ; i < len(ds.items); i++ {
		item := ds.items[i]
		// ds.items[i] = nil // avoid memory leak
		if _, ok := ds.set[item.point.Id]; ok {
			toReturn = item
			delete(ds.set, item.point.Id)
			break
		}
	}
	ds.items = ds.items[(i + 1):]
	return toReturn
}

func (ds *DistSet) KeepFirstK(k int) {
	for i := k; i < len(ds.items); i++ {
		delete(ds.set, ds.items[i].point.Id)
		// ds.items[i] = nil // avoid memory leak
	}
	if k < len(ds.items) {
		ds.items = ds.items[:k]
	}
}

func (ds *DistSet) Remove(id uuid.UUID) {
	delete(ds.set, id)
}

// ---------------------------
