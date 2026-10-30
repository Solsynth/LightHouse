// Package accountjson embeds fleet accounts into HTTP/WS payloads. The C#
// reference serialized the full SnAccount (SnAccount.FromProtoValue) beside
// publishers, chat members and survey answers; every Go service must emit the
// same full DyAccount wire shape, so the mapping lives here, once.
package accountjson

import (
	"encoding/json"

	"google.golang.org/protobuf/encoding/protojson"
	gen "src.solsynth.dev/sosys/go/proto"
)

// FromProto embeds the full DyAccount (snake_case, unpopulated fields emitted)
// exactly as System.Text.Json serialized SnAccount: zero-values and nulls are
// always present, so client parsers never see a missing key.
func FromProto(account *gen.DyAccount) any {
	if account == nil {
		return nil
	}
	raw, err := protojson.MarshalOptions{UseProtoNames: true, EmitUnpopulated: true}.Marshal(account)
	if err != nil {
		return nil
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil
	}
	return decoded
}
