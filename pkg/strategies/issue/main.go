package nop

import (
	"github.com/ds-test-framework/scheduler/pkg/types"
)

// NopScheduler does nothing. Just returns the incoming message in the outgoing channel
type IssueScheduler struct {
	inChan  chan *types.MessageWrapper
	outChan chan *types.MessageWrapper
	stopCh  chan bool
}

// NewNopScheduler returns a new NopScheduler
func NewIssueScheduler() *IssueScheduler {
	return &IssueScheduler{
		stopCh: make(chan bool, 1),
	}
}

// Reset implements StrategyEngine
func (n *IssueScheduler) Reset() {
}

// Run implements StrategyEngine
func (n *IssueScheduler) Run() *types.Error {
	go n.poll()
	return nil
}

// Stop implements StrategyEngine
func (n *IssueScheduler) Stop() {
	close(n.stopCh)
}

// SetChannels implements StrategyEngine
func (n *IssueScheduler) SetChannels(inChan chan *types.MessageWrapper, outChan chan *types.MessageWrapper) {
	n.inChan = inChan
	n.outChan = outChan
}

func (n *IssueScheduler) poll() {
	for {
		select {
		case m := <-n.inChan:
			go func(m *types.MessageWrapper) {
				n.outChan <- m
			}(m)
		case <-n.stopCh:
			return
		}
	}
}
