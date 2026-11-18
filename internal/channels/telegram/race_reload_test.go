package telegram

import (
	"context"
	"sync"
	"testing"
)

func TestReloadAndUpdateAreSynchronized(t *testing.T) {
	ch, err := New(map[string]any{"telegram.token": "123:secret", "telegram.whitelist": "1"}, Callbacks{OnCommand: func(context.Context, Request, Command) (Response, error) { return Response{}, nil }})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_ = ch.Reload(context.Background(), map[string]any{"telegram.token": "456:secret", "telegram.whitelist": "1,2"})
		}()
		go func() { defer wg.Done(); _ = ch.HandleUpdate(context.Background(), update{}) }()
	}
	wg.Wait()
}
