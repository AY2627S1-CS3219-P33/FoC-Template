// AI Assistance Disclosure:
// Tool: Claude Code (model: Claude Opus 5.5), date: 2026-11-10
// Scope: Implementation of test code (unit and integration tests).
// Author review: Reviewed, validated, and tested by the authors.

package hub

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func newClient(userID string, buffer int) (*Client, *atomic.Int32) {
	var dropped atomic.Int32
	return NewClient(userID, buffer, func() { dropped.Add(1) }), &dropped
}

func received(c *Client) [][]byte {
	var out [][]byte
	for {
		select {
		case p := <-c.Outbox():
			out = append(out, p)
		default:
			return out
		}
	}
}

func TestDeliverReachesExactlyTheListedUsers(t *testing.T) {
	h := New(5)
	aliceTab1, _ := newClient("alice", 4)
	aliceTab2, _ := newClient("alice", 4)
	bob, _ := newClient("bob", 4)
	carol, _ := newClient("carol", 4)
	for _, c := range []*Client{aliceTab1, aliceTab2, bob, carol} {
		require.True(t, h.Register(c))
	}

	h.Deliver([]string{"alice", "bob", "alice", "nobody-online"}, []byte("hi"))

	require.Equal(t, [][]byte{[]byte("hi")}, received(aliceTab1), "every tab of a user, once each")
	require.Equal(t, [][]byte{[]byte("hi")}, received(aliceTab2))
	require.Equal(t, [][]byte{[]byte("hi")}, received(bob))
	require.Empty(t, received(carol), "not listed, so nothing")
}

func TestRegisterEnforcesTheLimitPerUser(t *testing.T) {
	h := New(2)
	first, _ := newClient("alice", 1)
	second, _ := newClient("alice", 1)
	third, _ := newClient("alice", 1)
	require.True(t, h.Register(first))
	require.True(t, h.Register(second))
	require.False(t, h.Register(third), "over the limit")
	other, _ := newClient("bob", 1)
	require.True(t, h.Register(other), "the limit is per user")
	require.Equal(t, 2, h.Connections("alice"))

	h.Unregister(first)
	h.Unregister(first) // harmless
	require.Equal(t, 1, h.Connections("alice"))
	require.True(t, h.Register(third), "room again after a tab closed")

	h.Unregister(second)
	h.Unregister(third)
	require.Equal(t, 0, h.Connections("alice"))
	h.Deliver([]string{"alice"}, []byte("x")) // no connections: nothing happens
}

func TestASlowClientIsDroppedWithoutBlockingOthers(t *testing.T) {
	h := New(5)
	slow, dropped := newClient("slow", 2)
	fast, _ := newClient("fast", 100)
	require.True(t, h.Register(slow))
	require.True(t, h.Register(fast))

	done := make(chan struct{})
	go func() {
		for i := 0; i < 10; i++ {
			h.Deliver([]string{"slow", "fast"}, []byte{byte(i)})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Deliver blocked on a slow client")
	}

	require.Len(t, received(fast), 10, "the fast client got everything")
	require.Len(t, received(slow), 2, "the slow one got what fitted")
	require.Eventually(t, func() bool { return dropped.Load() == 1 }, time.Second, 5*time.Millisecond,
		"tooSlow is called exactly once")
	time.Sleep(20 * time.Millisecond)
	require.EqualValues(t, 1, dropped.Load())
}

func TestConcurrentUse(t *testing.T) {
	h := New(1000)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, _ := newClient("alice", 1)
			require.True(t, h.Register(c))
			h.Deliver([]string{"alice"}, []byte("x"))
			h.Unregister(c)
		}()
	}
	wg.Wait()
	require.Equal(t, 0, h.Connections("alice"))
}

func TestNewClientRejectsBadArguments(t *testing.T) {
	require.Panics(t, func() { NewClient("", 1, func() {}) })
	require.Panics(t, func() { NewClient("a", 0, func() {}) })
	require.Panics(t, func() { NewClient("a", 1, nil) })
	require.Panics(t, func() { New(0) })
}
