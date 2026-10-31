package cache

import (
	"encoding/json"
	"reflect"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

var protojsonMarshal = protojson.MarshalOptions{
	UseProtoNames:   true,
	EmitUnpopulated: true,
}

// serialize converts a value to a JSON string for cache storage.
// Proto messages use protojson with UseProtoNames (snake_case fields).
// All other values use standard encoding/json.
func serialize(value any) (string, error) {
	if pm, ok := value.(proto.Message); ok {
		b, err := protojsonMarshal.Marshal(pm)
		if err != nil {
			return "", err
		}
		return string(b), nil
	}
	b, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// deserialize populates dest from a JSON string.
// Proto messages use protojson; all other values use standard encoding/json.
func deserialize(data string, dest any) error {
	if pm, ok := dest.(proto.Message); ok {
		return protojson.Unmarshal([]byte(data), pm)
	}
	return json.Unmarshal([]byte(data), dest)
}

// isProtoMessage reports whether the value is a proto.Message.
// If dest is a pointer to a proto.Message, it dereferences one level.
func isProtoMessage(dest any) bool {
	if _, ok := dest.(proto.Message); ok {
		return true
	}
	v := reflect.ValueOf(dest)
	if v.Kind() == reflect.Ptr && !v.IsNil() {
		if _, ok := v.Elem().Interface().(proto.Message); ok {
			return true
		}
	}
	return false
}
