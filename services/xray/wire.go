// SPDX-License-Identifier: Apache-2.0

package xray

// The node drives xray over its gRPC API with four messages. They are encoded
// here by hand with protowire instead of importing xray-core's generated
// code: xray-core and v2ray-core register the same proto file paths, so the
// two cannot be linked into one binary. The field numbers and type names are
// the API's public contract; the tests check the bytes against xray-core's
// own generated code, which stays a test-only dependency.

import (
	"context"
	"fmt"

	"google.golang.org/grpc"
	"google.golang.org/grpc/encoding"
	"google.golang.org/protobuf/encoding/protowire"
)

const (
	methodAlterInbound = "/xray.app.proxyman.command.HandlerService/AlterInbound"
	methodQueryStats   = "/xray.app.stats.command.StatsService/QueryStats"

	typeAddUserOperation    = "xray.app.proxyman.command.AddUserOperation"
	typeRemoveUserOperation = "xray.app.proxyman.command.RemoveUserOperation"
	typeVLESSAccount        = "xray.proxy.vless.Account"
)

// rawMessage carries already-encoded protobuf bytes through gRPC.
type rawMessage []byte

// rawCodec passes rawMessage bytes through unchanged.
type rawCodec struct{}

func (rawCodec) Name() string { return "proto" }

func (rawCodec) Marshal(v interface{}) ([]byte, error) {
	m, ok := v.(*rawMessage)
	if !ok {
		return nil, fmt.Errorf("rawCodec: unexpected %T", v)
	}

	return *m, nil
}

func (rawCodec) Unmarshal(data []byte, v interface{}) error {
	m, ok := v.(*rawMessage)
	if !ok {
		return fmt.Errorf("rawCodec: unexpected %T", v)
	}
	*m = append((*m)[:0], data...)

	return nil
}

var _ encoding.Codec = rawCodec{}

// invoke calls a unary method with pre-encoded bytes and returns the reply bytes.
func invoke(ctx context.Context, conn *grpc.ClientConn, method string, req []byte) ([]byte, error) {
	in := rawMessage(req)
	var out rawMessage
	if err := conn.Invoke(ctx, method, &in, &out, grpc.ForceCodec(rawCodec{})); err != nil {
		return nil, err
	}

	return out, nil
}

func appendString(b []byte, num protowire.Number, s string) []byte {
	if s == "" {
		return b
	}
	b = protowire.AppendTag(b, num, protowire.BytesType)

	return protowire.AppendString(b, s)
}

func appendMessage(b []byte, num protowire.Number, m []byte) []byte {
	b = protowire.AppendTag(b, num, protowire.BytesType)

	return protowire.AppendBytes(b, m)
}

func appendBool(b []byte, num protowire.Number, v bool) []byte {
	if !v {
		return b
	}
	b = protowire.AppendTag(b, num, protowire.VarintType)

	return protowire.AppendVarint(b, 1)
}

// typedMessage is xray.common.serial.TypedMessage {type = 1, value = 2}.
func typedMessage(typeName string, value []byte) []byte {
	var b []byte
	b = appendString(b, 1, typeName)

	return appendMessage(b, 2, value)
}

// vlessAccount is xray.proxy.vless.Account {id = 1, flow = 2, encryption = 3}.
func vlessAccount(id, flow, encryption string) []byte {
	var b []byte
	b = appendString(b, 1, id)
	b = appendString(b, 2, flow)

	return appendString(b, 3, encryption)
}

// user is xray.common.protocol.User {level = 1, email = 2, account = 3};
// level 0 is the proto default and is left out.
func user(email string, account []byte) []byte {
	var b []byte
	b = appendString(b, 2, email)

	return appendMessage(b, 3, account)
}

// alterInboundRequest is xray.app.proxyman.command.AlterInboundRequest
// {tag = 1, operation = 2}.
func alterInboundRequest(tag string, operation []byte) []byte {
	var b []byte
	b = appendString(b, 1, tag)

	return appendMessage(b, 2, operation)
}

// addUserRequest builds AlterInbound(tag, AddUserOperation{user = 1}).
func addUserRequest(tag, email, uuid, flow string) []byte {
	account := typedMessage(typeVLESSAccount, vlessAccount(uuid, flow, "none"))
	operation := appendMessage(nil, 1, user(email, account))

	return alterInboundRequest(tag, typedMessage(typeAddUserOperation, operation))
}

// removeUserRequest builds AlterInbound(tag, RemoveUserOperation{email = 1}).
func removeUserRequest(tag, email string) []byte {
	operation := appendString(nil, 1, email)

	return alterInboundRequest(tag, typedMessage(typeRemoveUserOperation, operation))
}

// queryStatsRequest is xray.app.stats.command.QueryStatsRequest
// {pattern = 1, reset = 2}.
func queryStatsRequest(pattern string, reset bool) []byte {
	var b []byte
	b = appendString(b, 1, pattern)

	return appendBool(b, 2, reset)
}

// parseQueryStatsResponse reads QueryStatsResponse {stat = 1 repeated} where
// Stat is {name = 1, value = 2} into a name → value map.
func parseQueryStatsResponse(b []byte) (map[string]int64, error) {
	stats := map[string]int64{}

	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return nil, protowire.ParseError(n)
		}
		b = b[n:]

		if num != 1 || typ != protowire.BytesType {
			n = protowire.ConsumeFieldValue(num, typ, b)
			if n < 0 {
				return nil, protowire.ParseError(n)
			}
			b = b[n:]

			continue
		}

		stat, n := protowire.ConsumeBytes(b)
		if n < 0 {
			return nil, protowire.ParseError(n)
		}
		b = b[n:]

		name, value, err := parseStat(stat)
		if err != nil {
			return nil, err
		}
		stats[name] = value
	}

	return stats, nil
}

func parseStat(b []byte) (name string, value int64, err error) {
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return "", 0, protowire.ParseError(n)
		}
		b = b[n:]

		switch {
		case num == 1 && typ == protowire.BytesType:
			s, n := protowire.ConsumeString(b)
			if n < 0 {
				return "", 0, protowire.ParseError(n)
			}
			name, b = s, b[n:]
		case num == 2 && typ == protowire.VarintType:
			v, n := protowire.ConsumeVarint(b)
			if n < 0 {
				return "", 0, protowire.ParseError(n)
			}
			value, b = int64(v), b[n:] //nolint:gosec // G115: a protobuf int64 travels as its two's-complement uint64
		default:
			n = protowire.ConsumeFieldValue(num, typ, b)
			if n < 0 {
				return "", 0, protowire.ParseError(n)
			}
			b = b[n:]
		}
	}

	return name, value, nil
}
