package status

import (
	"sync"
)

type (
	Update struct {
		Key   string
		Value interface{}
	}
	Gatherer struct {
		l      locker
		status map[string]interface{}
		// Not strictly required as of current status gathering needs, but foundation for more sophisticated gathering
		// --> TODO Write issue for that
		updates chan Update
	}
	locker interface {
		lock()
		unlock()
	}
	mutexLocker struct {
		m sync.Mutex
	}
)

const (
	updateKeyRunnerFinished = "runnerFinished"
)

var (
	quitStatusGathering = Update{}
)

func (l *mutexLocker) lock() {

	l.m.Lock()

}

func (l *mutexLocker) unlock() {

	l.m.Unlock()

}

func NewGatherer() *Gatherer {

	return &Gatherer{
		l: &mutexLocker{
			m: sync.Mutex{},
		},
		status:  map[string]interface{}{},
		updates: make(chan Update),
	}

}

func (g *Gatherer) InsertSynchronously(u Update) {

	g.l.lock()
	{
		g.status[u.Key] = u.Value
	}
	g.l.unlock()

}

func (g *Gatherer) AssembleStatusCopy() map[string]interface{} {

	mapCopy := make(map[string]interface{}, len(g.status))

	g.l.lock()
	{
		for k, v := range g.status {
			mapCopy[k] = v
		}
	}
	g.l.unlock()

	return mapCopy

}

func (g *Gatherer) Listen() {

	g.InsertSynchronously(Update{Key: updateKeyRunnerFinished, Value: false})

	for {
		update := <-g.updates
		if update == quitStatusGathering {
			g.InsertSynchronously(Update{Key: updateKeyRunnerFinished, Value: true})
			close(g.updates)
			return
		} else {
			g.InsertSynchronously(update)
		}

	}

}

func (g *Gatherer) StopListen() {

	g.updates <- quitStatusGathering

}

func (g *Gatherer) ListeningStopped() bool {

	var result bool
	g.l.lock()
	{
		if v, ok := g.status[updateKeyRunnerFinished]; ok {
			result = v.(bool)
		} else {
			result = false
		}
	}
	g.l.unlock()

	return result

}
