// SPDX-License-Identifier: Apache-2.0

package types

import (
	"fmt"
	"sort"
	"strings"

	"github.com/go-viper/mapstructure/v2"
	"github.com/spf13/viper"
)

// SetConfigKey sets key to value in the configuration v has read and decodes
// the result into config, a pointer to the file's type. A key no field of
// that type takes is refused: viper accepts any key, and the file would be
// saved without it.
func SetConfigKey(v *viper.Viper, config any, key, value string) error {
	v.Set(key, value)

	var md mapstructure.Metadata
	if err := v.Unmarshal(config, func(c *mapstructure.DecoderConfig) { c.Metadata = &md }); err != nil {
		return err
	}
	// Viper keys are case-insensitive. A key under a table no field takes is
	// reported as the table.
	lower := strings.ToLower(key)
	for _, unused := range md.Unused {
		unused = strings.ToLower(unused)
		if lower == unused || strings.HasPrefix(lower, unused+".") {
			return fmt.Errorf("unknown key %q", key)
		}
	}

	return nil
}

// UnknownConfigKeys lists, sorted, the keys in the file v has read that no
// field of config's type takes: "table.key", or "table" for a table no field
// takes. config is a pointer to a value of the file's type to decode into.
// Viper ignores such keys, so a misspelt one is otherwise lost without a word.
func UnknownConfigKeys(v *viper.Viper, config any) ([]string, error) {
	var md mapstructure.Metadata
	if err := v.Unmarshal(config, func(c *mapstructure.DecoderConfig) { c.Metadata = &md }); err != nil {
		return nil, err
	}
	sort.Strings(md.Unused)

	return md.Unused, nil
}
