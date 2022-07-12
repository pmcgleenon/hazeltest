package client

import (
	"context"
	"fmt"
	"github.com/google/uuid"
	"hazeltest/client/config"
	"hazeltest/logging"

	"github.com/hazelcast/hazelcast-go-client"
	log "github.com/sirupsen/logrus"
)

type HzClientHelper struct {
	clientID uuid.UUID
	lp       *logging.LogProvider
}

func NewHzClient() HzClientHelper {
	return HzClientHelper{clientID, &logging.LogProvider{ClientID: clientID}}
}

func (h HzClientHelper) InitHazelcastClient(ctx context.Context, runnerName string, hzCluster string, hzMembers []string) *hazelcast.Client {

	hzConfig := &hazelcast.Config{}
	hzConfig.ClientName = fmt.Sprintf("%s-%s", h.clientID, runnerName)
	hzConfig.Cluster.Name = hzCluster

	useUniSocketClient, ok := config.RetrieveArgValue(config.ArgUseUniSocketClient).(bool)
	if !ok {
		logConfigurationError(config.ArgUseUniSocketClient, "command line", "unable to convert value into bool -- using default instead")
		useUniSocketClient = false
	}
	hzConfig.Cluster.Unisocket = useUniSocketClient

	logInternalStateInfo(fmt.Sprintf("hazelcast client config: %+v", hzConfig))

	hzConfig.Cluster.Network.SetAddresses(hzMembers...)

	hzClient, err := hazelcast.StartNewClientWithConfig(ctx, *hzConfig)

	if err != nil {
		h.lp.LogHzEvent(fmt.Sprintf("unable to initialize hazelcast client: %s", err), log.FatalLevel)
		return nil
	}

	return hzClient

}

func logInternalStateInfo(msg string) {

	log.WithFields(log.Fields{
		"kind":   logging.InternalStateInfo,
		"client": ID(),
	}).Info(msg)

}

func logConfigurationError(configValue string, source string, msg string) {

	log.WithFields(log.Fields{
		"kind":   logging.ConfigurationError,
		"value":  configValue,
		"source": source,
		"client": ID(),
	}).Warn(msg)

}
