// Copyright 2020 ConsenSys Software Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Code generated by gnark DO NOT EDIT

package plonk

import (
	"math/big"
	"math/bits"
	"runtime"
	"sync"

	"github.com/consensys/gnark-crypto/ecc/bls12-381/fr"

	"github.com/consensys/gnark-crypto/ecc/bls12-381/fr/polynomial"

	"github.com/consensys/gnark-crypto/ecc/bls12-381/fr/kzg"

	"github.com/consensys/gnark-crypto/ecc/bls12-381/fr/fft"

	bls12_381witness "github.com/consensys/gnark/internal/backend/bls12-381/witness"

	"github.com/consensys/gnark/internal/backend/bls12-381/cs"

	"github.com/consensys/gnark-crypto/ecc"
	"github.com/consensys/gnark-crypto/fiat-shamir"
	"github.com/consensys/gnark/internal/utils"
)

type Proof struct {

	// Commitments to the solution vectors
	LRO [3]kzg.Digest

	// Commitment to Z, the permutation polynomial
	Z kzg.Digest

	// Commitments to h1, h2, h3 such that h = h1 + Xh2 + X**2h3 is the quotient polynomial
	H [3]kzg.Digest

	// Batch opening proof of h1 + zeta*h2 + zeta**2h3, linearizedPolynomial, l, r, o, s1, s2
	BatchedProof kzg.BatchOpeningProof

	// Opening proof of Z at zeta*mu
	ZShiftedOpening kzg.OpeningProof
}

