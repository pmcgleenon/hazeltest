package maps

import (
	"context"
	"encoding/gob"
	"errors"
	"fmt"
	"github.com/google/uuid"
	log "github.com/sirupsen/logrus"
	"hazeltest/api"
	"hazeltest/client"
	"hazeltest/loadsupport"
	"strconv"
)

type (
	loadRunner struct {
		stateList []state
		name      string
		source    string
		mapStore  client.HzMapStore
		l         looper[loadElement]
	}
	loadElement struct {
		Key     string
		Payload string
	}
)

var (
	numEntriesPerMap int
	payloadSizeBytes int
)

func init() {
	register(&loadRunner{stateList: []state{}, name: "maps-loadrunner", source: "loadrunner", mapStore: client.DefaultHzMapStore{}, l: testLoop[loadElement]{}})
	gob.Register(loadElement{})
}

func (r *loadRunner) runMapTests(hzCluster string, hzMembers []string) {

	r.appendState(start)

	loadRunnerConfig, err := populateLoadConfig(propertyAssigner)
	if err != nil {
		lp.LogInternalStateEvent("unable to populate config for map load runner -- aborting", log.ErrorLevel)
		return
	}
	r.appendState(populateConfigComplete)

	if !loadRunnerConfig.enabled {
		// The source field being part of the generated log line can be used to disambiguate queues/loadrunner from maps/loadrunner
		lp.LogInternalStateEvent("loadrunner not enabled -- won't run", log.InfoLevel)
		return
	}
	r.appendState(checkEnabledComplete)

	api.RaiseNotReady()

	ctx := context.TODO()

	r.mapStore.InitHazelcastClient(ctx, "maps-loadrunner", hzCluster, hzMembers)
	defer r.mapStore.Shutdown(ctx)

	api.RaiseReady()
	r.appendState(raiseReadyComplete)

	lp.LogInternalStateEvent("initialized hazelcast client", log.InfoLevel)
	lp.LogInternalStateEvent("starting load test loop for maps", log.InfoLevel)

	lc := &testLoopConfig[loadElement]{uuid.New(), r.source, r.mapStore, loadRunnerConfig, populateLoadElements(), ctx, getLoadElementID, deserializeLoadElement}

	r.l.init(lc)

	r.appendState(testLoopStart)
	r.l.run()
	r.appendState(testLoopComplete)

	lp.LogInternalStateEvent("finished map load test loop", log.InfoLevel)

}

func (r *loadRunner) appendState(s state) {

	r.stateList = append(r.stateList, s)

}

func populateLoadElements() []loadElement {

	elements := make([]loadElement, numEntriesPerMap)
	// Depending on the value of 'payloadSizeBytes', this string can get very large, and to generate one
	// unique string for each map entry will result in high memory consumption of this Hazeltest client.
	// Thus, we use one random string for each map and reference that string in each load element
	randomPayload := loadsupport.GenerateRandomStringPayload(payloadSizeBytes)

	for i := 0; i < numEntriesPerMap; i++ {
		elements[i] = loadElement{
			Key:     strconv.Itoa(i),
			Payload: randomPayload,
		}
	}

	return elements

}

func getLoadElementID(element interface{}) string {

	loadElement := element.(loadElement)
	return loadElement.Key

}

func deserializeLoadElement(elementFromHz interface{}) error {

	_, ok := elementFromHz.(loadElement)

	if !ok {
		return errors.New("unable to deserialize value retrieved from hazelcast map into loadelement instance")
	}

	return nil

}

func populateLoadConfig(a configPropertyAssigner) (*runnerConfig, error) {

	runnerKeyPath := "maptests.load"

	if err := a.Assign(runnerKeyPath+".numEntriesPerMap", func(path string, a any) error {
		if i, ok := a.(int); !ok {
			return fmt.Errorf(templateIntParseError, path, a)
		} else if i <= 0 {
			return fmt.Errorf(templateNumberAtLeastOneError, path, i)
		} else {
			numEntriesPerMap = i
			return nil
		}
	}); err != nil {
		return nil, err
	}

	if err := a.Assign(runnerKeyPath+".payloadSizeBytes", func(path string, a any) error {
		if i, ok := a.(int); !ok {
			return fmt.Errorf(templateIntParseError, path, a)
		} else if i <= 0 {
			return fmt.Errorf(templateNumberAtLeastOneError, path, i)
		} else {
			payloadSizeBytes = i
			return nil
		}
	}); err != nil {
		return nil, err
	}

	configBuilder := runnerConfigBuilder{
		runnerKeyPath: runnerKeyPath,
		mapBaseName:   "load",
	}
	return configBuilder.populateConfig()

}
