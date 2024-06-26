package state

import (
	"context"
	"fmt"
	"github.com/hazelcast/hazelcast-go-client"
	log "github.com/sirupsen/logrus"
	"hazeltest/api"
	"hazeltest/client"
	"hazeltest/logging"
	"hazeltest/status"
	"strings"
)

type (
	hzMap interface {
		EvictAll(ctx context.Context) error
		Size(ctx context.Context) (int, error)
	}
	hzQueue interface {
		Clear(ctx context.Context) error
		Size(ctx context.Context) (int, error)
	}
	hzMapStore interface {
		GetMap(ctx context.Context, name string) (hzMap, error)
	}
	hzQueueStore interface {
		GetQueue(ctx context.Context, name string) (hzQueue, error)
	}
	hzObjectInfoStore interface {
		GetDistributedObjectsInfo(ctx context.Context) ([]hzObjectInfo, error)
	}
	hzClientHandler interface {
		getClient() *hazelcast.Client
		client.HzClientInitializer
		client.HzClientCloser
	}
	hzObjectInfo interface {
		getName() string
		getServiceName() string
	}
	defaultHzMapStore struct {
		hzClient *hazelcast.Client
	}
	defaultHzQueueStore struct {
		hzClient *hazelcast.Client
	}
	defaultHzObjectInfoStore struct {
		hzClient *hazelcast.Client
	}
	defaultHzClientHandler struct {
		hzClient *hazelcast.Client
	}
	simpleObjectInfo struct {
		name, serviceName string
	}
)

type (
	cleanerBuilder interface {
		build(ch hzClientHandler, ctx context.Context, g *status.Gatherer, hzCluster string, hzMembers []string) (cleaner, string, error)
	}
	cleaner interface {
		clean(ctx context.Context) (int, error)
	}
	cleanedTracker interface {
		addCleanedDataStructure(name string, cleaned int)
	}
	cleanerConfig struct {
		enabled   bool
		usePrefix bool
		prefix    string
	}
	cleanerConfigBuilder struct {
		keyPath string
		a       client.ConfigPropertyAssigner
	}
	mapCleanerBuilder struct {
		cfb cleanerConfigBuilder
	}
	cleanedDataStructureTracker struct {
		g *status.Gatherer
	}
	mapCleaner struct {
		name      string
		hzCluster string
		hzMembers []string
		keyPath   string
		c         *cleanerConfig
		ms        hzMapStore
		ois       hzObjectInfoStore
		ch        hzClientHandler
		t         cleanedTracker
	}
	queueCleanerBuilder struct {
		cfb cleanerConfigBuilder
	}
	queueCleaner struct {
		name      string
		hzCluster string
		hzMembers []string
		keyPath   string
		c         *cleanerConfig
		qs        hzQueueStore
		ois       hzObjectInfoStore
		ch        hzClientHandler
		t         cleanedTracker
	}
)

const (
	mapCleanerBasePath            = "stateCleaner.maps"
	queueCleanerBasePath          = "stateCleaner.queues"
	hzInternalDataStructurePrefix = "__"
	hzMapService                  = "hz:impl:mapService"
	hzQueueService                = "hz:impl:queueService"
)

var (
	builders []cleanerBuilder
	lp       *logging.LogProvider
)

func init() {
	register(newMapCleanerBuilder())
	register(newQueueCleanerBuilder())
	lp = &logging.LogProvider{ClientID: client.ID()}
}

func newMapCleanerBuilder() *mapCleanerBuilder {

	return &mapCleanerBuilder{
		cfb: cleanerConfigBuilder{
			keyPath: mapCleanerBasePath,
			a:       client.DefaultConfigPropertyAssigner{},
		},
	}

}

func newQueueCleanerBuilder() *queueCleanerBuilder {

	return &queueCleanerBuilder{
		cfb: cleanerConfigBuilder{
			keyPath: queueCleanerBasePath,
			a:       client.DefaultConfigPropertyAssigner{},
		},
	}

}