// Prove from the public data
func Prove(spr *cs.SparseR1CS, pk *ProvingKey, fullWitness bls12_381witness.Witness) (*Proof, error) {

	// create a transcript manager to apply Fiat Shamir
	fs := fiatshamir.NewTranscript(fiatshamir.SHA256, "gamma", "alpha", "zeta")

	// result
	proof := &Proof{}

	// compute the solution
	solution, err := spr.Solve(fullWitness)
	if err != nil {
		return nil, err
	}

	// query l, r, o in Lagrange basis, not blinded
	ll, lr, lo := computeLRO(spr, pk, solution)

	// save ll, lr, lo, and make a copy of them in canonical basis.
	sizeDomainNum := int64(pk.DomainNum.Cardinality)
	// allocate more capacity to reuse for blinded polynomials
	cl := make(polynomial.Polynomial, sizeDomainNum, sizeDomainNum+2)
	cr := make(polynomial.Polynomial, sizeDomainNum, sizeDomainNum+2)
	co := make(polynomial.Polynomial, sizeDomainNum, sizeDomainNum+2)
	copy(cl, ll)
	copy(cr, lr)
	copy(co, lo)
	pk.DomainNum.FFTInverse(cl, fft.DIF, 0)
	pk.DomainNum.FFTInverse(cr, fft.DIF, 0)
	pk.DomainNum.FFTInverse(co, fft.DIF, 0)
	{
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			fft.BitReverse(cl)
			wg.Done()
		}()
		go func() {
			fft.BitReverse(cr)
			wg.Done()
		}()
		fft.BitReverse(co)
		wg.Wait()
	}

	// blind cl, cr, co before committing to them
	bcl := blindPoly(cl, pk.DomainNum.Cardinality, 1)
	bcr := blindPoly(cr, pk.DomainNum.Cardinality, 1)
	bco := blindPoly(co, pk.DomainNum.Cardinality, 1)

	// note that bcl, bcr, bco re-use memory of cl, cr and co respectively
	cl = nil
	cr = nil
	co = nil

	// using this ensures that our multiExps running in parallel won't use more than
	// provided CPUs
	cpuSemaphore := ecc.NewCPUSemaphore(runtime.NumCPU())

	// derive gamma from the Comm(blinded cl), Comm(blinded cr), Comm(blinded co)
	var err0, err1 error
	chCommit0 := make(chan struct{}, 1)
	chCommit1 := make(chan struct{}, 1)
	go func() {
		proof.LRO[0], err0 = kzg.Commit(bcl, pk.Vk.KZGSRS, cpuSemaphore)
		close(chCommit0)
	}()
	go func() {
		proof.LRO[1], err1 = kzg.Commit(bcr, pk.Vk.KZGSRS, cpuSemaphore)
		close(chCommit1)
	}()
	if proof.LRO[2], err = kzg.Commit(bco, pk.Vk.KZGSRS, cpuSemaphore); err != nil {
		return nil, err
	}
	<-chCommit0
	if err0 != nil {
		return nil, err0
	}

	if err = fs.Bind("gamma", proof.LRO[0].Marshal()); err != nil {
		return nil, err
	}

	<-chCommit1
	if err1 != nil {
		return nil, err1
	}
	if err = fs.Bind("gamma", proof.LRO[1].Marshal()); err != nil {
		return nil, err
	}
	if err = fs.Bind("gamma", proof.LRO[2].Marshal()); err != nil {
		return nil, err
	}
	bgamma, err := fs.ComputeChallenge("gamma")
	if err != nil {
		return nil, err
	}
	var gamma fr.Element
	gamma.SetBytes(bgamma)

	chZ := make(chan struct{}, 1)
	var z polynomial.Polynomial
	// TODO @gbotrel need to check on a many-core machine how this behaves -->
	// we are firing more go routines than CPU for the FFTs, trace looks better
	go func() {
		// compute Z, the permutation accumulator polynomial, in canonical basis
		// ll, lr, lo are NOT blinded
		z = computeZ(ll, lr, lo, pk, gamma)
		pk.DomainNum.FFTInverse(z, fft.DIF, 0)
		fft.BitReverse(z)
		close(chZ)
	}()

	// compute qk in canonical basis, completed with the public inputs
	qkFullC := make(polynomial.Polynomial, sizeDomainNum)
	copy(qkFullC, fullWitness[:spr.NbPublicVariables])
	copy(qkFullC[spr.NbPublicVariables:], pk.LQk[spr.NbPublicVariables:])
	pk.DomainNum.FFTInverse(qkFullC, fft.DIF, 0)
	fft.BitReverse(qkFullC)

	// evaluation of the blinded versions of l, r, o
	// on the odd cosets of (Z/8mZ)/(Z/mZ)
	evalBlindedL := evaluateOddCosetsHDomain(bcl, &pk.DomainH)
	evalBlindedR := evaluateOddCosetsHDomain(bcr, &pk.DomainH)
	evalBlindedO := evaluateOddCosetsHDomain(bco, &pk.DomainH)

	// compute the evaluation of qlL+qrR+qmL.R+qoO+k on the odd cosets of (Z/8mZ)/(Z/mZ)
	// --> uses the blinded version of l, r, o
	var constraintsInd []fr.Element
	chEvalConstraints := make(chan struct{}, 1)
	go func() {
		constraintsInd = evalConstraints(pk, evalBlindedL, evalBlindedR, evalBlindedO, qkFullC)
		close(chEvalConstraints)
	}()

	// blind z
	<-chZ
	bz := blindPoly(z, pk.DomainNum.Cardinality, 2)
	// note that bz shares same memory space as z
	z = nil

	// commit to the blinded version of z
	if proof.Z, err = kzg.Commit(bz, pk.Vk.KZGSRS); err != nil {
		return nil, err
	}

	// evaluate Z on the odd cosets
	evalBlindedZ := evaluateOddCosetsHDomain(bz, &pk.DomainH)

	// compute zu*g1*g2*g3-z*f1*f2*f3 on the odd cosets of (Z/8mZ)/(Z/mZ)
	// evalL, evalO, evalR are the evaluations of the blinded versions of l, r, o.
	var constraintsOrdering polynomial.Polynomial
	chEvalConstraintsOrdering := make(chan struct{}, 1)
	go func() {
		constraintsOrdering = evalConstraintOrdering(pk, evalBlindedZ, evalBlindedL, evalBlindedR, evalBlindedO, gamma)
		close(chEvalConstraintsOrdering)
	}()

	// compute L1*(z-1) on the odd cosets of (Z/8mZ)/(Z/mZ)
	startsAtOne := evalStartsAtOne(pk, evalBlindedZ)

	// derive alpha from the Comm(l), Comm(r), Comm(o), Com(Z)
	if err = fs.Bind("alpha", proof.Z.Marshal()); err != nil {
		return nil, err
	}
	balpha, err := fs.ComputeChallenge("alpha")
	if err != nil {
		return nil, err
	}
	var alpha fr.Element
	alpha.SetBytes(balpha)

	// compute h in canonical form
	<-chEvalConstraints
	<-chEvalConstraintsOrdering
	h1, h2, h3 := computeH(pk, constraintsInd, constraintsOrdering, startsAtOne, alpha)

	// commit to h (3 commitments h1 + x**n*h2 + x**2n*h3)
	chH0 := make(chan struct{}, 1)
	chH1 := make(chan struct{}, 1)
	go func() {
		proof.H[0], err0 = kzg.Commit(h1, pk.Vk.KZGSRS, cpuSemaphore)
		close(chH0)
	}()
	go func() {
		proof.H[1], err1 = kzg.Commit(h2, pk.Vk.KZGSRS, cpuSemaphore)
		close(chH1)
	}()
	if proof.H[2], err = kzg.Commit(h3, pk.Vk.KZGSRS, cpuSemaphore); err != nil {
		return nil, err
	}

	// derive zeta, the point of evaluation
	<-chH0
	if err0 != nil {
		return nil, err0
	}
	if err = fs.Bind("zeta", proof.H[0].Marshal()); err != nil {
		return nil, err
	}
	<-chH1
	if err1 != nil {
		return nil, err1
	}
	if err = fs.Bind("zeta", proof.H[1].Marshal()); err != nil {
		return nil, err
	}
	if err = fs.Bind("zeta", proof.H[2].Marshal()); err != nil {
		return nil, err
	}
	bzeta, err := fs.ComputeChallenge("zeta")
	if err != nil {
		return nil, err
	}
	var zeta fr.Element
	zeta.SetBytes(bzeta)

	// open blinded Z at zeta*z
	var zetaShifted fr.Element
	zetaShifted.Mul(&zeta, &pk.Vk.Generator)
	proof.ZShiftedOpening, err = kzg.Open(
		bz,
		&zetaShifted,
		&pk.DomainH,
		pk.Vk.KZGSRS,
	)
	if err != nil {
		return nil, err
	}

	// blinded z evaluated at u*zeta
	bzuzeta := proof.ZShiftedOpening.ClaimedValue

	// compute evaluations of (blinded version of) l, r, o, z at zeta
	blzeta := bcl.Eval(&zeta)
	brzeta := bcr.Eval(&zeta)
	bozeta := bco.Eval(&zeta)

	// compute the linearization polynomial r at zeta (goal: save committing separately to z, ql, qr, qm, qo, k)
	linearizedPolynomial := computeLinearizedPolynomial(
		blzeta,
		brzeta,
		bozeta,
		alpha,
		gamma,
		zeta,
		bzuzeta,
		bz,
		pk,
	)

	// foldedHDigest = Comm(h1) + zeta**m*Comm(h2) + zeta**2m*Comm(h3)
	var bZetaPowerm big.Int
	sizeBigInt := big.NewInt(sizeDomainNum + 2) // +2 because of the masking (h of degree 3(n+2)-1)
	var zetaPowerm fr.Element
	zetaPowerm.Exp(zeta, sizeBigInt)
	zetaPowerm.ToBigIntRegular(&bZetaPowerm)
	foldedHDigest := proof.H[2]
	foldedHDigest.ScalarMultiplication(&foldedHDigest, &bZetaPowerm)
	foldedHDigest.Add(&foldedHDigest, &proof.H[1])                   // zeta**(m+1)*Comm(h3)
	foldedHDigest.ScalarMultiplication(&foldedHDigest, &bZetaPowerm) // zeta**2(m+1)*Comm(h3) + zeta**(m+1)*Comm(h2)
	foldedHDigest.Add(&foldedHDigest, &proof.H[0])                   // zeta**2(m+1)*Comm(h3) + zeta**(m+1)*Comm(h2) + Comm(h1)

	// foldedH = h1 + zeta*h2 + zeta**2*h3
	foldedH := h3
	utils.Parallelize(len(foldedH), func(start, end int) {
		for i := start; i < end; i++ {
			foldedH[i].Mul(&foldedH[i], &zetaPowerm) // zeta**(m+1)*h3
			foldedH[i].Add(&foldedH[i], &h2[i])      // zeta**(m+1)*h3
			foldedH[i].Mul(&foldedH[i], &zetaPowerm) // zeta**2(m+1)*h3+h2*zeta**(m+1)
			foldedH[i].Add(&foldedH[i], &h1[i])      // zeta**2(m+1)*h3+zeta**(m+1)*h2 + h1
		}
	})

	// TODO this commitment is only necessary to derive the challenge, we should
	// be able to avoid doing it and get the challenge in another way
	linearizedPolynomialDigest, err := kzg.Commit(linearizedPolynomial, pk.Vk.KZGSRS)
	if err != nil {
		return nil, err
	}

	// Batch open the first list of polynomials
	proof.BatchedProof, err = kzg.BatchOpenSinglePoint(
		[]polynomial.Polynomial{
			foldedH,
			linearizedPolynomial,
			bcl,
			bcr,
			bco,
			pk.CS1,
			pk.CS2,
		},
		[]kzg.Digest{
			foldedHDigest,
			linearizedPolynomialDigest,
			proof.LRO[0],
			proof.LRO[1],
			proof.LRO[2],
			pk.Vk.S[0],
			pk.Vk.S[1],
		},
		&zeta,
		&pk.DomainH,
		pk.Vk.KZGSRS,
	)
	if err != nil {
		return nil, err
	}

	return proof, nil

}

