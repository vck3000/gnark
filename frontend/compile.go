package frontend

import (
	"errors"
	"fmt"
	"reflect"
	"runtime/debug"

	"github.com/consensys/gnark/internal/backend/compiled"
	"github.com/consensys/gnark/internal/parser"
)

// Compile will generate a ConstraintSystem from the given circuit
//
// 1. it will first allocate the user inputs (see type Tag for more info)
// example:
// 		type MyCircuit struct {
// 			Y frontend.Variable `gnark:"exponent,public"`
// 		}
// in that case, Compile() will allocate one public variable with id "exponent"
//
// 2. it then calls circuit.Define(curveID, R1CS) to build the internal constraint system
// from the declarative code
//
// 3. finally, it converts that to a ConstraintSystem.
// 		if zkpID == backend.GROTH16	--> R1CS
//		if zkpID == backend.PLONK 	--> SparseR1CS
//
// initialCapacity is an optional parameter that reserves memory in slices
// it should be set to the estimated number of constraints in the circuit, if known.
func Compile(builder Builder, circuit Circuit, opts ...func(opt *CompileOption) error) (compiled.ConstraintSystem, error) {
	// setup option
	opt := CompileOption{}
	for _, o := range opts {
		if err := o(&opt); err != nil {
			return nil, fmt.Errorf("apply option: %w", err)
		}
	}

	ccs, err := compile(circuit, builder)
	if err != nil {
		return nil, fmt.Errorf("compile: %w", err)
	}
	return ccs, nil
}

// CompileOption enables to set optional argument to call of frontend.Compile()
type CompileOption struct {
	capacity                  int
	ignoreUnconstrainedInputs bool
}

// WithOutput is a Compile option that specifies the estimated capacity needed for internal variables and constraints
func WithCapacity(capacity int) func(opt *CompileOption) error {
	return func(opt *CompileOption) error {
		opt.capacity = capacity
		return nil
	}
}

// IgnoreUnconstrainedInputs when set, the Compile function doesn't check for unconstrained inputs
func IgnoreUnconstrainedInputs(opt *CompileOption) error {
	opt.ignoreUnconstrainedInputs = true
	return nil
}

// buildCS builds the constraint system. It bootstraps the inputs
// allocations by parsing the circuit's underlying structure, then
// it builds the constraint system using the Define method.
func compile(circuit Circuit, builder Builder) (ccs compiled.ConstraintSystem, err error) {
	// leaf handlers are called when encoutering leafs in the circuit data struct
	// leafs are Constraints that need to be initialized in the context of compiling a circuit
	var handler parser.LeafHandler = func(visibility compiled.Visibility, name string, tInput reflect.Value) error {
		if tInput.CanSet() {
			switch visibility {
			case compiled.Secret:
				tInput.Set(reflect.ValueOf(builder.NewSecretVariable(name)))
			case compiled.Public:
				tInput.Set(reflect.ValueOf(builder.NewPublicVariable(name)))
			case compiled.Unset:
				return errors.New("can't set val " + name + " visibility is unset")
			}

			return nil
		}
		return errors.New("can't set val " + name)
	}
	// recursively parse through reflection the circuits members to find all Constraints that need to be allocated
	// (secret or public inputs)
	if err := parser.Visit(circuit, "", compiled.Unset, handler, tVariable); err != nil {
		return nil, err
	}

	// recover from panics to print user-friendlier messages
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("%v\n%s", r, debug.Stack())
		}
	}()

	// call Define() to fill in the Constraints
	if err = circuit.Define(builder); err != nil {
		return nil, fmt.Errorf("define circuit: %w", err)
	}

	ccs, err = builder.Compile()
	if err != nil {
		return nil, fmt.Errorf("compile system: %w", err)
	}

	return
}

var tVariable reflect.Type

func init() {
	tVariable = reflect.ValueOf(struct{ A Variable }{}).FieldByName("A").Type()
}
