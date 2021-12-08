package circuits

import (
	"math/big"

	"github.com/consensys/gnark-crypto/ecc"
	"github.com/consensys/gnark/frontend"
	"github.com/consensys/gnark/frontend/cs"
)

type invCircuit struct {
	A cs.Variable
	C cs.Variable `gnark:",public"`
}

func (circuit *invCircuit) Define(api frontend.API) error {
	d := api.Inverse(circuit.A)
	e := api.Inverse(2387287246)
	api.AssertIsEqual(d, circuit.C)
	api.AssertIsEqual(e, circuit.C)
	return nil
}

func init() {

	var good, bad invCircuit

	a := big.NewInt(2387287246)
	m := ecc.BLS12_377.Info().Fp.Modulus()
	var c big.Int
	c.ModInverse(a, m)

	good.A = a
	good.C = c

	bad.A = a
	bad.C = 1

	addEntry("inv", &invCircuit{}, &good, &bad, []ecc.ID{ecc.BW6_761})
}