// blindPoly blinds a polynomial by adding a Q(X)*(X**degree-1), where deg Q = order.
//
// * cp polynomial in canonical form
// * rou root of unity, meaning the blinding factor is multiple of X**rou-1
// * bo blinding order,  it's the degree of Q, where the blinding is Q(X)*(X**degree-1)
//
// WARNING:
// pre condition degree(cp) <= rou + bo
// pre condition cap(cp) >= int(totalDegree + 1)
func blindPoly(cp polynomial.Polynomial, rou, bo uint64) polynomial.Polynomial {

	// degree of the blinded polynomial is max(rou+order, cp.Degree)
	totalDegree := rou + bo

	// re-use cp
	res := cp[:totalDegree+1]

	// random polynomial
	blindingPoly := make(polynomial.Polynomial, bo+1)
	for i := uint64(0); i < bo+1; i++ {
		blindingPoly[i].SetRandom()
	}

	// blinding
	for i := uint64(0); i < bo+1; i++ {
		res[i].Sub(&res[i], &blindingPoly[i])
		res[rou+i].Add(&res[rou+i], &blindingPoly[i])
	}

	return res
}

// computeLRO extracts the solution l, r, o, and returns it in lagrange form.
// solution = [ public | secret | internal ]
func computeLRO(spr *cs.SparseR1CS, pk *ProvingKey, solution []fr.Element) (polynomial.Polynomial, polynomial.Polynomial, polynomial.Polynomial) {

	s := int(pk.DomainNum.Cardinality)

	var l, r, o polynomial.Polynomial
	l = make([]fr.Element, s)
	r = make([]fr.Element, s)
	o = make([]fr.Element, s)

	for i := 0; i < spr.NbPublicVariables; i++ { // placeholders
		l[i].Set(&solution[i])
		r[i].Set(&solution[0])
		o[i].Set(&solution[0])
	}
	offset := spr.NbPublicVariables
	for i := 0; i < len(spr.Constraints); i++ { // constraints
		l[offset+i].Set(&solution[spr.Constraints[i].L.VariableID()])
		r[offset+i].Set(&solution[spr.Constraints[i].R.VariableID()])
		o[offset+i].Set(&solution[spr.Constraints[i].O.VariableID()])
	}
	offset += len(spr.Constraints)
	for i := 0; i < len(spr.Assertions); i++ { // assertions
		l[offset+i].Set(&solution[spr.Assertions[i].L.VariableID()])
		r[offset+i].Set(&solution[spr.Assertions[i].R.VariableID()])
		o[offset+i].Set(&solution[spr.Assertions[i].O.VariableID()])
	}
	offset += len(spr.Assertions)
	for i := 0; i < s-offset; i++ { // offset to reach 2**n constraints (where the id of l,r,o is 0, so we assign solution[0])
		l[offset+i].Set(&solution[0])
		r[offset+i].Set(&solution[0])
		o[offset+i].Set(&solution[0])
	}

	return l, r, o

}

