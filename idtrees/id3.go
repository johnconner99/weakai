package idtrees

import (
	"math"
	"runtime"
	"sort"
	"sync"
)

// ID3 generates a Tree using the ID3 algorithm.
//
// The maxGos argument specifies the maximum number
// of Goroutines to use during tree generation.
// If maxGos is 0, then GOMAXPROCS is used.
func ID3(samples []Sample, attrs []string, maxGos int) *Tree {
	if maxGos == 0 {
		maxGos = runtime.GOMAXPROCS(0)
	}
	baseEntropy := newEntropyCounter(samples).Entropy()
	return id3(samples, attrs, maxGos, baseEntropy)
}

func id3(samples []Sample, attrs []string, maxGos int, entropy float64) *Tree {
	if entropy == 0 {
		return createLeaf(samples)
	}

	attrChan := make(chan string, len(attrs))
	for _, a := range attrs {
		attrChan <- a
	}
	close(attrChan)

	splitChan := make(chan *potentialSplit)

	var wg sync.WaitGroup
	for i := 0; i < maxGos; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for attr := range attrChan {
				split := createPotentialSplit(samples, attr)
				if split != nil {
					splitChan <- split
				}
			}
		}()
	}
	go func() {
		wg.Wait()
		close(splitChan)
	}()

	var bestSplit *potentialSplit
	for split := range splitChan {
		if bestSplit == nil || split.Entropy < bestSplit.Entropy {
			bestSplit = split
		}
	}
	if bestSplit.Entropy >= entropy {
		return createLeaf(samples)
	}

	if bestSplit.Threshold != nil {
		less := id3(bestSplit.NumSplitSamples[0], attrs, maxGos,
			bestSplit.NumSplitEntropies[0])
		greater := id3(bestSplit.NumSplitSamples[1], attrs, maxGos,
			bestSplit.NumSplitEntropies[0])
		return &Tree{
			Attr: bestSplit.Attr,
			NumSplit: &NumSplit{
				Threshold: bestSplit.Threshold,
				LessEqual: less,
				Greater:   greater,
			},
		}
	}

	res := &Tree{
		Attr:     bestSplit.Attr,
		ValSplit: ValSplit{},
	}
	for class, samples := range bestSplit.ValSplitSamples {
		tree := id3(samples, attrs, maxGos, bestSplit.ValSplitEntropies[class])
		res.ValSplit[class] = tree
	}
	return res
}

func createLeaf(samples []Sample) *Tree {
	counts := map[interface{}]int{}
	for _, s := range samples {
		counts[s.Class()]++
	}
	res := &Tree{Classification: map[interface{}]float64{}}
	totalScaler := 1 / float64(len(samples))
	for class, count := range counts {
		res.Classification[class] = float64(count) * totalScaler
	}
	return res
}

type potentialSplit struct {
	Attr    string
	Entropy float64

	ValSplitEntropies map[interface{}]float64
	ValSplitSamples   map[interface{}][]Sample

	Threshold         interface{}
	NumSplitEntropies [2]float64
	NumSplitSamples   [2][]Sample
}

func createPotentialSplit(samples []Sample, attr string) *potentialSplit {
	if len(samples) == 0 {
		panic("cannot split 0 samples")
	}

	val1 := samples[0].Attr(attr)
	switch val1.(type) {
	case int64:
		return createIntSplit(samples, attr)
	case float64:
		return createFloatSplit(samples, attr)
	}

	res := &potentialSplit{
		Attr:              attr,
		ValSplitEntropies: map[interface{}]float64{},
		ValSplitSamples:   map[interface{}][]Sample{},
	}

	for _, s := range samples {
		c := s.Class()
		res.ValSplitSamples[c] = append(res.ValSplitSamples[c], s)
	}

	totalDivider := 1 / float64(len(samples))
	for class, s := range res.ValSplitSamples {
		e := newEntropyCounter(s).Entropy()
		res.ValSplitEntropies[class] = e
		res.Entropy += float64(len(s)) * totalDivider * e
	}

	return res
}

func createIntSplit(samples []Sample, attr string) *potentialSplit {
	sorter := &intSorter{
		sampleSorter: sampleSorter{
			Attr:    attr,
			Samples: samples,
		},
	}
	sort.Sort(sorter)

	lastValue := sorter.Samples[0].Attr(attr).(int64)
	var cutoffIdxs []int
	var cutoffs []interface{}
	for i := 1; i < len(sorter.Samples); i++ {
		val := sorter.Samples[i].Attr(attr).(int64)
		if val > lastValue {
			cutoffIdxs = append(cutoffIdxs, i)
			cutoffs = append(cutoffs, lastValue+(val-lastValue)/2)
			lastValue = val
		}
	}

	return createNumericSplit(sorter.sampleSorter, cutoffIdxs, cutoffs)
}

