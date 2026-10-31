package lifecycle

import "errors"

// ErrInvalidPhase identifies calls outside the admitted execution phase.
var ErrInvalidPhase = errors.New("invalid application phase")

// ErrNilFunc identifies missing callbacks and empty check names.
var ErrNilFunc = errors.New("nil function")
