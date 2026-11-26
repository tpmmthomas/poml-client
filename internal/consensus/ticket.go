// Package consensus contains pure checks that must produce the same answer on
// every node.
package consensus

import (
	"errors"
	"math/big"

	"github.com/thomas/poml-node/internal/types"
)

func Threshold(hashBits uint32, baseTarget *big.Int, weight uint64) (*big.Int, error) {
	if hashBits == 0 || baseTarget == nil || baseTarget.Sign() < 0 {
		return nil, errors.New("invalid ticket parameters")
	}
	if weight == 0 {
		return big.NewInt(0), nil
	}
	n := new(big.Int).Lsh(big.NewInt(1), uint(hashBits))
	if baseTarget.Cmp(n) > 0 {
		return nil, errors.New("base target exceeds hash space")
	}
	base := new(big.Int).Sub(n, baseTarget)
	numerator := new(big.Int).Exp(base, new(big.Int).SetUint64(weight), nil)
	denominator := new(big.Int).Exp(n, new(big.Int).SetUint64(weight-1), nil)
	numerator.Add(numerator, new(big.Int).Sub(denominator, big.NewInt(1)))
	quotient := new(big.Int).Quo(numerator, denominator)
	return new(big.Int).Sub(n, quotient), nil
}

func TicketWins(ticket types.Hash, threshold *big.Int) bool {
	if threshold == nil || threshold.Sign() <= 0 {
		return false
	}
	return new(big.Int).SetBytes(ticket[:]).Cmp(threshold) < 0
}
