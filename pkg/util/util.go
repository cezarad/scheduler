package util

import (
	crand "crypto/rand"
	"fmt"
	"math"
	"math/big"
	"math/rand"
)

func init() {
	r, err := crand.Int(crand.Reader, big.NewInt(math.MaxInt64))
	if err != nil {
		panic(fmt.Errorf("Could not read initialize random bytes"))
	}
	rand.Seed(r.Int64())
}

func RandIntn(n int) int {
	return rand.Intn(n)
}
