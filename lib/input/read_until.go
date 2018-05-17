// Copyright (c) 2018 Ashley Jeffs
//
// Permission is hereby granted, free of charge, to any person obtaining a copy
// of this software and associated documentation files (the "Software"), to deal
// in the Software without restriction, including without limitation the rights
// to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
// copies of the Software, and to permit persons to whom the Software is
// furnished to do so, subject to the following conditions:
//
// The above copyright notice and this permission notice shall be included in
// all copies or substantial portions of the Software.
//
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
// FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
// AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
// LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
// OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN
// THE SOFTWARE.

package input

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/Jeffail/benthos/lib/processor/condition"
	"github.com/Jeffail/benthos/lib/types"
	"github.com/Jeffail/benthos/lib/util/service/log"
	"github.com/Jeffail/benthos/lib/util/service/metrics"
)

//------------------------------------------------------------------------------

func init() {
	Constructors["read_until"] = TypeSpec{
		constructor: NewReadUntil,
		description: `
Reads from an input and tests a condition on each message. When the condition
returns true the message is sent out and the input is closed. Use this type to
define inputs where the stream should end once a certain message appears.

Sometimes inputs close themselves. For example, when the ` + "`file`" + ` input
type reaches the end of a file it will shut down. By default this type will also
shut down. If you wish for the input type to be restarted every time it shuts
down until the condition is met then set ` + "`restart_input` to `true`.",
	}
}

//------------------------------------------------------------------------------

// ReadUntilConfig is configuration values for the ReadUntil input type.
type ReadUntilConfig struct {
	Input     *Config          `json:"input" yaml:"input"`
	Restart   bool             `json:"restart_input" yaml:"restart_input"`
	Condition condition.Config `json:"condition" yaml:"condition"`
}

// NewReadUntilConfig creates a new ReadUntilConfig with default values.
func NewReadUntilConfig() ReadUntilConfig {
	return ReadUntilConfig{
		Input:     nil,
		Restart:   false,
		Condition: condition.NewConfig(),
	}
}

//------------------------------------------------------------------------------

type dummyReadUntilConfig struct {
	Input     interface{}      `json:"input" yaml:"input"`
	Condition condition.Config `json:"condition" yaml:"condition"`
}

// MarshalJSON prints an empty object instead of nil.
func (r ReadUntilConfig) MarshalJSON() ([]byte, error) {
	dummy := dummyReadUntilConfig{
		Input:     r.Input,
		Condition: r.Condition,
	}
	if r.Input == nil {
		dummy.Input = struct{}{}
	}
	return json.Marshal(dummy)
}

// MarshalYAML prints an empty object instead of nil.
func (r ReadUntilConfig) MarshalYAML() (interface{}, error) {
	dummy := dummyReadUntilConfig{
		Input:     r.Input,
		Condition: r.Condition,
	}
	if r.Input == nil {
		dummy.Input = struct{}{}
	}
	return dummy, nil
}

//------------------------------------------------------------------------------

// ReadUntil is an input type that reads from a ReadUntil instance.
type ReadUntil struct {
	running int32
	conf    ReadUntilConfig

	wrapped Type
	cond    condition.Type

	wrapperMgr   types.Manager
	wrapperLog   log.Modular
	wrapperStats metrics.Type

	stats metrics.Type
	log   log.Modular

	transactions chan types.Transaction

	closeChan  chan struct{}
	closedChan chan struct{}
}