// computeZ computes Z, in canonical basis, where:
//
// * Z of degree n (domainNum.Cardinality)
// * Z(1)=1
// 								   (l_i+z**i+gamma)*(r_i+u*z**i+gamma)*(o_i+u**2z**i+gamma)
// * for i>0: Z(u**i) = Pi_{k<i} -------------------------------------------------------
//								     (l_i+s1+gamma)*(r_i+s2+gamma)*(o_i+s3+gamma)
//
//	* l, r, o are the solution in Lagrange basis
func computeZ(l, r, o polynomial.Polynomial, pk *ProvingKey, gamma fr.Element) polynomial.Polynomial {

	// note that z has more capacity has its memory is reused for blinded z later on
	z := make(polynomial.Polynomial, pk.DomainNum.Cardinality, pk.DomainNum.Cardinality+3)
	nbElmts := int(pk.DomainNum.Cardinality)
	gInv := make(polynomial.Polynomial, pk.DomainNum.Cardinality)

	var f [3]fr.Element
	var g [3]fr.Element
	var u [3]fr.Element
	u[0].SetOne()
	u[1].Set(&pk.Vk.Shifter[0])
	u[2].Set(&pk.Vk.Shifter[1])

	z[0].SetOne()
	gInv[0].SetOne()

	for i := 0; i < nbElmts-1; i++ {

		f[0].Add(&l[i], &u[0]).Add(&f[0], &gamma) //l_i+z**i+gamma
		f[1].Add(&r[i], &u[1]).Add(&f[1], &gamma) //r_i+u*z**i+gamma
		f[2].Add(&o[i], &u[2]).Add(&f[2], &gamma) //o_i+u**2*z**i+gamma

		g[0].Add(&l[i], &pk.LS1[i]).Add(&g[0], &gamma) //l_i+z**i+gamma
		g[1].Add(&r[i], &pk.LS2[i]).Add(&g[1], &gamma) //r_i+u*z**i+gamma
		g[2].Add(&o[i], &pk.LS3[i]).Add(&g[2], &gamma) //o_i+u**2*z**i+gamma

		f[0].Mul(&f[0], &f[1]).Mul(&f[0], &f[2]) // (l_i+z**i+gamma)*(r_i+u*z**i+gamma)*(o_i+u**2z**i+gamma)
		g[0].Mul(&g[0], &g[1]).Mul(&g[0], &g[2]) //  (l_i+s1+gamma)*(r_i+s2+gamma)*(o_i+s3+gamma)

		gInv[i+1] = g[0]
		z[i+1].Mul(&z[i], &f[0]) //.Div(&z[i+1], &g[0]) --> use montgomery batch inversion in a second loop

		u[0].Mul(&u[0], &pk.DomainNum.Generator) // z**i -> z**i+1
		u[1].Mul(&u[1], &pk.DomainNum.Generator) // u*z**i -> u*z**i+1
		u[2].Mul(&u[2], &pk.DomainNum.Generator) // u**2*z**i -> u**2*z**i+1
	}

	//.Div(&z[i+1], &g[0])
	gInv = fr.BatchInvert(gInv)
	acc := fr.One()
	for i := 1; i < nbElmts; i++ {
		acc.Mul(&acc, &gInv[i])
		z[i].Mul(&z[i], &acc)
	}

	return z

}

