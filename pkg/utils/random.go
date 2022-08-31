package utils

import (
	"math/rand"
	"sync"
	"time"
)

var seededRand = rand.New(
	rand.NewSource(time.Now().UnixNano()))
var seededRandMu sync.Mutex

const CharsetAlphaLowercase = "abcdefghijklmnopqrstuvwxyz"
const CharsetLowercaseNumeric = "abcdefghijklmnopqrstuvwxyz0123456789"

// RandomString returns a random string of given length using the given charset
func RandomString(length int, charset string) string {
	b := make([]byte, length)
	lc := len(charset)
	for i := range b {
		seededRandMu.Lock()
		r := seededRand.Intn(lc)
		seededRandMu.Unlock()
		b[i] = charset[r]
	}
	return string(b)
}
