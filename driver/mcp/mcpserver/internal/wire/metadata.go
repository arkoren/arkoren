package wire

import (
	"encoding/json/jsontext"
	"encoding/json/v2"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"arkoren.dev/core/mcp"
)

// nativeRecord snapshots a JSON-v2 record for the SDK's JSON-v1 marshaler.
// jsontext.Value implements both marshal interfaces, so nested raw numbers
// remain exact instead of passing through float64 or base64 byte encoding.
func nativeRecord(value any) (jsontext.Value, error) {
	data, err := json.Marshal(value)

	if err != nil {
		return nil, err
	}

	return jsontext.Value(data), nil
}

// cliMetadata omits absent CLI declarations rather than advertising an empty
// command. Present reports become detached JSON-compatible native metadata.
func cliMetadata(info *mcp.CLIInfo) (sdkmcp.Meta, error) {
	if info == nil {
		return nil, nil
	}

	record, err := nativeRecord(info)

	if err != nil {
		return nil, err
	}

	return sdkmcp.Meta{"arkoren.dev/cli": record}, nil
}