// evalConstraints computes the evaluation of lL+qrR+qqmL.R+qoO+k on
// the odd cosets of (Z/8mZ)/(Z/mZ), where m=nbConstraints+nbAssertions.
//
// * evalL, evalR, evalO are the evaluation of the blinded solution vectors on odd cosets
// * qk is the completed version of qk, in canonical version
func evalConstraints(pk *ProvingKey, evalL, evalR, evalO, qk []fr.Element) []fr.Element {

	evalQl := evaluateOddCosetsHDomain(pk.Ql, &pk.DomainH)
	evalQr := evaluateOddCosetsHDomain(pk.Qr, &pk.DomainH)
	evalQm := evaluateOddCosetsHDomain(pk.Qm, &pk.DomainH)
	evalQo := evaluateOddCosetsHDomain(pk.Qo, &pk.DomainH)
	evalQk := evaluateOddCosetsHDomain(qk, &pk.DomainH)

	// computes the evaluation of qrR+qlL+qmL.R+qoO+k on the odd cosets
	// of (Z/8mZ)/(Z/mZ)
	utils.Parallelize(len(evalQk), func(start, end int) {
		var t0, t1 fr.Element
		for i := start; i < end; i++ {
			t1.Mul(&evalQm[i], &evalR[i]) // qm.r
			t1.Add(&t1, &evalQl[i])       // qm.r + ql
			t1.Mul(&t1, &evalL[i])        //  qm.l.r + ql.l

			t0.Mul(&evalQr[i], &evalR[i])
			t0.Add(&t0, &t1) // qm.l.r + ql.l + qr.r

			t1.Mul(&evalQo[i], &evalO[i])
			t0.Add(&t0, &t1)               // ql.l + qr.r + qm.l.r + qo.o
			evalQk[i].Add(&t0, &evalQk[i]) // ql.l + qr.r + qm.l.r + qo.o + k
		}
	})

	return evalQk
}

// evalIDCosets id, uid, u**2id on the odd cosets of (Z/8mZ)/(Z/mZ)
func evalIDCosets(pk *ProvingKey) (id polynomial.Polynomial) {

	id = make([]fr.Element, pk.DomainH.Cardinality)

	var acc fr.Element
	acc.SetOne()

	nn := uint64(64 - bits.TrailingZeros64(pk.DomainH.Cardinality))

	for i := 0; i < int(pk.DomainH.Cardinality); i++ {
		irev := bits.Reverse64(uint64(i)) >> nn
		id[irev].Mul(&acc, &pk.DomainH.FinerGenerator)
		acc.Mul(&acc, &pk.DomainH.Generator)
	}

	return id
}