func register(cb cleanerBuilder) {
	builders = append(builders, cb)
}

func (i simpleObjectInfo) getName() string {

	return i.name

}

func (i simpleObjectInfo) getServiceName() string {

	return i.serviceName

}

func (ms *defaultHzMapStore) GetMap(ctx context.Context, name string) (hzMap, error) {

	return ms.hzClient.GetMap(ctx, name)

}

func (qs *defaultHzQueueStore) GetQueue(ctx context.Context, name string) (hzQueue, error) {

	return qs.hzClient.GetQueue(ctx, name)

}

func (ois *defaultHzObjectInfoStore) GetDistributedObjectsInfo(ctx context.Context) ([]hzObjectInfo, error) {

	infos, err := ois.hzClient.GetDistributedObjectsInfo(ctx)

	if err != nil {
		return nil, err
	}

	var result []hzObjectInfo

	for _, v := range infos {
		i := &simpleObjectInfo{
			name:        v.Name,
			serviceName: v.ServiceName,
		}
		result = append(result, i)
	}

	return result, nil

}

func (ch *defaultHzClientHandler) InitHazelcastClient(ctx context.Context, clientName string, hzCluster string, hzMembers []string) {
	ch.hzClient = client.NewHzClientHelper().AssembleHazelcastClient(ctx, clientName, hzCluster, hzMembers)
}

func (ch *defaultHzClientHandler) Shutdown(ctx context.Context) error {
	return ch.hzClient.Shutdown(ctx)
}

func (ch *defaultHzClientHandler) getClient() *hazelcast.Client {
	return ch.hzClient
}

func (t *cleanedDataStructureTracker) addCleanedDataStructure(name string, cleaned int) {

	t.g.Updates <- status.Update{Key: name, Value: cleaned}

}

func (b *mapCleanerBuilder) build(ch hzClientHandler, ctx context.Context, g *status.Gatherer, hzCluster string, hzMembers []string) (cleaner, string, error) {

	config, err := b.cfb.populateConfig()

	if err != nil {
		lp.LogStateCleanerEvent(fmt.Sprintf("unable to populate state cleaner config for key path '%s' due to error: %v", b.cfb.keyPath, err), hzMapService, log.ErrorLevel)
		return nil, hzMapService, err
	}

	clientName := "mapCleaner"
	ch.InitHazelcastClient(ctx, clientName, hzCluster, hzMembers)

	t := &cleanedDataStructureTracker{g}
	api.RegisterStatefulActor(api.StateCleaners, clientName, t.g.AssembleStatusCopy)

	return &mapCleaner{
		name:      clientName,
		hzCluster: hzCluster,
		hzMembers: hzMembers,
		keyPath:   b.cfb.keyPath,
		c:         config,
		ms:        &defaultHzMapStore{ch.getClient()},
		ois:       &defaultHzObjectInfoStore{ch.getClient()},
		ch:        ch,
		t:         t,
	}, hzMapService, nil

}

func identifyCandidateDataStructures(ois hzObjectInfoStore, ctx context.Context, hzService string) ([]hzObjectInfo, error) {

	infos, err := ois.GetDistributedObjectsInfo(ctx)

	var result []hzObjectInfo
	if err != nil {
		return result, err
	}

	for _, v := range infos {
		if !strings.HasPrefix(v.getName(), hzInternalDataStructurePrefix) && v.getServiceName() == hzService {
			result = append(result, v)
		}
	}

	return result, nil

}

func (c *mapCleaner) retrieveAndClean(name string, ctx context.Context) (int, error) {
	m, err := c.ms.GetMap(ctx, name)
	if err != nil {
		return 0, err
	}
	size, err := m.Size(ctx)
	if err != nil {
		return 0, err
	}
	if size == 0 {
		return 0, nil
	}
	if err := m.EvictAll(ctx); err != nil {
		return 0, err
	}
	c.t.addCleanedDataStructure(name, size)
	return size, nil
}

