//go:build unit

package service

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestChannelCacheRejectsInvalidatedBuild(t *testing.T) {
	for _, oldError := range []bool{false, true} {
		t.Run(map[bool]string{false: "old_snapshot", true: "old_error"}[oldError], func(t *testing.T) {
			started, release := make(chan struct{}), make(chan struct{})
			var calls atomic.Int32
			repo := &mockChannelRepository{listAllFn: func(context.Context) ([]Channel, error) {
				if calls.Add(1) == 1 {
					close(started)
					<-release
					if oldError {
						return nil, errors.New("old failure")
					}
					return []Channel{{ID: 1, Name: "old"}}, nil
				}
				return []Channel{{ID: 1, Name: "new"}}, nil
			}}
			svc := NewChannelService(repo, nil, nil, nil, nil)
			result := make(chan *channelCache, 1)
			go func() { cache, _ := svc.loadCache(context.Background()); result <- cache }()
			<-started
			svc.clearCache()
			newCache, err := svc.loadCache(context.Background())
			close(release)
			require.NoError(t, err)
			require.Equal(t, "new", newCache.byID[1].Name)
			select {
			case cache := <-result:
				require.Same(t, newCache, cache)
			case <-time.After(time.Second):
				t.Fatal("invalidated reader did not join the new snapshot")
			}
			require.Same(t, newCache, svc.cache.Load())
		})
	}
}
