package handler

import (
	"encoding/json"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// protoEmit marshals protobuf messages to JSON with ALL fields present
// (including zero/false defaults) and snake_case field names — matching the
// existing struct-json field names while fixing the proto3 omit-default
// behaviour, so list responses never drop a field just because its value is
// false/0 (e.g. metrics_available, item_count). Detail endpoints already shape
// these explicitly; this brings the list endpoints in line.
var protoEmit = protojson.MarshalOptions{EmitUnpopulated: true, UseProtoNames: true}

// protoJSON marshals one proto message to a json.RawMessage with defaults
// emitted. Returns null on error (never panics).
func protoJSON(m proto.Message) json.RawMessage {
	b, err := protoEmit.Marshal(m)
	if err != nil {
		return json.RawMessage("null")
	}
	return json.RawMessage(b)
}

// protoJSONSlice maps a slice of proto messages through protoJSON.
func protoJSONSlice[T proto.Message](items []T) []json.RawMessage {
	out := make([]json.RawMessage, len(items))
	for i, m := range items {
		out[i] = protoJSON(m)
	}
	return out
}
