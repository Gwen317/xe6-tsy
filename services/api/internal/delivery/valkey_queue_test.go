package delivery

import (
	"testing"

	"github.com/redis/go-redis/v9"
)

func TestValkeyQueuePendingBufferPreservesClaimedOrder(t *testing.T) {
	queue := &ValkeyQueue{}
	queue.appendPending(nil, []redis.XMessage{
		{ID: "1-0", Values: map[string]interface{}{"attempt_id": "attempt-1"}},
		{ID: "2-0", Values: map[string]interface{}{"attempt_id": []byte("attempt-2")}},
	})

	first, ok := queue.takePending()
	if !ok || first != (QueueMessage{AttemptID: "attempt-1", Receipt: "1-0"}) {
		t.Fatalf("first pending = %#v, ok=%v", first, ok)
	}
	second, ok := queue.takePending()
	if !ok || second != (QueueMessage{AttemptID: "attempt-2", Receipt: "2-0"}) {
		t.Fatalf("second pending = %#v, ok=%v", second, ok)
	}
	if _, ok := queue.takePending(); ok {
		t.Fatal("pending buffer still contains an entry")
	}
}

func TestStringValueAcceptsRedisRepresentations(t *testing.T) {
	tests := []struct {
		name  string
		value interface{}
		want  string
		ok    bool
	}{
		{name: "string", value: "attempt-1", want: "attempt-1", ok: true},
		{name: "bytes", value: []byte("attempt-2"), want: "attempt-2", ok: true},
		{name: "missing", value: nil, ok: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, ok := stringValue(test.value)
			if got != test.want || ok != test.ok {
				t.Fatalf("stringValue(%#v) = %q, %v; want %q, %v", test.value, got, ok, test.want, test.ok)
			}
		})
	}
}

func TestPendingReadDoesNotBlockWhenThereIsNoPendingEntry(t *testing.T) {
	args := pendingReadArgs(&ValkeyQueue{stream: "stream", group: "group", consumer: "consumer"})
	if args.Block >= 0 {
		t.Fatalf("pending read block = %s, want negative no-block sentinel", args.Block)
	}
	if len(args.Streams) != 2 || args.Streams[1] != "0" {
		t.Fatalf("pending read streams = %#v, want stream and 0 cursor", args.Streams)
	}
}
