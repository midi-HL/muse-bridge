package hatch

import (
	"context"
	"strings"
	"testing"
)

func TestSubscriptionRejectsOversizedCompleteEvent(t *testing.T) {
	for _, suffix := range []string{"", "\n", ` {"event":"next"}`} {
		r := &subscriptionReader{pending: []byte(`{"event":"delta","text":"` + strings.Repeat("x", maxEventLine) + `"}` + suffix), ended: true}
		if event, err := r.next(context.Background()); err == nil || event != nil {
			t.Fatal("oversized complete subscription object was delivered")
		}
	}
}