// evalConstraintOrdering computes the evaluation of Z(uX)g1g2g3-Z(X)f1f2f3 on the odd
// cosets of (Z/8mZ)/(Z/mZ), where m=nbConstraints+nbAssertions.
//
// * evalZ evaluation of the blinded permutation accumulator polynomial on odd cosets
// * evalL, evalR, evalO evaluation of the blinded solution vectors on odd cosets
// * gamma randomization
func evalConstraintOrdering(pk *ProvingKey, evalZ, evalL, evalR, evalO polynomial.Polynomial, gamma fr.Element) polynomial.Polynomial {

	// evaluation of Z(Xu) on the odd cosets of (Z/8mZ)/(Z/mZ)
	evalZu := shiftEval(evalZ, 4)

	// evaluation of z, zu, s1, s2, s3, on the odd cosets of (Z/8mZ)/(Z/mZ)
	evalS1 := evaluateOddCosetsHDomain(pk.CS1, &pk.DomainH)
	evalS2 := evaluateOddCosetsHDomain(pk.CS2, &pk.DomainH)
	evalS3 := evaluateOddCosetsHDomain(pk.CS3, &pk.DomainH)

	// evalutation of ID, u*ID, u**2*ID on the odd cosets of (Z/8mZ)/(Z/mZ)
	evalID := evalIDCosets(pk)

	// computes Z(uX)g1g2g3l-Z(X)f1f2f3l on the odd cosets of (Z/8mZ)/(Z/mZ)
	res := evalZu //make(polynomial.Polynomial, pk.DomainH.Cardinality)
	utils.Parallelize(4*int(pk.DomainNum.Cardinality), func(start, end int) {
		var f [3]fr.Element
		var g [3]fr.Element

		for i := start; i < end; i++ {

			f[0].Add(&evalL[i], &evalID[i]).Add(&f[0], &gamma) //l_i+z**i+gamma
			f[1].Mul(&evalID[i], &pk.Vk.Shifter[0])
			f[2].Mul(&evalID[i], &pk.Vk.Shifter[1])
			f[1].Add(&f[1], &evalR[i]).Add(&f[1], &gamma) //r_i+u*z**i+gamma
			f[2].Add(&f[2], &evalO[i]).Add(&f[2], &gamma) //o_i+u**2*z**i+gamma

			g[0].Add(&evalL[i], &evalS1[i]).Add(&g[0], &gamma) //l_i+s1+gamma
			g[1].Add(&evalR[i], &evalS2[i]).Add(&g[1], &gamma) //r_i+s2+gamma
			g[2].Add(&evalO[i], &evalS3[i]).Add(&g[2], &gamma) //o_i+s3+gamma

			f[0].Mul(&f[0], &f[1]).
				Mul(&f[0], &f[2]).
				Mul(&f[0], &evalZ[i]) // z_i*(l_i+z**i+gamma)*(r_i+u*z**i+gamma)*(o_i+u**2*z**i+gamma)

			g[0].Mul(&g[0], &g[1]).
				Mul(&g[0], &g[2]).
				Mul(&g[0], &evalZu[i]) // u*z_i*(l_i+s1+gamma)*(r_i+s2+gamma)*(o_i+s3+gamma)

			res[i].Sub(&g[0], &f[0])
		}
	})

	return res
}

// evalStartsAtOne computes the evaluation of L1*(z-1) on the odd cosets
// of (Z/8mZ)/(Z/mZ).
//
// evalZ is the evaluation of z (=permutation constraint polynomial) on odd cosets of (Z/8mZ)/(Z/mZ)
func evalStartsAtOne(pk *ProvingKey, evalZ polynomial.Polynomial) polynomial.Polynomial {

	// computes L1 (canonical form)
	lOneLagrange := make(polynomial.Polynomial, pk.DomainNum.Cardinality)
	for i := 0; i < len(lOneLagrange); i++ {
		lOneLagrange[i].Set(&pk.DomainNum.CardinalityInv)
	}

	// evaluates L1 on the odd cosets of (Z/8mZ)/(Z/mZ)
	res := evaluateOddCosetsHDomain(lOneLagrange, &pk.DomainH)

	// // evaluates L1*(z-1) on the odd cosets of (Z/8mZ)/(Z/mZ)
	var buf, one fr.Element
	one.SetOne()
	for i := 0; i < 4*int(pk.DomainNum.Cardinality); i++ {
		buf.Sub(&evalZ[i], &one)
		res[i].Mul(&buf, &res[i])
	}

	return res
}

