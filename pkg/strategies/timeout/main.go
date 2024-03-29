package timeout

import (
	"sync"
	"time"

	"github.com/ds-test-framework/scheduler/pkg/types"
	"github.com/ds-test-framework/scheduler/pkg/util"
	"github.com/spf13/viper"
)

type pendingReceives struct {
	pendingReceives map[types.ReplicaID]map[uint]*types.Event
	lock            *sync.Mutex
}

func newPendingReceives() *pendingReceives {
	return &pendingReceives{
		pendingReceives: make(map[types.ReplicaID]map[uint]*types.Event),
		lock:            new(sync.Mutex),
	}
}

func (p *pendingReceives) Update(e *types.Event) {
	p.lock.Lock()
	defer p.lock.Unlock()

	_, ok := p.pendingReceives[e.Replica]
	if !ok {
		p.pendingReceives[e.Replica] = make(map[uint]*types.Event)
	}

	p.pendingReceives[e.Replica][e.ID] = e
}

func (p *pendingReceives) Delete(e *types.Event) {
	p.lock.Lock()
	defer p.lock.Unlock()

	m, ok := p.pendingReceives[e.Replica]
	if ok {
		_, exists := m[e.ID]
		if exists {
			delete(m, e.ID)
			p.pendingReceives[e.Replica] = m
		}
	}
}

func (p *pendingReceives) Get(replica types.ReplicaID) []*types.Event {
	result := make([]*types.Event, 0)
	p.lock.Lock()
	defer p.lock.Unlock()
	l, ok := p.pendingReceives[replica]
	if !ok {
		return result
	}
	for _, v := range l {
		result = append(result, v)
	}
	return result
}

func (p *pendingReceives) Reset() {
	p.lock.Lock()
	defer p.lock.Unlock()

	p.pendingReceives = make(map[types.ReplicaID]map[uint]*types.Event)
}

// TimeoutEngine randomly schedules timeouts such that spuriousness is not violated
// For every incoming message two events corresponding to the send/receive are created. The send is immediately added to the causal order maintained as a DAG.
// Receive event is added only if there are no conflicts. Otherwise they are delayed by a randomly chosen time period
type TimeoutEngine struct {
	inChan       chan *types.MessageWrapper
	outChan      chan *types.MessageWrapper
	stopChan     chan bool
	eventStore   *eventStore
	messageStore *messageStore
	counterLock  *sync.Mutex
	eventChan    chan *types.Event
	eventCounter uint
	paused       bool
	pausedLock   *sync.Mutex
	graphManager *graphManager
	msgMap       map[string]*types.MessageWrapper
	msgMapLock   *sync.Mutex
	check        bool

	pendingReceives *pendingReceives
	scheduleManager *scheduleManager
}

// NewTimeoutEngine returns a TimeoutEngine
func NewTimeoutEngine(o *viper.Viper) *TimeoutEngine {
	o.SetDefault("check_spuriousness", true)

	e := &TimeoutEngine{
		stopChan:     make(chan bool, 3),
		eventStore:   newEventStore(),
		messageStore: newMessageStore(),
		counterLock:  new(sync.Mutex),
		eventChan:    make(chan *types.Event, 10),
		eventCounter: 0,
		paused:       false,
		pausedLock:   new(sync.Mutex),
		graphManager: newGraphManager(),
		msgMap:       make(map[string]*types.MessageWrapper),
		msgMapLock:   new(sync.Mutex),
		check:        o.GetBool("check_spuriousness"),

		pendingReceives: newPendingReceives(),
	}
	e.scheduleManager = newScheduleManager(e.eventChan)
	return e
}

// Reset implements StrategyEngine
func (e *TimeoutEngine) Reset() {
	e.pausedLock.Lock()
	e.paused = true
	e.pausedLock.Unlock()

	e.eventStore.Reset()
	e.messageStore.Reset()
	e.pendingReceives.Reset()
	e.graphManager.Reset()
	e.scheduleManager.Reset()

	e.counterLock.Lock()
	e.eventCounter = 0
	e.counterLock.Unlock()

	e.msgMapLock.Lock()
	e.msgMap = make(map[string]*types.MessageWrapper)
	e.msgMapLock.Unlock()

	e.flushChannels()
	e.pausedLock.Lock()
	e.paused = false
	e.pausedLock.Unlock()
}

