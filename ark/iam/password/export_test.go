package password

// Refund exposes [Verifier.refund] to the external tests, which cannot reach a
// counter that expired between its reservation and its refund any other way.
var Refund = (*Verifier).refund
