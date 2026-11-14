// SPDX-License-Identifier: Apache-2.0
// Modified from sentinel-official/dvpn-node @ 62bde16 (2024-01-25). See NOTICE.

package lite

import (
	"github.com/Feryx-Official/feryxhub/v12/x/vpn"
	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	sdkstd "github.com/cosmos/cosmos-sdk/std"
	"github.com/cosmos/cosmos-sdk/types/module"
	"github.com/cosmos/cosmos-sdk/x/auth"
	authtx "github.com/cosmos/cosmos-sdk/x/auth/tx"
	authvesting "github.com/cosmos/cosmos-sdk/x/auth/vesting"
	authzmodule "github.com/cosmos/cosmos-sdk/x/authz/module"
	feegrantmodule "github.com/cosmos/cosmos-sdk/x/feegrant/module"
)

type EncodingConfig struct {
	Amino             *codec.LegacyAmino
	Codec             codec.Codec
	InterfaceRegistry codectypes.InterfaceRegistry
	TxConfig          client.TxConfig
}

func NewEncodingConfig() EncodingConfig {
	var (
		amino             = codec.NewLegacyAmino()
		interfaceRegistry = codectypes.NewInterfaceRegistry()
		cdc               = codec.NewProtoCodec(interfaceRegistry)
		txConfig          = authtx.NewTxConfig(cdc, authtx.DefaultSignModes)
	)

	return EncodingConfig{
		Amino:             amino,
		Codec:             cdc,
		InterfaceRegistry: interfaceRegistry,
		TxConfig:          txConfig,
	}
}

// DefaultEncodingConfig registers the auth and vesting account types, authz
// and feegrant (a node that signs with a hot key sends MsgExec and reads its
// grants), plus every Sentinel module interface (v1, v2 and v3) so that
// Any-typed query results such as sessions can be unpacked.
func DefaultEncodingConfig() EncodingConfig {
	var (
		cfg     = NewEncodingConfig()
		modules = module.NewBasicManager(
			auth.AppModuleBasic{},
			authvesting.AppModuleBasic{},
			authzmodule.AppModuleBasic{},
			feegrantmodule.AppModuleBasic{},
			vpn.AppModuleBasic{},
		)
	)

	sdkstd.RegisterLegacyAminoCodec(cfg.Amino)
	sdkstd.RegisterInterfaces(cfg.InterfaceRegistry)
	modules.RegisterLegacyAminoCodec(cfg.Amino)
	modules.RegisterInterfaces(cfg.InterfaceRegistry)

	return cfg
}
