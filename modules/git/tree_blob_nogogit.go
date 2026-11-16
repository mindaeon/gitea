// Copyright 2020 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

//go:build !gogit

package git

import (
	"context"
	"strings"
)

// GetTreeEntryByPath get the tree entries according the sub dir
func (t *Tree) GetTreeEntryByPath(ctx context.Context, gitRepo *Repository, relpath string) (_ *TreeEntry, err error) {
	if relpath == "" {
		return &TreeEntry{
			ptree:     t,
			ID:        t.ID,
			name:      "",
			entryMode: EntryModeTree,
		}, nil
	}

	parts := strings.Split(relpath, "/")

	tree := t
	for _, name := range parts[:len(parts)-1] {
		tree, err = tree.SubTree(ctx, gitRepo, name)
		if err != nil {
			return nil, err
		}
	}

	name := parts[len(parts)-1]
	entries, err := tree.ListEntries(ctx, gitRepo)
	if err != nil {
		return nil, err
	}
	for _, v := range entries {
		if v.Name() == name {
			return v, nil
		}
	}
	return nil, ErrNotExist{"", relpath}
}
