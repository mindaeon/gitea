// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package session

import (
	"context"
	"strings"
	"testing"

	"gitea.dev/models/unittest"
	"gitea.dev/modules/json"
	"gitea.dev/modules/nosql"
	"gitea.dev/modules/test"
	"gitea.dev/modules/util"

	"gitea.com/go-chi/session"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
)

func TestDestroyUserSessions(t *testing.T) {
	t.Run("db", func(t *testing.T) {
		require.NoError(t, unittest.PrepareTestDatabase())
		testDestroyUserSessions(t, "db", "")
	})
	t.Run("file", func(t *testing.T) {
		testDestroyUserSessions(t, "file", t.TempDir())
	})
	t.Run("redis", func(t *testing.T) {
		// a prefix of its own: the redis server may be shared and keep what earlier runs left
		testDestroyUserSessions(t, "redis", test.PrepareTestRedis(t)+"?prefix=sessions-"+util.CryptoRandomString(8)+":")
	})
	t.Run("redis, empty prefix beside other keys", func(t *testing.T) {
		conn := redisDatabaseOfItsOwn(t)
		c := nosql.GetManager().GetRedisClient(conn)
		// what else a database without a prefix may hold: a queue's list, a cache's hash, a non-session string
		require.NoError(t, c.RPush(t.Context(), "queue", "item").Err())
		require.NoError(t, c.HSet(t.Context(), "cache", "field", "value").Err())
		require.NoError(t, c.Set(t.Context(), "plain", "not a session", 0).Err())
		testDestroyUserSessions(t, "redis", conn)
		assert.EqualValues(t, 3, c.Exists(t.Context(), "queue", "cache", "plain").Val())
	})
	t.Run("redis cluster masters, visited concurrently", func(t *testing.T) {
		// two masters that see the same keys, scanned at the same time, as ForEachMaster does:
		// run with -race to check that the visits are synchronized; every session is counted once
		defer test.MockVariableValue(&forEachRedisMaster, func(ctx context.Context, client redis.UniversalClient, fn func(context.Context, redis.Cmdable) error) error {
			var g errgroup.Group
			for range 2 {
				g.Go(func() error { return fn(ctx, client) })
			}
			return g.Wait()
		})()
		testDestroyUserSessions(t, "redis", test.PrepareTestRedis(t)+"?prefix=sessions-"+util.CryptoRandomString(8)+":")
	})
	t.Run("memory", func(t *testing.T) {
		p := initTestProvider(t, "memory", "")
		sid := storeTestSession(t, p, map[any]any{KeyUID: int64(2)})

		assert.False(t, CanDestroyUserSessions())
		_, err := DestroyUserSessions(t.Context(), 2)
		assert.ErrorIs(t, err, ErrUserSessionsUnsupported)
		assert.True(t, sessionExists(t, p, sid))
	})
}

func initTestProvider(t *testing.T, provider, config string) *VirtualSessionProvider {
	previous := activeProvider.Load()
	t.Cleanup(func() { activeProvider.Store(previous) })
	p := &VirtualSessionProvider{}
	opts, err := json.Marshal(session.Options{Provider: provider, ProviderConfig: config})
	require.NoError(t, err)
	require.NoError(t, p.Init(3600, string(opts)))
	return p
}

func storeTestSession(t *testing.T, p *VirtualSessionProvider, data map[any]any) string {
	sid := util.CryptoRandomString(16)
	store, err := p.Read(sid)
	require.NoError(t, err)
	for k, v := range data {
		require.NoError(t, store.Set(k, v))
	}
	require.NoError(t, store.Release())
	require.True(t, sessionExists(t, p, sid))
	return sid
}

func sessionExists(t *testing.T, p *VirtualSessionProvider, sid string) bool {
	exist, err := p.provider.Exist(sid)
	require.NoError(t, err)
	return exist
}

func testDestroyUserSessions(t *testing.T, provider, config string) {
	p := initTestProvider(t, provider, config)
	require.True(t, CanDestroyUserSessions())

	signedIn := storeTestSession(t, p, map[any]any{KeyUID: int64(2), "_csrf": "token"})
	signedInElsewhere := storeTestSession(t, p, map[any]any{KeyUID: int64(2)})
	impersonated := storeTestSession(t, p, map[any]any{KeyUID: int64(2), KeyImpersonatorData: map[string]any{KeyUID: int64(1)}})
	impersonating := storeTestSession(t, p, map[any]any{KeyUID: int64(3), KeyImpersonatorData: map[string]any{KeyUID: int64(2)}})
	otherUser := storeTestSession(t, p, map[any]any{KeyUID: int64(3)})
	anonymous := storeTestSession(t, p, map[any]any{"_csrf": "token"})

	// a request using the session while it is destroyed must not save it back afterwards
	inFlight, err := p.Read(signedIn)
	require.NoError(t, err)
	assert.EqualValues(t, 2, inFlight.Get(KeyUID))

	destroyed, err := DestroyUserSessions(t.Context(), 2)
	require.NoError(t, err)
	assert.Equal(t, 4, destroyed)

	require.NoError(t, inFlight.Set("visited", true))
	require.NoError(t, inFlight.Release())

	for _, sid := range []string{signedIn, signedInElsewhere, impersonated, impersonating} {
		assert.False(t, sessionExists(t, p, sid), "session %s of the user is destroyed", sid)
	}
	for _, sid := range []string{otherUser, anonymous} {
		assert.True(t, sessionExists(t, p, sid), "session %s of someone else is kept", sid)
	}

	// a destroyed session reads as a new, signed-out one
	store, err := p.Read(signedIn)
	require.NoError(t, err)
	assert.Nil(t, store.Get(KeyUID))

	destroyed, err = DestroyUserSessions(t.Context(), 2)
	require.NoError(t, err)
	assert.Zero(t, destroyed)

	// the administrator who impersonated the user is signed out as well
	destroyed, err = DestroyUserSessions(t.Context(), 1)
	require.NoError(t, err)
	assert.Zero(t, destroyed, "the impersonation was ended with the user's sessions")
	assert.True(t, sessionExists(t, p, otherUser))
}

// redisDatabaseOfItsOwn returns a connection to an emptied database no other test uses
func redisDatabaseOfItsOwn(t *testing.T) string {
	conn := test.PrepareTestRedis(t)
	if server, ok := strings.CutSuffix(conn, "/0"); ok {
		conn = server + "/7" // a shared server: not the database the other packages use
	}
	require.NoError(t, nosql.GetManager().GetRedisClient(conn).FlushDB(t.Context()).Err())
	return conn
}