func createFloatSplit(samples []Sample, attr string) *potentialSplit {
	sorter := &floatSorter{
		sampleSorter: sampleSorter{
			Attr:    attr,
			Samples: samples,
		},
	}
	sort.Sort(sorter)

	lastValue := sorter.Samples[0].Attr(attr).(float64)
	var cutoffIdxs []int
	var cutoffs []interface{}
	for i := 1; i < len(sorter.Samples); i++ {
		val := sorter.Samples[i].Attr(attr).(float64)
		if val > lastValue {
			cutoffIdxs = append(cutoffIdxs, i)
			cutoffs = append(cutoffs, lastValue+(val-lastValue)/2)
			lastValue = val
		}
	}

	return createNumericSplit(sorter.sampleSorter, cutoffIdxs, cutoffs)
}

func createNumericSplit(s sampleSorter, cutoffIdxs []int, cutoffs []interface{}) *potentialSplit {
	if len(cutoffIdxs) == 0 {
		return nil
	}

	best := &potentialSplit{
		Attr: s.Attr,
	}

	lessEntropy := newEntropyCounter(s.Samples[:cutoffIdxs[0]])
	greaterEntropy := newEntropyCounter(s.Samples[cutoffIdxs[0]:])

	countDivider := 1 / float64(len(s.Samples))
	for i, cutoffIdx := range cutoffIdxs {
		if i != 0 {
			lastIdx := cutoffIdxs[i-1]
			for j := lastIdx; j < cutoffIdx; j++ {
				lessEntropy.Add(s.Samples[j])
				greaterEntropy.Remove(s.Samples[j])
			}
		}
		lessE := lessEntropy.Entropy()
		greaterE := greaterEntropy.Entropy()
		entropy := countDivider * (float64(lessEntropy.totalCount)*lessE +
			float64(greaterEntropy.totalCount)*greaterE)
		if entropy < best.Entropy || i == 0 {
			best.Entropy = entropy
			best.NumSplitEntropies[0] = lessE
			best.NumSplitEntropies[1] = greaterE
			best.NumSplitSamples[0] = s.Samples[:cutoffIdx]
			best.NumSplitSamples[1] = s.Samples[cutoffIdx:]
			best.Threshold = cutoffs[i]
		}
	}

	return best
}

type entropyCounter struct {
	classCounts map[interface{}]int
	totalCount  int
}

func newEntropyCounter(s []Sample) *entropyCounter {
	res := &entropyCounter{
		classCounts: map[interface{}]int{},
		totalCount:  len(s),
	}
	for _, sample := range s {
		res.classCounts[sample.Class()]++
	}
	return res
}

func (e *entropyCounter) Entropy() float64 {
	var entropy float64
	countScaler := 1 / float64(e.totalCount)
	for _, count := range e.classCounts {
		if count == 0 {
			continue
		}
		probability := float64(count) * countScaler
		entropy -= probability * math.Log(probability)
	}
	return entropy
}

func (e *entropyCounter) Add(s Sample) {
	e.classCounts[s.Class()]++
	e.totalCount++
}

func (e *entropyCounter) Remove(s Sample) {
	e.classCounts[s.Class()]--
	e.totalCount--
}

type sampleSorter struct {
	Attr    string
	Samples []Sample
}

func (s *sampleSorter) Len() int {
	return len(s.Samples)
}

func (s *sampleSorter) Swap(i, j int) {
	s.Samples[i], s.Samples[j] = s.Samples[j], s.Samples[i]
}

type intSorter struct {
	sampleSorter
}

func (i *intSorter) Less(k, j int) bool {
	kVal := i.Samples[k].Attr(i.Attr).(int64)
	jVal := i.Samples[j].Attr(i.Attr).(int64)
	return kVal < jVal
}

type floatSorter struct {
	sampleSorter
}

func (f *floatSorter) Less(k, j int) bool {
	kVal := f.Samples[k].Attr(f.Attr).(float64)
	jVal := f.Samples[j].Attr(f.Attr).(float64)
	return kVal < jVal
}