func (c *mapCleaner) clean(ctx context.Context) (int, error) {

	defer func() {
		_ = c.ch.Shutdown(ctx)
	}()

	if !c.c.enabled {
		lp.LogStateCleanerEvent(fmt.Sprintf("map cleaner '%s' not enabled; won't run", c.name), hzMapService, log.InfoLevel)
		return 0, nil
	}

	numCleaned, err := runGenericClean(
		c.ois,
		ctx,
		hzMapService,
		c.c,
		c.retrieveAndClean,
	)

	return numCleaned, err

}

func (b *queueCleanerBuilder) build(ch hzClientHandler, ctx context.Context, g *status.Gatherer, hzCluster string, hzMembers []string) (cleaner, string, error) {

	config, err := b.cfb.populateConfig()

	if err != nil {
		lp.LogStateCleanerEvent(fmt.Sprintf("unable to populate state cleaner config for key path '%s' due to error: %v", b.cfb.keyPath, err), hzQueueService, log.ErrorLevel)
		return nil, hzQueueService, err
	}

	clientName := "queueCleaner"
	ch.InitHazelcastClient(ctx, clientName, hzCluster, hzMembers)

	t := &cleanedDataStructureTracker{g}
	api.RegisterStatefulActor(api.StateCleaners, clientName, t.g.AssembleStatusCopy)

	return &queueCleaner{
		name:      clientName,
		hzCluster: hzCluster,
		hzMembers: hzMembers,
		keyPath:   b.cfb.keyPath,
		c:         config,
		qs:        &defaultHzQueueStore{hzClient: ch.getClient()},
		ois:       &defaultHzObjectInfoStore{hzClient: ch.getClient()},
		ch:        ch,
		t:         t,
	}, hzQueueService, nil

}

func runGenericClean(
	ois hzObjectInfoStore,
	ctx context.Context,
	hzService string,
	c *cleanerConfig,
	retrieveAndClean func(name string, ctx context.Context) (int, error),
) (int, error) {

	candidateDataStructures, err := identifyCandidateDataStructures(ois, ctx, hzService)

	if err != nil {
		return 0, err
	}

	if len(candidateDataStructures) > 0 {
		lp.LogStateCleanerEvent(fmt.Sprintf("identified %d data structure candidate/-s to be considered for state cleaning", len(candidateDataStructures)), hzService, log.TraceLevel)
	} else {
		lp.LogStateCleanerEvent("no data structure candidates for state cleaning identified in target hazelcast cluster", hzService, log.TraceLevel)
		return 0, nil
	}

	var filteredDataStructures []hzObjectInfo
	if c.usePrefix {
		lp.LogStateCleanerEvent(fmt.Sprintf("applying prefix '%s' to %d data structure candidate/-s identified for cleaning", c.prefix, len(candidateDataStructures)), hzService, log.TraceLevel)
		for _, v := range candidateDataStructures {
			if strings.HasPrefix(v.getName(), c.prefix) {
				filteredDataStructures = append(filteredDataStructures, v)
			}
		}
	} else {
		filteredDataStructures = candidateDataStructures
	}

	numCleanedDataStructures := 0
	for _, v := range filteredDataStructures {
		if numCleanedElements, err := retrieveAndClean(v.getName(), ctx); err != nil {
			lp.LogStateCleanerEvent(fmt.Sprintf("cannot clean data structure '%s' due to error upon retrieval of proxy object from Hazelcast cluster: %v", v, err), hzService, log.ErrorLevel)
			return numCleanedDataStructures, err
		} else if numCleanedElements > 0 {
			lp.LogStateCleanerEvent(fmt.Sprintf("data structure '%s' successfully cleaned", v), hzService, log.InfoLevel)
		} else {
			lp.LogStateCleanerEvent(fmt.Sprintf("no state to clean in data structure '%s'", v), hzService, log.InfoLevel)
		}
		numCleanedDataStructures++
	}

	return numCleanedDataStructures, nil

}

