package maps

import (
	"context"
	"fmt"
	"hazeltest/api"
	"hazeltest/client"
	"math/rand"
	"sync"
	"time"

	"github.com/google/uuid"
	log "github.com/sirupsen/logrus"
)

type (
	getElementID       func(element interface{}) string
	deserializeElement func(element interface{}) error
	looper[t any]      interface {
		init(lc *testLoopConfig[t], sg *statusGatherer)
		run()
	}
	testLoop[t any] struct {
		config *testLoopConfig[t]
		sg     *statusGatherer
	}
	testLoopConfig[t any] struct {
		id                     uuid.UUID
		source                 string
		mapStore               hzMapStore
		runnerConfig           *runnerConfig
		elements               []t
		ctx                    context.Context
		getElementIdFunc       getElementID
		deserializeElementFunc deserializeElement
	}
	statusElement struct {
		key   string
		value interface{}
	}
	statusGatherer struct {
		status   sync.Map
		elements chan statusElement
	}
)

const (
	statusKeyNumMaps        = "numMaps"
	statusKeyNumRuns        = "numRuns"
	statusKeyTotalRuns      = "totalRuns"
	statusKeyRunnerFinished = "runnerFinished"
)

var (
	quitStatusGathering = statusElement{}
)

func (sg *statusGatherer) getStatus() *sync.Map {

	return &sg.status

}

func (sg *statusGatherer) gather() {

	for {
		element := <-sg.elements
		if element == quitStatusGathering {
			sg.status.Store(statusKeyRunnerFinished, true)
			close(sg.elements)
			return
		} else {
			sg.status.Store(element.key, element.value)
		}
	}

}

func (l *testLoop[t]) init(lc *testLoopConfig[t], sg *statusGatherer) {
	l.config = lc
	l.sg = sg
	api.RegisterRunner(lc.id, l.sg.getStatus)
}

func (l *testLoop[t]) run() {

	go l.sg.gather()
	defer func() {
		l.sg.elements <- quitStatusGathering
	}()

	l.insertLoopWithInitialStatus()

	var wg sync.WaitGroup
	for i := 0; i < l.config.runnerConfig.numMaps; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			mapName := l.assembleMapName(i)
			lp.LogInternalStateEvent(fmt.Sprintf("using map name '%s' in map goroutine %d", mapName, i), log.InfoLevel)
			start := time.Now()
			m, err := l.config.mapStore.GetMap(l.config.ctx, mapName)
			if err != nil {
				lp.LogHzEvent(fmt.Sprintf("unable to retrieve map '%s' from hazelcast: %s", mapName, err), log.ErrorLevel)
				return
			}
			defer func() {
				_ = m.Destroy(l.config.ctx)
			}()
			elapsed := time.Since(start).Milliseconds()
			lp.LogTimingEvent("getMap()", mapName, int(elapsed), log.InfoLevel)
			l.runForMap(m, mapName, i)
		}(i)
	}
	wg.Wait()

}

func (l *testLoop[t]) insertLoopWithInitialStatus() {

	c := l.config

	// Insert initial state synchronously -- other goroutines starting afterwards might have to rely on it,
	// so better incur additional processing time for synchronous initial insertion rather than build around
	// possibility initial state has not been fully provided
	numMaps := c.runnerConfig.numMaps
	numRuns := c.runnerConfig.numRuns

	l.sg.status.Store(statusKeyNumMaps, numMaps)
	l.sg.status.Store(statusKeyNumRuns, numRuns)
	l.sg.status.Store(statusKeyTotalRuns, uint32(numMaps)*numRuns)
	l.sg.status.Store(statusKeyRunnerFinished, false)

}

