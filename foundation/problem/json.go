package problem

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
)

// MarshalJSONTo delegates the flattened RFC 9457 representation to JSON v2.
// Zero status is omitted; causes and headers are excluded. Unsupported extension
// values return the encoder's error without HTTP rendering's fallback.
// A status extension still conflicts with the standard member when status is
// omitted, rather than supplying a different advisory status through the map.
//
// Example:
//
//	data, err := json.Marshal(problem.New())
//	if err != nil { panic(err) }
//	_ = data
func (problem Details) MarshalJSONTo(encoder *jsontext.Encoder) error {
	// JSON v2 detects collisions only for emitted members. Status used to be
	// emitted even at zero; keep that collision an error now that it is optional.
	_, conflict := problem.data.Additional["status"]

	if problem.data.Status == 0 && conflict {
		return fmt.Errorf("problem: extension %q conflicts with standard member", "status")
	}

	return json.MarshalEncode(encoder, problem.data)
}

// UnmarshalJSONFrom replaces details only after successful JSON v2 decoding.
// Absent members stay zero, unknown members become extensions, and success
// clears previous causes and headers. Failure leaves details unchanged.
//
// Example:
//
//	var failure problem.Details
//	if err := json.Unmarshal([]byte(`{"type":"about:blank","title":"Missing"}`), &failure); err != nil {
//		panic(err)
//	}
//	_ = failure.Status() // 0: absent on the wire
func (problem *Details) UnmarshalJSONFrom(decoder *jsontext.Decoder) error {
	var data detailsData

	if err := json.UnmarshalDecode(decoder, &data); err != nil {
		return err
	}

	*problem = Details{
		data: data,
	}

	return nil
}
