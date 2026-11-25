// Package cryptotest provides [Run], a conformance suite that every
// [crypto.Driver] implementation must pass.
//
// Drivers take a key.Ring: the current key encrypts and previous keys only
// decrypt. [WithRing] gives the suite a factory from a ring to a driver so it
// can check rotation: ciphertext written under an old key decrypts through a
// driver whose ring lists that key as previous, and Decrypt returns previous
// as true so the caller knows to re-encrypt. [WithForeignDriver] adds the
// wrong-key check.
//
// # Example
//
//	func TestDriver(t *testing.T) {
//		newDriver := func(t *testing.T, ring key.Ring[key.Shared]) crypto.Driver {
//			driver, err := aescrypto.New(ring)
//			if err != nil {
//				t.Fatal(err)
//			}
//			return driver
//		}
//		cryptotest.Run(t, func(t *testing.T) crypto.Driver {
//			return newDriver(t, ring)
//		}, cryptotest.WithRing(newDriver))
//	}
package cryptotest
