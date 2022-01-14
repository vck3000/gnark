package r1cs_test

import (
	"testing"

	"github.com/consensys/gnark/frontend"
	"github.com/consensys/gnark/test"
)

type CompareCircuit struct {
	A              frontend.Variable
	B              frontend.Variable
	ExpectedOutput frontend.Variable
}

func (c *CompareCircuit) Define(api frontend.API) error {
	output := api.LessThan(c.A, c.B)

	api.AssertIsEqual(output, c.ExpectedOutput)

	return nil
}

func TestCompare(t *testing.T) {
	assert := test.NewAssert(t)

	var expCircuit CompareCircuit

	assert.ProverSucceeded(&expCircuit, &CompareCircuit{
		A:              9,
		B:              10,
		ExpectedOutput: 1,
	})

	// assert.ProverSucceeded(&expCircuit, &CompareCircuit{
	// 	A:              11,
	// 	B:              10,
	// 	ExpectedOutput: 0,
	// })

	// assert.ProverSucceeded(&expCircuit, &CompareCircuit{
	// 	A:              10,
	// 	B:              10,
	// 	ExpectedOutput: 0,
	// })
}
