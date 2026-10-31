package workflow

import "encoding/json/v2"

// Codec converts values to and from the bytes that cross a driver. The
// output of Marshal must be valid JSON, because payloads are wrapped in a JSON
// object envelope.
// Implementations remain shared by clients and registered handlers and must
// support their concurrent use. Use the same encoding in both processes.
//
//	var codec workflow.Codec = workflow.JSON{}
//	client := workflow.NewClient(driver, func(client *workflow.ClientBuilder) {
//		client.Codec(codec)
//	})
//	_ = client.Driver()
type Codec interface {
	// Marshal encodes v as JSON.
	Marshal(v any) ([]byte, error)

	// Unmarshal decodes JSON data into v, which is a pointer.
	Unmarshal(data []byte, v any) error
}

// JSON is the default [Codec]. It uses encoding/json/v2.
// Its zero value is ready to use and holds no mutable state.
//
//	codec := workflow.JSON{}
//	data, err := codec.Marshal("welcome")
//	if err != nil {
//		return err
//	}
//	_ = data
type JSON struct{}

// Marshal encodes v with encoding/json/v2.
// Unsupported values return the JSON encoder's error rather than a payload.
//
//	data, err := (workflow.JSON{}).Marshal(map[string]string{"greeting": "welcome"})
//	if err != nil {
//		return err
//	}
//	_ = data
func (JSON) Marshal(v any) ([]byte, error) {
	return json.Marshal(v)
}

// Unmarshal decodes data with encoding/json/v2.
// v must be an appropriate nonnil destination pointer; invalid JSON and
// incompatible destinations return the decoder's error.
//
//	var greeting string
//	if err := (workflow.JSON{}).Unmarshal([]byte(`"welcome"`), &greeting); err != nil {
//		return err
//	}
//	fmt.Println(greeting)
func (JSON) Unmarshal(data []byte, v any) error {
	return json.Unmarshal(data, v)
}
