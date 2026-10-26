package relayclient

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/hyperkubeorg/openarcanum/internal/wire"
)

// A client with nothing but a Hello: deliver runs on the read loop alone.
func newPartialClient(maxMessage uint64) *Client {
	return &Client{
		hello:        &wire.Hello{MaxChunk: 64, MaxMessage: maxMessage},
		handlers:     make(map[string]Handler),
		partial:      make(map[partialKey]*partialMsg),
		partialBy:    make(map[uint64]int),
		PartialBytes: 64 << 20,
	}
}

func deliverFrame(topic string, sender, id uint64, seq uint32, final bool, data string) *wire.Deliver {
	return &wire.Deliver{Topic: []byte(topic), Sender: sender, MessageId: id, Seq: seq, Final: final, Data: []byte(data)}
}

// What partly received messages hold is capped: in all (PartialBytes, the
// oldest dropped first), in number, and per sender. A relay can start as
// many messages as it likes and finish none of them.
func TestPartialsBounded(t *testing.T) {
	c := newPartialClient(1 << 20)
	c.PartialBytes = 4096
	var got int
	c.handlers["t"] = func(_, _ []byte) { got++ }
	check := func(what string) {
		t.Helper()
		if c.partialBytes > c.PartialBytes || len(c.partial) > maxPartials {
			t.Fatalf("%s: %d bytes in %d partial messages", what, c.partialBytes, len(c.partial))
		}
		for sender, n := range c.partialBy {
			if n > maxPartialsBySender {
				t.Fatalf("%s: sender %d has %d messages in flight", what, sender, n)
			}
		}
	}
	// One sender, many messages of 64 bytes, never finished.
	for id := uint64(1); id <= 1000; id++ {
		c.deliver(deliverFrame("t", 1, id, 0, false, strings.Repeat("x", 64)))
		check("one sender")
	}
	if n := c.partialBy[1]; n != maxPartialsBySender {
		t.Fatalf("sender kept %d messages in flight", n)
	}
	// Many senders: the count and the bytes stay within bounds, the
	// oldest messages going first.
	for sender := uint64(2); sender <= 3000; sender++ {
		c.deliver(deliverFrame("t", sender, 1, 0, false, strings.Repeat("y", 64)))
		check("many senders")
	}
	if _, ok := c.partial[partialKey{1, 1}]; ok {
		t.Fatal("the oldest message outlived thousands of newer ones")
	}
	// The newest still completes.
	c.deliver(deliverFrame("t", 3000, 1, 1, true, "!"))
	if got != 1 {
		t.Fatalf("%d messages delivered, want the newest", got)
	}
	if c.partialBytes < 0 || c.partialBy[3000] != 0 {
		t.Fatalf("accounting off: %d bytes, sender 3000 has %d in flight", c.partialBytes, c.partialBy[3000])
	}
	// Everything dropped once stale.
	for _, p := range c.partial {
		p.at = time.Now().Add(-3 * time.Minute)
	}
	c.gcPartial()
	if len(c.partial) != 0 || c.partialBytes != 0 || len(c.partialBy) != 0 {
		t.Fatalf("after gc: %d messages, %d bytes, %d senders", len(c.partial), c.partialBytes, len(c.partialBy))
	}
}

// A message's chunks all belong to the topic its first chunk named: one
// that switches topics is dropped, and a message is handed to the
// handler of the first chunk's topic.
func TestChunksStayOnTheirTopic(t *testing.T) {
	c := newPartialClient(1 << 20)
	var got []string
	for _, topic := range []string{"a", "b"} {
		c.handlers[topic] = func(topic, data []byte) { got = append(got, string(topic)+":"+string(data)) }
	}
	c.deliver(deliverFrame("a", 1, 1, 0, false, "a-"))
	c.deliver(deliverFrame("b", 1, 1, 1, true, "then b"))
	c.deliver(deliverFrame("a", 1, 2, 0, false, "whole "))
	c.deliver(deliverFrame("a", 1, 2, 1, true, "message"))
	if strings.Join(got, ",") != "a:whole message" {
		t.Fatalf("delivered %q", got)
	}
	if len(c.partial) != 0 || c.partialBytes != 0 {
		t.Fatalf("%d messages (%d bytes) left in flight", len(c.partial), c.partialBytes)
	}
}

// A relay's Hello can't raise max_message above the protocol's ceiling.
func TestHelloMaxMessageClamped(t *testing.T) {
	relay := startFakeRelay(t)
	relay.maxMessage = 1 << 40
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := Dial(ctx, relay.url)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if c.Hello().MaxMessage != maxMessage {
		t.Fatalf("max_message %d, want %d", c.Hello().MaxMessage, maxMessage)
	}
	if err := c.Publish([]byte("t"), make([]byte, maxMessage+1)); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("message over the ceiling: %v", err)
	}
	select {
	case relay.frames <- nil:
	case <-ctx.Done():
		t.Fatal("fake relay never took the frames")
	}
}
