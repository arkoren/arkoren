// Package hashtest provides [Run], a conformance suite that every
// [hash.Driver] implementation must pass.
//
// # Example
//
//	func TestDriver(t *testing.T) {
//		hashtest.Run(t, func(t *testing.T) hash.Driver {
//			return bcrypthash.New(func(hasher *bcrypthash.Builder) {
//				hasher.Cost(4)
//			})
//		})
//	}
package hashtest