func (c *queueCleaner) retrieveAndClean(name string, ctx context.Context) (int, error) {

	q, err := c.qs.GetQueue(ctx, name)
	if err != nil {
		return 0, err
	}
	size, err := q.Size(ctx)
	if err != nil {
		return 0, err
	}
	if size == 0 {
		return 0, nil
	}
	if err := q.Clear(ctx); err != nil {
		return 0, err
	}
	c.t.addCleanedDataStructure(name, size)
	return size, nil

}

func (c *queueCleaner) clean(ctx context.Context) (int, error) {

	defer func() {
		_ = c.ch.Shutdown(ctx)
	}()

	if !c.c.enabled {
		lp.LogStateCleanerEvent(fmt.Sprintf("queue cleaner '%s' not enabled; won't run", c.name), hzQueueService, log.InfoLevel)
		return 0, nil
	}

	numCleaned, err := runGenericClean(
		c.ois,
		ctx,
		hzQueueService,
		c.c,
		c.retrieveAndClean,
	)

	return numCleaned, err

}

func RunCleaners(hzCluster string, hzMembers []string) error {

	for _, b := range builders {

		g := status.NewGatherer()
		go g.Listen()

		ctx, cancel := context.WithCancel(context.Background())
		err := func() error {
			defer func() {
				cancel()
				g.StopListen()
			}()

			c, hzService, err := b.build(&defaultHzClientHandler{}, ctx, g, hzCluster, hzMembers)
			if err != nil {
				lp.LogStateCleanerEvent(fmt.Sprintf("unable to construct state cleaning builder for hazelcast due to error: %v", err), hzService, log.ErrorLevel)
				return err
			}

			if numCleanedDataStructures, err := c.clean(ctx); err != nil {
				if numCleanedDataStructures > 0 {
					lp.LogStateCleanerEvent(fmt.Sprintf("%d data structure/-s were cleaned before encountering error: %v", numCleanedDataStructures, err), hzService, log.ErrorLevel)
				} else {
					lp.LogStateCleanerEvent(fmt.Sprintf("encountered error upon attempt to clean data structures: %v", err), hzService, log.ErrorLevel)
				}
				return err
			} else {
				if numCleanedDataStructures > 0 {
					lp.LogStateCleanerEvent(fmt.Sprintf("successfully cleaned state in %d data structures", numCleanedDataStructures), hzService, log.InfoLevel)
				} else {
					lp.LogStateCleanerEvent("execution of cleaner was successful; however, zero data structures were cleaned", hzService, log.InfoLevel)
				}
			}

			return nil
		}()

		<-ctx.Done()

		if err != nil {
			return err
		}

	}

	return nil

}

func (b cleanerConfigBuilder) populateConfig() (*cleanerConfig, error) {

	var assignmentOps []func() error

	var enabled bool
	assignmentOps = append(assignmentOps, func() error {
		return b.a.Assign(b.keyPath+".enabled", client.ValidateBool, func(a any) {
			enabled = a.(bool)
		})
	})

	var usePrefix bool
	assignmentOps = append(assignmentOps, func() error {
		return b.a.Assign(b.keyPath+".prefix.enabled", client.ValidateBool, func(a any) {
			usePrefix = a.(bool)
		})
	})

	var prefix string
	assignmentOps = append(assignmentOps, func() error {
		return b.a.Assign(b.keyPath+".prefix.prefix", client.ValidateString, func(a any) {
			prefix = a.(string)
		})
	})

	for _, f := range assignmentOps {
		if err := f(); err != nil {
			return nil, err
		}
	}

	return &cleanerConfig{
		enabled:   enabled,
		usePrefix: usePrefix,
		prefix:    prefix,
	}, nil

}
