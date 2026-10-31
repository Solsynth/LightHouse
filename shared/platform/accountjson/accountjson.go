// Package accountjson embeds fleet protos into HTTP/WS payloads. The C#
// reference serialized the decoded model (SnAccount.FromProtoValue and the
// like) beside publishers, chat members and survey answers, so every Go
// service must emit that same wire shape.
package accountjson

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"strconv"
	"strings"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	gen "src.solsynth.dev/sosys/lighthouse/shared/proto"
)

// FromProto embeds a fleet account exactly as System.Text.Json serialized
// SnAccount: snake_case, zero-values and nulls always present, jsonb columns
// parsed and enums numeric.
func FromProto(account *gen.DyAccount) any {
	if account == nil {
		return nil
	}
	return Message(account)
}

// Message renders any fleet proto in the decoded C# wire shape.
//
// protojson alone is not that shape: it has no access to the C# converters, so
// it emits every `bytes` field as base64 (file_meta, user_meta, meta,
// sensitive_marks hold parsed JSON in the database), every enum as its proto
// name, and every 64-bit integer as a string. Clients parse those columns as
// objects, so the base64 leaked into the wire made a Dart client throw
// "type 'String' is not a subtype of type 'Map<String, dynamic>?'".
func Message(message proto.Message) any {
	if message == nil {
		return nil
	}
	raw, err := protojson.MarshalOptions{UseProtoNames: true, EmitUnpopulated: true}.Marshal(message)
	if err != nil {
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		return nil
	}
	return normalize(message.ProtoReflect().Descriptor(), decoded)
}

// decimalFields are proto3 strings that the C# models declared as decimal.
// protojson can only carry those as text, but System.Text.Json wrote a bare
// number and clients parse them as num, so a quoted value is a type error on
// their side. Keyed by message because the field name alone is not a promise
// that the value is numeric (`id` is not).
var decimalFields = map[protoreflect.FullName]map[protoreflect.Name]bool{
	"proto.DyRealm": {"boost_points": true},
}

// normalize rewrites one decoded protojson object: enum names become numbers,
// JSON blobs become parsed values.
func normalize(descriptor protoreflect.MessageDescriptor, value any) any {
	object, ok := value.(map[string]any)
	if !ok {
		return value
	}
	fields := descriptor.Fields()
	for key, item := range object {
		// UseProtoNames above means the decoded keys are proto names, not the
		// JSON (camelCase) names.
		field := fields.ByName(protoreflect.Name(key))
		if field == nil {
			continue
		}
		object[key] = normalizeField(field, item)
		if decimalFields[descriptor.FullName()][field.Name()] {
			object[key] = decimalNumber(item)
		}
	}
	// ModelBase-shaped fleet messages always carry created_at/updated_at (and
	// a nullable deleted_at) in the C# wire shape. protojson emits null for an
	// unset Timestamp, and reference objects such as SnAccountBadgeRef or the
	// realm snapshots have no timestamp fields in the proto at all. Dart
	// parses createdAt/updatedAt with DateTime.parse, so restore the zero
	// Instant wherever C# would have serialized it.
	switch descriptor.FullName() {
	case "proto.DyBadgeReferenceObject", "proto.DyAccountStatus",
		"proto.DySubscriptionReferenceObject",
		"proto.DyRealm", "proto.DyRealmMember", "proto.DyRealmLabel",
		"proto.DyAccount", "proto.DyAccountProfile", "proto.DyAccountContact",
		"proto.DyAccountAuthFactor", "proto.DyAccountBadge",
		"proto.DyAccountConnection", "proto.DyMagicSpell", "proto.DyRelationship":
		fillModelTimestamps(object)
	}
	// DyCloudFile.Url is a proto3 string, so EmitUnpopulated renders an unset
	// URL as "". C# stored SnCloudFileReferenceObject.Url as a nullable string
	// and serialized null for local cloud files (their address is `/drive/
	// files/{id}`). An empty string is not "no URL" to a client: it is a
	// non-null value that beats every `storageUrl ?? fallback` and fails in the
	// image loader with "No host specified in URI". Blank means the same thing
	// as absent here — a real off-site URL is never empty — so emit null.
	if descriptor.FullName() == "proto.DyCloudFile" {
		if url, ok := object["url"].(string); ok && url == "" {
			object["url"] = nil
		}
	}
	return object
}

// zeroInstant is how NodaTime's default Instant serializes in the C# fleet.
const zeroInstant = "0001-01-01T00:00:00Z"

// fillModelTimestamps restores the ModelBase keys System.Text.Json always
// emitted for model-shaped messages. An unset proto Timestamp decodes as a
// present null, so a missing key and a null value are both replaced.
func fillModelTimestamps(object map[string]any) {
	for _, key := range []string{"created_at", "updated_at"} {
		if value, ok := object[key]; !ok || value == nil {
			object[key] = zeroInstant
		}
	}
	if value, ok := object["deleted_at"]; !ok || value == nil {
		object["deleted_at"] = nil
	}
}

