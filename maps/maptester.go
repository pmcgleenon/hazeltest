package maps

import (
	"fmt"
	"hazeltest/client"
	"hazeltest/logging"
	"sync"

	log "github.com/sirupsen/logrus"
)

type MapTester struct {
	HzCluster string
	HzMembers []string
}

func (t *MapTester) TestMaps() {

	clientID := client.ClientID()
	logInternalStateInfo(fmt.Sprintf("%s: maptester starting %d runner/-s", clientID, len(MapRunners)))

	var wg sync.WaitGroup
	for i := 0; i < len(MapRunners); i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			runner := MapRunners[i]
			runner.Run(t.HzCluster, t.HzMembers)
		}(i)
	}

	wg.Wait()

}

func logInternalStateInfo(msg string) {

	log.WithFields(log.Fields{
		"kind": logging.InternalStateInfo,
		"client": client.ClientID(),
	}).Trace(msg)

}
