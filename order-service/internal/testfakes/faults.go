// Package testfakes has in-memory fakes of every port in internal/errand,
// plus FakeCreditService, which plays credit-service's side of the credit
// message contract. The fakes are safe for concurrent use and need no setup
// beyond their constructors.
//
// Each fake can be told to fail through its embedded Faults, and the ones
// without state of their own record their calls through CallLog.
package testfakes

import (
	"slices"
	"sync"
)

// Faults scripts errors per method name (the port method's name, e.g.
// "Apply"). The zero value fails nothing.
type Faults struct {
	mu     sync.Mutex
	next   map[string][]error
	always map[string]error
}

// FailNext makes the next len(errs) calls of method fail, in order.
func (f *Faults) FailNext(method string, errs ...error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.next == nil {
		f.next = map[string][]error{}
	}
	f.next[method] = append(f.next[method], errs...)
}

// FailAlways makes every call of method fail with err until it is called
// again with a nil err. One-shot FailNext errors are used first.
func (f *Faults) FailAlways(method string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.always == nil {
		f.always = map[string]error{}
	}
	if err == nil {
		delete(f.always, method)
		return
	}
	f.always[method] = err
}

// fault returns the error scripted for this call of method, if any.
func (f *Faults) fault(method string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if queue := f.next[method]; len(queue) > 0 {
		f.next[method] = queue[1:]
		return queue[0]
	}
	return f.always[method]
}

// Call is one recorded port call.
type Call struct {
	Method string
	Args   []any
}

// CallLog records port calls.
type CallLog struct {
	mu    sync.Mutex
	calls []Call
}

func (l *CallLog) record(method string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls = append(l.calls, Call{Method: method, Args: args})
}

// Calls returns every recorded call in order.
func (l *CallLog) Calls() []Call {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.calls)
}

// CallsTo returns the recorded calls of one method.
func (l *CallLog) CallsTo(method string) []Call {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []Call
	for _, c := range l.calls {
		if c.Method == method {
			out = append(out, c)
		}
	}
	return out
}
