//go:build windows

package edge

import (
	"os"
	"testing"
)

// TestLiveMemoryConsumers reads the real process table. It is opt-in because
// its result depends on what is running; it only reads.
func TestLiveMemoryConsumers(t *testing.T) {
	if os.Getenv("CIA_EDGE_LIVE_TEST") != "1" {
		t.Skip("set CIA_EDGE_LIVE_TEST=1 to read this machine's process table")
	}
	for _, byCommit := range []bool{false, true} {
		consumers := topMemoryConsumers(refusalConsumerLimit, byCommit)
		if len(consumers) == 0 {
			t.Fatalf("no application holding memory was found (byCommit=%v)", byCommit)
		}
		for index, consumer := range consumers {
			if unclosable[consumer.Name] || consumer.GiB < 0.1 || (index > 0 && consumer.GiB > consumers[index-1].GiB) {
				t.Errorf("unexpected consumer %+v at %d", consumer, index)
			}
		}
		t.Logf("byCommit=%v refusal would name:%s", byCommit, consumerClause(consumers, "usam"))
	}
}