func (e *TimeoutEngine) flushChannels() {
	for {
		l := len(e.eventChan)
		if l == 0 {
			break
		}
		<-e.eventChan
	}

	for {
		l := len(e.inChan)
		if l == 0 {
			break
		}
		<-e.inChan
	}
}

func (e *TimeoutEngine) isPaused() bool {
	e.pausedLock.Lock()
	defer e.pausedLock.Unlock()

	return e.paused
}

func (e *TimeoutEngine) createEvents(msg *types.Message) (*types.Event, *types.Event) {
	e.counterLock.Lock()
	defer e.counterLock.Unlock()

	sendEvent := types.NewEvent(e.eventCounter, msg.From, types.Send, 0, msg.ID)
	e.eventCounter = e.eventCounter + 1
	receiveEvent := types.NewEvent(e.eventCounter, msg.To, types.Receive, 0, msg.ID)
	e.eventCounter = e.eventCounter + 1

	msg.UpdateReceiveEvent(receiveEvent.ID)
	msg.UpdateSendEvent(sendEvent.ID)
	e.eventStore.Set(sendEvent)
	e.eventStore.Set(receiveEvent)

	return sendEvent, receiveEvent
}

// TODO: Can leak from previous run. Need to be able to cancel the scheduled receives
func (e *TimeoutEngine) scheduleReceive(event *types.Event) {
	if event.Type != types.Receive {
		e.eventChan <- event
	}
	msg, ok := e.messageStore.Get(event.MsgID)
	var d int
	if ok && msg.Timeout {
		d = util.RandIntn(msg.Weight)
	} else {
		d = util.RandIntn(1000)
	}
	// logger.Debug(
	// 	fmt.Sprintf("Engine: Scheduling receive event: %#v for %d ms", event, d),
	// )
	e.scheduleManager.Schedule(event, time.Duration(d)*time.Millisecond)
}

func (e *TimeoutEngine) handleReceiveEvent(event *types.Event) {
	if event.Type != types.Receive {
		return
	}
	_, ok := e.eventStore.Get(event.ID)
	if !ok {
		return
	}
	// logger.Debug(fmt.Sprintf("Engine: Handling receive: %#v", event))
	msg, ok := e.messageStore.Get(event.MsgID)
	if !ok {
		return
	}
	if msg != nil && msg.Timeout {
		// logger.Debug(fmt.Sprintf("Engine: Timeout event: %#v", event))
		err := e.graphManager.AddEvent(event, e.messageStore, e.eventStore)
		e.pendingReceives.Delete(event)
		if err != nil {
			// logger.Debug(fmt.Sprintf("Engine: Error adding event to grpah: %s", err.Error()))
		}
		go e.dispatch(event.MsgID)
		return
	}

	allok := true

	if e.check {
		// logger.Debug(fmt.Sprintf("Engine: Non timeout event: %#v", event))
		for _, pReceive := range e.pendingReceives.Get(event.Replica) {

			// logger.Debug(fmt.Sprintf("Engine: For pending receive: %#v", pReceive))

			mPseudo := e.messageStore.Pseudo()
			ePseudo := e.eventStore.Pseudo()

			mPseudo.MarkDirty(pReceive.MsgID)
			mPseudo.MarkDirty(event.MsgID)

			ePseudo.MarkDirty(event.ID)
			ePseudo.MarkDirty(pReceive.ID)

			gPseudo := e.graphManager.GetPseudo()

			// logger.Debug("Engine: Created pseudo elements")
			pseudoE, ok := ePseudo.Get(event.ID)
			if !ok {
				return
			}
			_, err := gPseudo.AddEvent(pseudoE, mPseudo, ePseudo)
			if err != nil {
				// logger.Debug(fmt.Sprintf("Engine: Error adding receive event: %s", err.Error()))
				allok = false
				return
			}
			// logger.Debug("Engine: Added pending receive event to the graph")

			pseudoE, ok = ePseudo.Get(pReceive.ID)
			if !ok {
				return
			}
			ok, err = gPseudo.AddEvent(pseudoE, mPseudo, ePseudo)
			if err != nil {
				// logger.Debug(fmt.Sprintf("Engine: Error finding conflict: %s", err.Error()))
				allok = false
				return
			}
			if !ok {
				allok = false
			}
		}
	}

	if allok {
		// logger.Debug(fmt.Sprintf("Engine: All ok, adding receive event: %#v", event))
		err := e.graphManager.AddEvent(event, e.messageStore, e.eventStore)
		// e.pendingReceives.Delete(event)
		if err != nil {
			// logger.Debug(fmt.Sprintf("Engine: Error adding event to graph: %s", err.Error()))
		}
		go e.dispatch(event.MsgID)
	} else {
		// logger.Debug(fmt.Sprintf("Engine: Not all ok, rescheduling: %#v", event))
		go e.scheduleReceive(event)
	}
}

