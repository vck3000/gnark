/*
Copyright © 2020 ConsenSys

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package sw_bls12377

import (
	"math/big"

	"github.com/consensys/gnark/frontend"
	"github.com/consensys/gnark/std/algebra/fields_bls12377"
)

// PairingContext contains useful info about the pairing
type PairingContext struct {
	AteLoop     uint64 // stores the ate loop
	Extension   fields_bls12377.Extension
	BTwistCoeff fields_bls12377.E2
}

// LineEvaluation represents a sparse Fp12 Elmt (result of the line evaluation)
type LineEvaluation struct {
	R0, R1 fields_bls12377.E2
}

// MillerLoop computes the miller loop
func MillerLoop(api frontend.API, P G1Affine, Q G2Affine, res *fields_bls12377.E12, pairingInfo PairingContext) *fields_bls12377.E12 {

	var ateLoopBin [64]uint
	var ateLoopBigInt big.Int
	ateLoopBigInt.SetUint64(pairingInfo.AteLoop)
	for i := 0; i < 64; i++ {
		ateLoopBin[i] = ateLoopBigInt.Bit(i)
	}

	res.SetOne(api)

	var l1, l2 LineEvaluation
	var Qacc G2Affine
	Qacc = Q
	yInv := api.DivUnchecked(1, P.Y)
	xOverY := api.DivUnchecked(P.X, P.Y)

	for i := len(ateLoopBin) - 2; i >= 0; i-- {
		res.Square(api, *res, pairingInfo.Extension)

		if ateLoopBin[i] == 0 {
			Qacc, l1 = DoubleStep(api, &Qacc, pairingInfo.Extension)
			l1.R0.MulByFp(api, l1.R0, xOverY)
			l1.R1.MulByFp(api, l1.R1, yInv)
			res.MulBy034(api, l1.R0, l1.R1, pairingInfo.Extension)
			continue
		}

		Qacc, l1, l2 = DoubleAndAddStep(api, &Qacc, &Q, pairingInfo.Extension)
		l1.R0.MulByFp(api, l1.R0, xOverY)
		l1.R1.MulByFp(api, l1.R1, yInv)
		res.MulBy034(api, l1.R0, l1.R1, pairingInfo.Extension)
		l2.R0.MulByFp(api, l2.R0, xOverY)
		l2.R1.MulByFp(api, l2.R1, yInv)
		res.MulBy034(api, l2.R0, l2.R1, pairingInfo.Extension)
	}

	return res
}

// DoubleAndAddStep
func DoubleAndAddStep(api frontend.API, p1, p2 *G2Affine, ext fields_bls12377.Extension) (G2Affine, LineEvaluation, LineEvaluation) {

	var n, d, l1, l2, x3, x4, y4 fields_bls12377.E2
	var line1, line2 LineEvaluation
	var p G2Affine

	// compute lambda1 = (y2-y1)/(x2-x1)
	n.Sub(api, p1.Y, p2.Y)
	d.Sub(api, p1.X, p2.X)
	l1.Inverse(api, d, ext).Mul(api, l1, n, ext)

	// x3 =lambda1**2-p1.x-p2.x
	x3.Square(api, l1, ext).
		Sub(api, x3, p1.X).
		Sub(api, x3, p2.X)

		// omit y3 computation

		// compute line1
	line1.R0.Neg(api, l1)
	line1.R1.Mul(api, l1, p1.X, ext).Sub(api, line1.R1, p1.Y)

	// compute lambda2 = -lambda1-2*y1/(x3-x1)
	n.Double(api, p1.Y)
	d.Sub(api, x3, p1.X)
	l2.Inverse(api, d, ext).Mul(api, l2, n, ext)
	l2.Add(api, l2, l1).Neg(api, l2)

	// compute x4 = lambda2**2-x1-x3
	x4.Square(api, l2, ext).
		Sub(api, x4, p1.X).
		Sub(api, x4, x3)

	// compute y4 = lambda2*(x1 - x4)-y1
	y4.Sub(api, p1.X, x4).
		Mul(api, l2, y4, ext).
		Sub(api, y4, p1.Y)

	p.X = x4
	p.Y = y4

	// compute line2
	line2.R0.Neg(api, l2)
	line2.R1.Mul(api, l2, p1.X, ext).Sub(api, line2.R1, p1.Y)

	return p, line1, line2
}

func DoubleStep(api frontend.API, p1 *G2Affine, ext fields_bls12377.Extension) (G2Affine, LineEvaluation) {

	var n, d, l, xr, yr fields_bls12377.E2
	var p G2Affine
	var line LineEvaluation

	// lambda = 3*p1.x**2/2*p.y
	n.Square(api, p1.X, ext).MulByFp(api, n, 3)
	d.MulByFp(api, p1.Y, 2)
	l.Inverse(api, d, ext).Mul(api, l, n, ext)

	// xr = lambda**2-2*p1.x
	xr.Square(api, l, ext).
		Sub(api, xr, p1.X).
		Sub(api, xr, p1.X)

	// yr = lambda*(p.x-xr)-p.y
	yr.Sub(api, p1.X, xr).
		Mul(api, l, yr, ext).
		Sub(api, yr, p1.Y)

	p.X = xr
	p.Y = yr

	line.R0.Neg(api, l)
	line.R1.Mul(api, l, p1.X, ext).Sub(api, line.R1, p1.Y)

	return p, line

}

// TripleMillerLoop computes the product of three miller loops
func TripleMillerLoop(api frontend.API, P [3]G1Affine, Q [3]G2Affine, res *fields_bls12377.E12, pairingInfo PairingContext) *fields_bls12377.E12 {

	var ateLoopBin [64]uint
	var ateLoopBigInt big.Int
	ateLoopBigInt.SetUint64(pairingInfo.AteLoop)
	for i := 0; i < 64; i++ {
		ateLoopBin[i] = ateLoopBigInt.Bit(i)
	}

	res.SetOne(api)

	var l1, l2 LineEvaluation
	Qacc := make([]G2Affine, 3)
	yInv := make([]frontend.Variable, 3)
	xOverY := make([]frontend.Variable, 3)
	for k := 0; k < 3; k++ {
		Qacc[k] = Q[k]
		yInv[k] = api.DivUnchecked(1, P[k].Y)
		xOverY[k] = api.DivUnchecked(P[k].X, P[k].Y)
	}

	for i := len(ateLoopBin) - 2; i >= 0; i-- {
		res.Square(api, *res, pairingInfo.Extension)

		if ateLoopBin[i] == 0 {
			for k := 0; k < 3; k++ {
				Qacc[k], l1 = DoubleStep(api, &Qacc[k], pairingInfo.Extension)
				l1.R0.MulByFp(api, l1.R0, xOverY[k])
				l1.R1.MulByFp(api, l1.R1, yInv[k])
				res.MulBy034(api, l1.R0, l1.R1, pairingInfo.Extension)
			}
			continue
		}

		for k := 0; k < 3; k++ {
			Qacc[k], l1, l2 = DoubleAndAddStep(api, &Qacc[k], &Q[k], pairingInfo.Extension)
			l1.R0.MulByFp(api, l1.R0, xOverY[k])
			l1.R1.MulByFp(api, l1.R1, yInv[k])
			res.MulBy034(api, l1.R0, l1.R1, pairingInfo.Extension)
			l2.R0.MulByFp(api, l2.R0, xOverY[k])
			l2.R1.MulByFp(api, l2.R1, yInv[k])
			res.MulBy034(api, l2.R0, l2.R1, pairingInfo.Extension)
		}
	}

	return res
}
