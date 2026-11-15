// SPDX-License-Identifier: Apache-2.0
// Modified from sentinel-official/dvpn-node @ 62bde16 (2024-01-25). See NOTICE.

package lite

import (
	feegrantmodule "cosmossdk.io/x/feegrant/module"
	"cosmossdk.io/x/tx/signing"
	base "github.com/Feryx-Official/feryxhub/v12/types"
	"github.com/Feryx-Official/feryxhub/v12/x/vpn"
	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/codec"
	"github.com/cosmos/cosmos-sdk/codec/address"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	sdkstd "github.com/cosmos/cosmos-sdk/std"
	"github.com/cosmos/cosmos-sdk/types/module"
	"github.com/cosmos/cosmos-sdk/x/auth"
	authtx "github.com/cosmos/cosmos-sdk/x/auth/tx"
	authvesting "github.com/cosmos/cosmos-sdk/x/auth/vesting"
	authzmodule "github.com/cosmos/cosmos-sdk/x/authz/module"
	gogoproto "github.com/cosmos/gogoproto/proto"
)

type EncodingConfig struct {
	Amino             *codec.LegacyAmino
	Codec             codec.Codec
	InterfaceRegistry codectypes.InterfaceRegistry
	TxConfig          client.TxConfig
}

// modules are the module interfaces the node registers: the auth and vesting
// account types, authz and feegrant (a node that signs with a hot key sends
// MsgExec and reads its grants), plus every chain module interface (v1, v2
// and v3) so that Any-typed query results such as sessions can be unpacked.
var modules = module.NewBasicManager(
	auth.AppModuleBasic{},
	authvesting.AppModuleBasic{},
	authzmodule.AppModuleBasic{},
	feegrantmodule.AppModuleBasic{},
	vpn.AppModuleBasic{},
)

// NewEncodingConfig builds the codec the way the chain builds its own: Feryx
// address prefixes, and the chain's signer extraction for every feryx.*
// message, which names its signers with its own GetSigners because none
// carries the SDK's signer annotation. The SDK resolves a transaction's
// signers through it.
func NewEncodingConfig() EncodingConfig {
	interfaceRegistry, err := codectypes.NewInterfaceRegistryWithOptions(codectypes.InterfaceRegistryOptions{
		ProtoFiles: gogoproto.HybridResolver,
		SigningOptions: signing.Options{
			AddressCodec:          address.Bech32Codec{Bech32Prefix: base.Bech32PrefixAccAddr},
			ValidatorAddressCodec: address.Bech32Codec{Bech32Prefix: base.Bech32PrefixValAddr},
			CustomGetSigners:      base.Signers(modules.RegisterInterfaces),
		},
	})
	if err != nil {
		panic(err)
	}

	var (
		amino    = codec.NewLegacyAmino()
		cdc      = codec.NewProtoCodec(interfaceRegistry)
		txConfig = authtx.NewTxConfig(cdc, authtx.DefaultSignModes)
	)

	return EncodingConfig{
		Amino:             amino,
		Codec:             cdc,
		InterfaceRegistry: interfaceRegistry,
		TxConfig:          txConfig,
	}
}

// DefaultEncodingConfig registers the modules' interfaces and amino types.
func DefaultEncodingConfig() EncodingConfig {
	cfg := NewEncodingConfig()

	sdkstd.RegisterLegacyAminoCodec(cfg.Amino)
	sdkstd.RegisterInterfaces(cfg.InterfaceRegistry)
	modules.RegisterLegacyAminoCodec(cfg.Amino)
	modules.RegisterInterfaces(cfg.InterfaceRegistry)

	return cfg
}
