// SPDX-License-Identifier: Apache-2.0
// Modified from sentinel-official/dvpn-node @ 62bde16 (2024-01-25). See NOTICE.

package context

import (
	"encoding/base64"

	"github.com/Feryx-Official/dvpnd/v9/types"
)

func (c *Context) RemovePeer(key string) error {
	c.Log().Info("Removing the peer from service", "key", types.KeyTag(key))

	data, err := base64.StdEncoding.DecodeString(key)
	if err != nil {
		c.Log().Error("failed to decode the key", "error", err, "key", types.KeyTag(key))
		return err
	}

	if err = c.Service().RemovePeer(data); err != nil {
		c.Log().Error("failed to remove the peer from service", "error", err, "key", types.KeyTag(key))
		return err
	}

	return nil
}

// StoreUsage writes the totals the service's counters for a peer give into
// its session's row, if they moved since the last write; it says whether
// they did. The caller holds the admission lock and read the counters under
// it, so an older reading never overwrites a newer one.
func (c *Context) StoreUsage(item types.Session, peer types.Peer) (bool, error) {
	upload, download, moved := item.Usage(peer)
	if !moved {
		return false, nil
	}

	// Both columns by name: a struct update would skip a zero.
	err := c.Database().Model(&types.Session{}).Where(&types.Session{ID: item.ID}).Updates(
		map[string]interface{}{
			"upload":   upload,
			"download": download,
		},
	).Error

	return true, err
}

func (c *Context) HasPeer(key string) (bool, error) {
	data, err := base64.StdEncoding.DecodeString(key)
	if err != nil {
		c.Log().Error("failed to decode the key", "error", err, "key", types.KeyTag(key))
		return false, err
	}

	return c.Service().HasPeer(data), nil
}

func (c *Context) RemovePeerIfExists(key string) error {
	ok, err := c.HasPeer(key)
	if err != nil {
		return err
	}
	if !ok {
		c.Log().Debug("Peer does not exist", "key", types.KeyTag(key))
		return nil
	}

	return c.RemovePeer(key)
}