func (e *TimeoutEngine) dispatch(msgID string) {
	// logger.Debug(fmt.Sprintf("Engine: Called dispatch on: %s", msgID))
	e.msgMapLock.Lock()
	msg, ok := e.msgMap[msgID]
	if ok {
		delete(e.msgMap, msgID)
	}
	e.msgMapLock.Unlock()
	if !ok {
		return
	}
	// logger.Debug(fmt.Sprintf("Engine: Dispatching message: %#v", msg.Msg))
	e.outChan <- msg
}

func (e *TimeoutEngine) handleSendEvent(event *types.Event) {
	if event.Type != types.Send {
		return
	}
	_, ok := e.eventStore.Get(event.ID)
	if !ok {
		return
	}
	msg, ok := e.messageStore.Get(event.MsgID)
	if !ok {
		return
	}
	// logger.Debug(fmt.Sprintf("Engine: Send event: %#v", event))
	err := e.graphManager.AddEvent(
		event,
		e.messageStore,
		e.eventStore,
	)
	if err != nil {
		// logger.Debug(fmt.Sprintf("Engine: Error adding send event: %s", err.Error()))
		return
	}
	if msg.Timeout {
		if receive, ok := e.eventStore.Get(msg.GetReceiveEvent()); ok {
			e.pendingReceives.Update(receive)
		}
	}
}

func (e *TimeoutEngine) pollEventChan() {
	for {
		select {
		case event := <-e.eventChan:
			// logger.Debug(fmt.Sprintf("Engine: Handling event: %#v", event))
			if !e.isPaused() {
				if event.Type == types.Send {
					e.handleSendEvent(event)
				} else {
					e.handleReceiveEvent(event)
				}
			}
			// logger.Debug(fmt.Sprintf("Engine: Completed Handling event: %#v", event))
			break
		case <-e.stopChan:
			return
		}
	}
}

func (e *TimeoutEngine) pollInChan() {
	for {
		select {
		case msgW := <-e.inChan:
			e.msgMapLock.Lock()
			e.msgMap[msgW.Msg.ID] = msgW
			e.msgMapLock.Unlock()

			send, receive := e.createEvents(msgW.Msg)
			e.messageStore.Set(msgW.Msg)
			// logger.Debug(fmt.Sprintf("Engine: Received message: %#v", msgW.Msg))
			e.eventChan <- send
			// logger.Debug(fmt.Sprintf("Engine: Added send event to channel: %#v", send))
			if msgW.Msg.Timeout {
				go e.scheduleReceive(receive)
			} else {
				e.eventChan <- receive
				// logger.Debug(fmt.Sprintf("Engine: Added receive event to channel: %#v", receive))
			}
			break
		case <-e.stopChan:
			return
		}
	}
}

// Run implements StrategyEngine
func (e *TimeoutEngine) Run() *types.Error {
	go e.pollEventChan()
	go e.pollInChan()
	return nil
}

// Stop implements StrategyEngine
func (e *TimeoutEngine) Stop() {
	e.stopChan <- true
	e.stopChan <- true
}

// SetChannels implements StrategyEngine
func (e *TimeoutEngine) SetChannels(
	inChan chan *types.MessageWrapper,
	outChan chan *types.MessageWrapper,
) {
	e.inChan = inChan
	e.outChan = outChan
}
