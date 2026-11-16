// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package session

import (
	"context"
	"encoding/gob"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"gitea.dev/models/auth"
	"gitea.dev/modules/log"
	"gitea.dev/modules/util"

	"gitea.com/go-chi/session"
	"github.com/redis/go-redis/v9"
)

// ErrUserSessionsUnsupported is returned when the configured session provider can't find the sessions of a user
var ErrUserSessionsUnsupported = util.ErrorWrap(util.ErrUnprocessableContent, "the session provider can't end the sessions of one user, only the db, file and redis providers can")

func init() {
	// the impersonator's data is stored as a map[string]any: register it now, not on the first Set,
	// so that sessions stored before a restart can be decoded (see CHI-SESSION-GOB-REGISTER)
	gob.Register(map[string]any{})
}

// sessionLister is implemented for the session providers whose sessions can all be gone through
type sessionLister interface {
	// listSessions calls visit with the id and the data of every stored session
	listSessions(ctx context.Context, visit func(sid string, data map[any]any)) error
}

// CanDestroyUserSessions reports whether the configured session provider can end the sessions of one user
func CanDestroyUserSessions() bool {
	return activeProvider.Load().lister() != nil
}

// DestroyUserSessions destroys every session signed in as the user, including those of an
// administrator who is impersonating the user and those in which the user is impersonating
// someone else, and returns how many it destroyed.
// It goes through every stored session, so it is meant for occasional administrative use.
func DestroyUserSessions(ctx context.Context, uid int64) (int, error) {
	provider := activeProvider.Load()
	lister := provider.lister()
	if lister == nil {
		return 0, ErrUserSessionsUnsupported
	}
	// a lister may visit concurrently (a Redis cluster's masters), and may see a session twice
	var mu sync.Mutex
	matched := map[string]struct{}{}
	if err := lister.listSessions(ctx, func(sid string, data map[any]any) {
		if sessionBelongsTo(data, uid) {
			mu.Lock()
			matched[sid] = struct{}{}
			mu.Unlock()
		}
	}); err != nil {
		return 0, fmt.Errorf("list sessions: %w", err)
	}
	destroyed := 0
	for sid := range matched {
		if err := provider.Destroy(sid); errors.Is(err, fs.ErrNotExist) {
			continue // ended meanwhile
		} else if err != nil {
			return destroyed, fmt.Errorf("destroy session: %w", err)
		}
		destroyed++
	}
	return destroyed, nil
}

// sessionBelongsTo reports whether the session is signed in as the user, or the user is the impersonator behind it
func sessionBelongsTo(data map[any]any, uid int64) bool {
	if id, ok := data[KeyUID].(int64); ok && id == uid {
		return true
	}
	if impersonator, ok := data[KeyImpersonatorData].(map[string]any); ok {
		if id, ok := impersonator[KeyUID].(int64); ok && id == uid {
			return true
		}
	}
	return false
}

func decodeSessionData(sid string, data []byte) (map[any]any, bool) {
	if len(data) == 0 {
		return nil, false
	}
	kv, err := session.DecodeGob(data)
	if err != nil {
		// a session written by another version may hold types this one can't decode; it can't be matched
		log.Warn("session: unable to decode session %q, it is left as it is: %v", sid, err)
		return nil, false
	}
	return kv, true
}

func (o *VirtualSessionProvider) lister() sessionLister {
	if o == nil {
		return nil // the session middleware isn't initialized
	}
	o.lock.RLock()
	defer o.lock.RUnlock()
	switch p := o.provider.(type) {
	case *DBProvider:
		return p
	case *RedisProvider:
		return p
	case *session.FileProvider:
		return fileSessionLister(o.fileRoot)
	}
	return nil
}

func (p *DBProvider) listSessions(ctx context.Context, visit func(sid string, data map[any]any)) error {
	return auth.IterateSessions(ctx, func(_ context.Context, s *auth.Session) error {
		if kv, ok := decodeSessionData(s.Key, s.Data); ok {
			visit(s.Key, kv)
		}
		return nil
	})
}

func (p *RedisProvider) listSessions(ctx context.Context, visit func(sid string, data map[any]any)) error {
	return forEachRedisMaster(ctx, p.c, func(ctx context.Context, node redis.Cmdable) error {
		return p.listNodeSessions(ctx, node, visit)
	})
}

// forEachRedisMaster calls fn for the client, or for every master of a cluster (SCAN only sees
// the keys of the node it is sent to), concurrently: fn must be safe for concurrent use
var forEachRedisMaster = func(ctx context.Context, client redis.UniversalClient, fn func(ctx context.Context, node redis.Cmdable) error) error {
	if holder, ok := client.(interface{ Unwrap() redis.UniversalClient }); ok {
		client = holder.Unwrap()
	}
	if cluster, ok := client.(*redis.ClusterClient); ok {
		return cluster.ForEachMaster(ctx, func(ctx context.Context, node *redis.Client) error {
			return fn(ctx, node)
		})
	}
	return fn(ctx, client)
}

func (p *RedisProvider) listNodeSessions(ctx context.Context, node redis.Cmdable, visit func(sid string, data map[any]any)) error {
	// only string keys: with an empty prefix the pattern also matches whatever else shares the database
	pattern := escapeRedisPattern(p.prefix) + "*"
	var cursor uint64
	for {
		keys, next, err := node.ScanType(ctx, cursor, pattern, 1000, "string").Result()
		if err != nil {
			return err
		}
		if len(keys) > 0 {
			// one round trip per batch; not MGET, which a cluster refuses for keys of different slots
			pipe := node.Pipeline()
			gets := make([]*redis.StringCmd, len(keys))
			for i, key := range keys {
				gets[i] = pipe.Get(ctx, key)
			}
			_, _ = pipe.Exec(ctx) // each command's own error is read below
			for i, get := range gets {
				value, err := get.Result()
				if errors.Is(err, redis.Nil) || redis.HasErrorPrefix(err, "WRONGTYPE") {
					continue // expired, destroyed or replaced meanwhile
				} else if err != nil {
					return err
				}
				sid := strings.TrimPrefix(keys[i], p.prefix)
				if kv, ok := decodeSessionData(sid, []byte(value)); ok {
					visit(sid, kv)
				}
			}
		}
		if next == 0 {
			return nil
		}
		cursor = next
	}
}

func escapeRedisPattern(s string) string {
	return strings.NewReplacer(`\`, `\\`, `*`, `\*`, `?`, `\?`, `[`, `\[`, `]`, `\]`).Replace(s)
}

// fileSessionLister goes through the file provider's directory, where a session is stored as <root>/<sid[0]>/<sid[1]>/<sid>
type fileSessionLister string

func (root fileSessionLister) listSessions(ctx context.Context, visit func(sid string, data map[any]any)) error {
	err := filepath.WalkDir(string(root), func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil // removed meanwhile, or no session stored yet
			}
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		sid := d.Name()
		rel, err := filepath.Rel(string(root), path)
		if err != nil || len(sid) < 2 || rel != filepath.Join(sid[0:1], sid[1:2], sid) {
			return nil // not a session file
		}
		data, err := os.ReadFile(path)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		} else if err != nil {
			return err
		}
		if kv, ok := decodeSessionData(sid, data); ok {
			visit(sid, kv)
		}
		return nil
	})
	return err
}

// existingFileStore keeps a request from writing back a file session that was destroyed while it ran
type existingFileStore struct {
	session.RawStore
	provider session.Provider
}

func (s *existingFileStore) Release() error {
	exist, err := s.provider.Exist(s.ID())
	if err != nil {
		return err
	}
	if !exist {
		return nil
	}
	return s.RawStore.Release()
}