func (l testLoop[t]) runForMap(m hzMap, mapName string, mapNumber int) {

	updateStep := uint32(50)
	sleepBetweenActionBatchesConfig := l.config.runnerConfig.sleepBetweenActionBatches
	sleepBetweenRunsConfig := l.config.runnerConfig.sleepBetweenRuns

	for i := uint32(0); i < l.config.runnerConfig.numRuns; i++ {
		sleep(sleepBetweenRunsConfig)
		if i > 0 && i%updateStep == 0 {
			lp.LogInternalStateEvent(fmt.Sprintf("finished %d of %d runs for map %s in map goroutine %d", i, l.config.runnerConfig.numRuns, mapName, mapNumber), log.InfoLevel)
		}
		lp.LogInternalStateEvent(fmt.Sprintf("in run %d on map %s in map goroutine %d", i, mapName, mapNumber), log.TraceLevel)
		err := l.ingestAll(m, mapName, mapNumber)
		if err != nil {
			lp.LogHzEvent(fmt.Sprintf("failed to ingest data into map '%s' in run %d: %s", mapName, i, err), log.WarnLevel)
			continue
		}
		sleep(sleepBetweenActionBatchesConfig)
		err = l.readAll(m, mapName, mapNumber)
		if err != nil {
			lp.LogHzEvent(fmt.Sprintf("failed to read data from map '%s' in run %d: %s", mapName, i, err), log.WarnLevel)
			continue
		}
		sleep(sleepBetweenActionBatchesConfig)
		err = l.removeSome(m, mapName, mapNumber)
		if err != nil {
			lp.LogHzEvent(fmt.Sprintf("failed to delete data from map '%s' in run %d: %s", mapName, i, err), log.WarnLevel)
			continue
		}
	}

	lp.LogInternalStateEvent(fmt.Sprintf("map test loop done on map '%s' in map goroutine %d", mapName, mapNumber), log.InfoLevel)

}

func (l testLoop[t]) ingestAll(m hzMap, mapName string, mapNumber int) error {

	numNewlyIngested := 0
	for _, v := range l.config.elements {
		key := assembleMapKey(mapNumber, l.config.getElementIdFunc(v))
		containsKey, err := m.ContainsKey(l.config.ctx, key)
		if err != nil {
			return err
		}
		if containsKey {
			continue
		}
		if err = m.Set(l.config.ctx, key, v); err != nil {
			return err
		}
		numNewlyIngested++
	}

	lp.LogInternalStateEvent(fmt.Sprintf("stored %d items in hazelcast map '%s'", numNewlyIngested, mapName), log.TraceLevel)

	return nil

}

func (l testLoop[t]) readAll(m hzMap, mapName string, mapNumber int) error {

	for _, v := range l.config.elements {
		key := assembleMapKey(mapNumber, l.config.getElementIdFunc(v))
		valueFromHZ, err := m.Get(l.config.ctx, key)
		if err != nil {
			return err
		}
		if valueFromHZ == nil {
			return fmt.Errorf("value retrieved from hazelcast for key '%s' was nil -- value might have been evicted or expired in hazelcast", key)
		}
		err = l.config.deserializeElementFunc(valueFromHZ)
		if err != nil {
			return err
		}
	}

	lp.LogInternalStateEvent(fmt.Sprintf("retrieved %d items from hazelcast map '%s'", len(l.config.elements), mapName), log.TraceLevel)

	return nil

}

func (l testLoop[t]) removeSome(m hzMap, mapName string, mapNumber int) error {

	numElementsToDelete := rand.Intn(len(l.config.elements))
	removed := 0

	elements := l.config.elements

	for i := 0; i < numElementsToDelete; i++ {
		key := assembleMapKey(mapNumber, l.config.getElementIdFunc(elements[i]))
		containsKey, err := m.ContainsKey(l.config.ctx, key)
		if err != nil {
			return err
		}
		if !containsKey {
			continue
		}
		_, err = m.Remove(l.config.ctx, key)
		if err != nil {
			return err
		}
		removed++
	}

	lp.LogInternalStateEvent(fmt.Sprintf("removed %d elements from hazelcast map '%s'", removed, mapName), log.TraceLevel)

	return nil

}

func (l testLoop[t]) assembleMapName(mapIndex int) string {

	c := l.config

	mapName := c.runnerConfig.mapBaseName
	if c.runnerConfig.useMapPrefix && c.runnerConfig.mapPrefix != "" {
		mapName = fmt.Sprintf("%s%s", c.runnerConfig.mapPrefix, mapName)
	}
	if c.runnerConfig.appendMapIndexToMapName {
		mapName = fmt.Sprintf("%s-%d", mapName, mapIndex)
	}
	if c.runnerConfig.appendClientIdToMapName {
		mapName = fmt.Sprintf("%s-%s", mapName, client.ID())
	}

	return mapName

}

func sleep(sleepConfig *sleepConfig) {

	if sleepConfig.enabled {
		var sleepDuration int
		if sleepConfig.enableRandomness {
			sleepDuration = rand.Intn(sleepConfig.durationMs + 1)
		} else {
			sleepDuration = sleepConfig.durationMs
		}
		lp.LogInternalStateEvent(fmt.Sprintf("sleeping for %d milliseconds", sleepDuration), log.TraceLevel)
		time.Sleep(time.Duration(sleepDuration) * time.Millisecond)
	}

}

func assembleMapKey(mapNumber int, elementID string) string {

	return fmt.Sprintf("%s-%d-%s", client.ID(), mapNumber, elementID)

}
