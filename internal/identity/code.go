package identity

import (
	"bytes"
	"encoding/binary"
	"strconv"
	"strings"

	"golang.org/x/crypto/blake2s"
)

// codeDomain separates this hash from every other use of BLAKE2s in dcc, so
// that a digest computed elsewhere can never be passed off as a Security
// Code. It is versioned with the protocol: changing how the code is derived
// means peers who verified under the old scheme must verify again.
const codeDomain = "dcc-security-code-1"

// The code is eight groups of five digits — forty in all, comfortably beyond
// what an attacker can grind a second Identity to match, and still short
// enough to read down a phone line.
const (
	codeGroups     = 8
	codeGroupWidth = 5
	// codeGroupMod is 10^codeGroupWidth: each group is four digest bytes
	// reduced into five digits. The reduction is very slightly biased —
	// 2^32 is not a multiple of 100000 — which costs a negligible fraction
	// of a bit per group and nothing an attacker can steer.
	codeGroupMod = 100000
)

// SecurityCode is the forty-digit code two participants compare out-of-band
// to satisfy themselves that no one sits between them. It is derived from
// both Identity public keys and nothing else, so it is the same in every
// Session between the same two people, and different the moment either side's
// Identity changes.
type SecurityCode string

// Groups splits the code into the eight five-digit blocks it is meant to be
// read in. UIs lay the blocks out; they never chunk the string themselves.
func (c SecurityCode) Groups() []string {
	groups := make([]string, 0, codeGroups)
	for i := 0; i+codeGroupWidth <= len(c); i += codeGroupWidth {
		groups = append(groups, string(c[i:i+codeGroupWidth]))
	}
	return groups
}

// SecurityCodeFor derives the code for a pair of Identities. The two keys are
// sorted before hashing, so both sides compute the same code without needing
// to agree on who hosted or who asked first.
func SecurityCodeFor(a, b PublicKey) SecurityCode {
	low, high := a, b
	if bytes.Compare(low[:], high[:]) > 0 {
		low, high = high, low
	}

	h, err := blake2s.New256(nil)
	if err != nil {
		// New256 only fails on an invalid key, and there is no key here.
		panic("identity: blake2s.New256: " + err.Error())
	}
	h.Write([]byte(codeDomain))
	h.Write(low[:])
	h.Write(high[:])
	digest := h.Sum(nil)

	var code strings.Builder
	code.Grow(codeGroups * codeGroupWidth)
	for i := range codeGroups {
		chunk := binary.BigEndian.Uint32(digest[i*4:])
		group := strconv.FormatUint(uint64(chunk%codeGroupMod), 10)
		for pad := codeGroupWidth - len(group); pad > 0; pad-- {
			code.WriteByte('0')
		}
		code.WriteString(group)
	}
	return SecurityCode(code.String())
}