func normalizeField(field protoreflect.FieldDescriptor, value any) any {
	if field.IsMap() {
		object, ok := value.(map[string]any)
		if !ok {
			return value
		}
		entry := field.MapValue()
		for key, item := range object {
			object[key] = normalizeElement(entry, item)
		}
		return object
	}
	if field.IsList() {
		list, ok := value.([]any)
		if !ok {
			return value
		}
		for index := range list {
			list[index] = normalizeElement(field, list[index])
		}
		return list
	}
	return normalizeElement(field, value)
}

func normalizeElement(field protoreflect.FieldDescriptor, value any) any {
	switch field.Kind() { //nolint:exhaustive // every other kind is emitted verbatim
	case protoreflect.BytesKind:
		return decodeJSONBlob(field, value)
	case protoreflect.EnumKind:
		return enumNumber(field, value)
	case protoreflect.Int64Kind, protoreflect.Uint64Kind, protoreflect.Sint64Kind,
		protoreflect.Fixed64Kind, protoreflect.Sfixed64Kind:
		return unquoteInteger(value)
	case protoreflect.MessageKind, protoreflect.GroupKind:
		// google.protobuf.Value/Struct/ListValue already decode to plain JSON,
		// and their keys never match this message's field names.
		return normalize(field.Message(), value)
	}
	return value
}

// alwaysObjectBlobs are the jsonb columns the C# models expose as
// non-nullable dictionaries, so an empty one must arrive as {} rather than
// protojson's empty string. Clients cast these fields as Map<String, dynamic>.
var alwaysObjectBlobs = map[protoreflect.FullName]bool{
	"proto.DyCloudFile.file_meta": true,
	"proto.DyCloudFile.user_meta": true,
}

// decodeJSONBlob turns a base64 `bytes` value into the JSON it encodes. JSON
// objects, arrays and the literal null are decoded; other bytes (ciphertext,
// key packages) keep their base64 form, because that is what their consumers
// expect.
func decodeJSONBlob(field protoreflect.FieldDescriptor, value any) any {
	encoded, ok := value.(string)
	if !ok {
		return value
	}
	if encoded == "" {
		if alwaysObjectBlobs[field.FullName()] {
			return map[string]any{}
		}
		return value
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return value
	}
	trimmed := bytes.TrimSpace(raw)
	// Objects and arrays are the jsonb columns clients parse; the literal
	// "null" is how InfraObjectCoder encodes a nil dictionary (for example an
	// unset account-status meta), so it must become JSON null rather than
	// leak through as base64. Other scalars stay base64 because binary
	// payloads can start with a digit or letter.
	if len(trimmed) == 0 ||
		(trimmed[0] != '{' && trimmed[0] != '[' && !bytes.Equal(trimmed, []byte("null"))) {
		return value
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.UseNumber()
	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		return value
	}
	return decoded
}

// enumNumber maps a protojson enum name to the number the C# fleet emitted
// (declaration order); numbers and unknown names pass through unchanged.
func enumNumber(field protoreflect.FieldDescriptor, value any) any {
	name, ok := value.(string)
	if !ok {
		return value
	}
	enum := field.Enum()
	if enum == nil {
		return value
	}
	values := enum.Values()
	for index := range values.Len() {
		entry := values.Get(index)
		if string(entry.Name()) == name {
			// StatusAttitude is the fleet enum whose proto numbering does not
			// match the C# model: the proto inserts Unspecified at 0, while
			// StatusAttitude starts at Positive. SnAccountStatus.FromProtoValue
			// maps Positive/Negative explicitly and everything else to Neutral.
			if enum.FullName() == "proto.DyStatusAttitude" {
				switch entry.Number() {
				case 1:
					return int64(0)
				case 2:
					return int64(1)
				default:
					return int64(2)
				}
			}
			return int64(entry.Number())
		}
	}
	return value
}

// unquoteInteger restores the number System.Text.Json wrote for 64-bit
// integers, which protojson quotes as a string to survive JavaScript parsing.
func unquoteInteger(value any) any {
	text, ok := value.(string)
	if !ok {
		return value
	}
	if _, err := strconv.ParseInt(text, 10, 64); err != nil {
		if _, unsignedErr := strconv.ParseUint(text, 10, 64); unsignedErr != nil {
			return value
		}
	}
	return json.Number(text)
}

// decimalNumber renders a decimal string as a bare JSON number, keeping the
// stored digits. An unset or unparseable value becomes 0, which is what an
// unset C# decimal field serialized as.
func decimalNumber(value any) any {
	text, ok := value.(string)
	if !ok {
		return value
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return json.Number("0")
	}
	if _, err := strconv.ParseFloat(text, 64); err != nil {
		return json.Number("0")
	}
	return json.Number(text)
}