// evaluateOddCosetsHDomain evaluates poly (canonical form) of degree m<n where n=domainH.Cardinality
// on the odd coset of (Z/2nZ)/(Z/nZ).
//
// Puts the result in res of size n.
// Warning: result is in bit reversed order, we do a bit reverse operation only once in computeH
func evaluateOddCosetsHDomain(poly []fr.Element, domainH *fft.Domain) []fr.Element {
	res := make([]fr.Element, domainH.Cardinality)
	copy(res, poly)
	domainH.FFT(res, fft.DIF, 1)
	// fft.BitReverse(res)
	return res
}

// shiftEval left shifts z by shift
// note that z is bitReversed but we want to shift without bit reverse
func shiftEval(z polynomial.Polynomial, shift int) polynomial.Polynomial {
	s := len(z)

	res := make(polynomial.Polynomial, s)

	nn := uint64(64 - bits.TrailingZeros64(uint64(s)))
	for i := 0; i < len(res); i++ {
		irev := bits.Reverse64(uint64(i)) >> nn
		jrev := bits.Reverse64(uint64((i+shift)%s)) >> nn
		res[irev] = z[jrev]
	}

	return res
}

// computeH computes h in canonical form, split as h1+X^mh2+X^2mh3 such that
//
// qlL+qrR+qmL.R+qoO+k + alpha.(zu*g1*g2*g3*l-z*f1*f2*f3*l) + alpha**2*L1*(z-1)= h.Z
// \------------------/         \------------------------/             \-----/
//    constraintsInd			    constraintOrdering					startsAtOne
//
// constraintInd, constraintOrdering are evaluated on the odd cosets of (Z/8mZ)/(Z/mZ)
func computeH(pk *ProvingKey, constraintsInd, constraintOrdering, startsAtOne polynomial.Polynomial, alpha fr.Element) (polynomial.Polynomial, polynomial.Polynomial, polynomial.Polynomial) {

	h := make(polynomial.Polynomial, pk.DomainH.Cardinality)

	// evaluate Z = X**m-1 on the odd cosets of (Z/8mZ)/(Z/mZ)
	var bExpo big.Int
	bExpo.SetUint64(pk.DomainNum.Cardinality)
	var u [4]fr.Element
	var uu fr.Element
	var one fr.Element
	one.SetOne()
	uu.Set(&pk.DomainH.Generator)
	u[0].Set(&pk.DomainH.FinerGenerator)
	u[1].Mul(&u[0], &uu)
	u[2].Mul(&u[1], &uu)
	u[3].Mul(&u[2], &uu)
	u[0].Exp(u[0], &bExpo).Sub(&u[0], &one).Inverse(&u[0]) // (X**m-1)**-1 at u
	u[1].Exp(u[1], &bExpo).Sub(&u[1], &one).Inverse(&u[1]) // (X**m-1)**-1 at u**3
	u[2].Exp(u[2], &bExpo).Sub(&u[2], &one).Inverse(&u[2]) // (X**m-1)**-1 at u**5
	u[3].Exp(u[3], &bExpo).Sub(&u[3], &one).Inverse(&u[3]) // (X**m-1)**-1 at u**7

	// evaluate qlL+qrR+qmL.R+qoO+k + alpha.(zu*g1*g2*g3*l-z*f1*f2*f3*l) + alpha**2*L1(X)(Z(X)-1)
	// on the odd cosets of (Z/8mZ)/(Z/mZ)
	nn := uint64(64 - bits.TrailingZeros64(pk.DomainH.Cardinality))

	utils.Parallelize(int(pk.DomainH.Cardinality), func(start, end int) {
		for i := uint64(start); i < uint64(end); i++ {
			h[i].Mul(&startsAtOne[i], &alpha).
				Add(&h[i], &constraintOrdering[i]).
				Mul(&h[i], &alpha).
				Add(&h[i], &constraintsInd[i])

			// evaluate qlL+qrR+qmL.R+qoO+k + alpha.(zu*g1*g2*g3*l-z*f1*f2*f3*l)/Z
			// on the odd cosets of (Z/8mZ)/(Z/mZ)
			// note that h is still bit reversed here
			irev := bits.Reverse64(i) >> nn
			h[i].Mul(&h[i], &u[irev%4])
		}
	})

	// put h in canonical form
	// using fft.DIT put h revert bit reverse
	pk.DomainH.FFTInverse(h, fft.DIT, 1)

	// degree of hi is n+2 because of the blinding
	h1 := h[:pk.DomainNum.Cardinality+2]
	h2 := h[pk.DomainNum.Cardinality+2 : 2*(pk.DomainNum.Cardinality+2)]
	h3 := h[2*(pk.DomainNum.Cardinality+2) : 3*(pk.DomainNum.Cardinality+2)]

	return h1, h2, h3

}

