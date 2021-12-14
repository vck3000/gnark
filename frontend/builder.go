package frontend

import (
	"github.com/consensys/gnark/internal/backend/compiled"
)

// Builder represents an object that can builds a constraint system
// TODO @gbotrel maybe ConstraintSystemBuilder?
type Builder interface {
	API
	NewPublicVariable(name string) Variable
	NewSecretVariable(name string) Variable
	Compile() (compiled.ConstraintSystem, error)
}