// NewReadUntil creates a new ReadUntil input type.
func NewReadUntil(
	conf Config,
	mgr types.Manager,
	log log.Modular,
	stats metrics.Type,
) (Type, error) {
	if conf.ReadUntil.Input == nil {
		return nil, errors.New("cannot create read_until input without a child")
	}

	wrapped, err := New(*conf.ReadUntil.Input, mgr, log, stats)
	if err != nil {
		return nil, fmt.Errorf("failed to create input '%v': %v", conf.ReadUntil.Input.Type, err)
	}

	var cond condition.Type
	if cond, err = condition.New(conf.ReadUntil.Condition, mgr, log, stats); err != nil {
		return nil, fmt.Errorf("failed to create condition '%v': %v", conf.ReadUntil.Condition.Type, err)
	}

	rdr := &ReadUntil{
		running: 1,
		conf:    conf.ReadUntil,

		wrapperLog:   log,
		wrapperStats: stats,
		wrapperMgr:   mgr,

		log:          log.NewModule(".input.read_until"),
		stats:        stats,
		wrapped:      wrapped,
		cond:         cond,
		transactions: make(chan types.Transaction),
		closeChan:    make(chan struct{}),
		closedChan:   make(chan struct{}),
	}

	go rdr.loop()
	return rdr, nil
}

//------------------------------------------------------------------------------

func (r *ReadUntil) loop() {
	defer func() {
		if r.wrapped != nil {
			r.wrapped.CloseAsync()
			err := r.wrapped.WaitForClose(time.Second)
			for ; err != nil; err = r.wrapped.WaitForClose(time.Second) {
			}
		}
		r.stats.Decr("input.read_until.running", 1)

		close(r.transactions)
		close(r.closedChan)
	}()
	r.stats.Incr("input.read_until.running", 1)

	var open bool

runLoop:
	for atomic.LoadInt32(&r.running) == 1 {
		if r.wrapped == nil {
			if r.conf.Restart {
				var err error
				if r.wrapped, err = New(
					*r.conf.Input, r.wrapperMgr, r.wrapperLog, r.wrapperStats,
				); err != nil {
					r.stats.Incr("input.read_until.input.restart.error", 1)
					r.log.Errorf("Failed to create input '%v': %v\n", r.conf.Input.Type, err)
					return
				}
				r.stats.Incr("input.read_until.input.restart.success", 1)
			} else {
				return
			}
		}

		var tran types.Transaction
		select {
		case tran, open = <-r.wrapped.TransactionChan():
			if !open {
				r.stats.Incr("input.read_until.input.closed", 1)
				r.wrapped = nil
				continue runLoop
			}
		case <-r.closeChan:
			return
		}
		r.stats.Incr("input.read_until.count", 1)

		if !r.cond.Check(tran.Payload) {
			select {
			case r.transactions <- tran:
				r.stats.Incr("input.read_until.propagated", 1)
			case <-r.closeChan:
				return
			}
			continue
		}

		// If this transaction succeeds we shut down.
		tmpRes := make(chan types.Response)
		select {
		case r.transactions <- types.NewTransaction(tran.Payload, tmpRes):
			r.stats.Incr("input.read_until.final.propagated", 1)
		case <-r.closeChan:
			return
		}

		var res types.Response
		select {
		case res, open = <-tmpRes:
			streamEnds := res.Error() == nil
			select {
			case tran.ResponseChan <- res:
				r.stats.Incr("input.read_until.final.response.sent", 1)
			case <-r.closeChan:
				return
			}
			if streamEnds {
				r.stats.Incr("input.read_until.final.response.sent", 1)
				return
			}
			r.stats.Incr("input.read_until.final.response.error", 1)
		case <-r.closeChan:
			return
		}
	}
}

// TransactionChan returns the transactions channel.
func (r *ReadUntil) TransactionChan() <-chan types.Transaction {
	return r.transactions
}

// CloseAsync shuts down the ReadUntil input and stops processing requests.
func (r *ReadUntil) CloseAsync() {
	if atomic.CompareAndSwapInt32(&r.running, 1, 0) {
		close(r.closeChan)
	}
}

// WaitForClose blocks until the ReadUntil input has closed down.
func (r *ReadUntil) WaitForClose(timeout time.Duration) error {
	select {
	case <-r.closedChan:
	case <-time.After(timeout):
		return types.ErrTimeout
	}
	return nil
}

//------------------------------------------------------------------------------
