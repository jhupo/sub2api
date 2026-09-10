package repository

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestChannelCachePubSub(t *testing.T) {
	redisServer := miniredis.RunT(t)
	publisherRedis := redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
	subscriberRedis := redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
	t.Cleanup(func() {
		_ = publisherRedis.Close()
		_ = subscriberRedis.Close()
	})

	publisher := NewChannelCache(publisherRedis)
	subscriber := NewChannelCache(subscriberRedis)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	received := make(chan struct{}, 1)
	stop := subscriber.SubscribeUpdates(ctx, func() {
		select {
		case received <- struct{}{}:
		default:
		}
	})
	t.Cleanup(stop)

	require.Eventually(t, func() bool {
		return redisServer.PubSubNumSub(channelCachePubSubKey)[channelCachePubSubKey] == 1
	}, time.Second, 10*time.Millisecond)
	select {
	case <-received:
	case <-time.After(time.Second):
		t.Fatal("initial subscription must invalidate a possibly stale snapshot")
	}
	require.NoError(t, publisher.NotifyUpdate(context.Background()))
	select {
	case <-received:
	case <-time.After(time.Second):
		t.Fatal("channel cache invalidation notification was not received")
	}
	stop()
	stop()
	require.Eventually(t, func() bool {
		return redisServer.PubSubNumSub(channelCachePubSubKey)[channelCachePubSubKey] == 0
	}, time.Second, 10*time.Millisecond)
}

func TestChannelCachePubSubReconnectInvalidatesSnapshot(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	events := make(chan struct{}, 8)
	stop := NewChannelCache(client).SubscribeUpdates(context.Background(), func() { events <- struct{}{} })
	t.Cleanup(stop)
	select {
	case <-events:
	case <-time.After(3 * time.Second):
		t.Fatal("initial subscription not ready")
	}
	server.Close()
	require.NoError(t, server.Restart())
	select {
	case <-events:
	case <-time.After(5 * time.Second):
		t.Fatal("reconnected subscription did not invalidate cache")
	}
}
