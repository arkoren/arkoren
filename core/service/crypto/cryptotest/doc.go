// Package cryptotest provides [Run], a conformance suite that every
// [crypto.Driver] implementation must pass.
//
// # Example
//
//	func TestDriver(t *testing.T) {
//		cryptotest.Run(t, func(t *testing.T) crypto.Driver {
//			driver, err := aes.New(key)
//			if err != nil {
//				t.Fatal(err)
//			}
//			return driver
//		})
//	}
package cryptotest