// computeLinearizedPolynomial computes the linearized polynomial in canonical basis.
// The purpose is to commit and open all in one ql, qr, qm, qo, qk.
// * a, b, c are the evaluation of l, r, o at zeta
// * z is the permutation polynomial, zu is Z(uX), the shifted version of Z
// * pk is the proving key: the linearized polynomial is a linear combination of ql, qr, qm, qo, qk.
func computeLinearizedPolynomial(l, r, o, alpha, gamma, zeta, zu fr.Element, z polynomial.Polynomial, pk *ProvingKey) polynomial.Polynomial {

	// first part: individual constraints
	var rl fr.Element
	rl.Mul(&r, &l)

	// second part: Z(uzeta)(a+s1+gamma)*(b+s2+gamma)*s3(X)-Z(X)(a+zeta+gamma)*(b+uzeta+gamma)*(c+u**2*zeta+gamma)
	var s2 fr.Element
	s1 := pk.CS1.Eval(&zeta)
	t := pk.CS2.Eval(&zeta)
	s1.Add(&s1, &l).Add(&s1, &gamma) // (a+s1+gamma)
	t.Add(&t, &r).Add(&t, &gamma)    // (b+s2+gamma)
	s1.Mul(&s1, &t).                 // (a+s1+gamma)*(b+s2+gamma)
						Mul(&s1, &zu) // (a+s1+gamma)*(b+s2+gamma)*Z(uzeta)

	s2.Add(&l, &zeta).Add(&s2, &gamma)                          // (a+z+gamma)
	t.Mul(&pk.Vk.Shifter[0], &zeta).Add(&t, &r).Add(&t, &gamma) // (b+uz+gamma)
	s2.Mul(&s2, &t)                                             // (a+z+gamma)*(b+uz+gamma)
	t.Mul(&pk.Vk.Shifter[1], &zeta).Add(&t, &o).Add(&t, &gamma) // (o+u**2z+gamma)
	s2.Mul(&s2, &t)                                             // (a+z+gamma)*(b+uz+gamma)*(c+u**2*z+gamma)
	s2.Neg(&s2)                                                 // -(a+z+gamma)*(b+uz+gamma)*(c+u**2*z+gamma)

	// third part L1(zeta)*alpha**2**Z
	var lagrange, one, den, frNbElmt fr.Element
	one.SetOne()
	nbElmt := int64(pk.DomainNum.Cardinality)
	lagrange.Set(&zeta).
		Exp(lagrange, big.NewInt(nbElmt)).
		Sub(&lagrange, &one)
	frNbElmt.SetUint64(uint64(nbElmt))
	den.Sub(&zeta, &one).
		Mul(&den, &frNbElmt).
		Inverse(&den)
	lagrange.Mul(&lagrange, &den). // L_0 = 1/m*(zeta**n-1)/(zeta-1)
					Mul(&lagrange, &alpha).
					Mul(&lagrange, &alpha) // alpha**2*L_0

	linPol := z.Clone()

	utils.Parallelize(len(linPol), func(start, end int) {
		var t0, t1 fr.Element
		for i := start; i < end; i++ {
			linPol[i].Mul(&linPol[i], &s2) // -Z(X)(a+zeta+gamma)*(b+uzeta+gamma)*(c+u**2*zeta+gamma)
			if i < len(pk.CS3) {
				t0.Mul(&pk.CS3[i], &s1) // (a+s1+gamma)*(b+s2+gamma)*Z(uzeta)*s3(X)
				linPol[i].Add(&linPol[i], &t0)
			}

			linPol[i].Mul(&linPol[i], &alpha) // alpha*( Z(uzeta)*(a+s1+gamma)*(b+s2+gamma)s3(X)-Z(X)(a+zeta+gamma)*(b+uzeta+gamma)*(c+u**2*zeta+gamma) )

			if i < len(pk.Qm) {
				t1.Mul(&pk.Qm[i], &rl) // linPol = lr*Qm
				t0.Mul(&pk.Ql[i], &l)
				t0.Add(&t0, &t1)
				linPol[i].Add(&linPol[i], &t0) // linPol = lr*Qm + l*Ql

				t0.Mul(&pk.Qr[i], &r)
				linPol[i].Add(&linPol[i], &t0) // linPol = lr*Qm + l*Ql + r*Qr

				t0.Mul(&pk.Qo[i], &o).Add(&t0, &pk.CQk[i])
				linPol[i].Add(&linPol[i], &t0) // linPol = lr*Qm + l*Ql + r*Qr + o*Qo + Qk
			}

			t0.Mul(&z[i], &lagrange)
			linPol[i].Add(&linPol[i], &t0) // finish the computation
		}
	})

	return linPol
}
