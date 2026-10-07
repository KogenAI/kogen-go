package intentapprove

import (
	"errors"
	"regexp"
	"strings"
)

var digestPrefix = regexp.MustCompile(`^[0-9a-f]{6,64}$`)

var errUnmappedHashSymbol = errors.New("intentapprove: hash symbol has no byte-derived mapping")

// intentClaim translates the historical intent-slice vocabulary into a real
// command argument. Match/mismatch is encoded by the symbol itself, never by
// prefixOk. Unrecognized values are refused instead of guessed.
func intentClaim(actualDigest, symbol string) (string, bool, error) {
	if !digestPrefix.MatchString(actualDigest) || len(actualDigest) != 64 {
		return "", false, errors.New("intentapprove: invalid computed approval digest")
	}
	if symbol == "abcd1234" || symbol == "bbbb2222" || symbol == "cccc3333" {
		return actualDigest[:len(symbol)], true, nil
	}
	if symbol == "ffff0000" || symbol == "deadbeef" {
		return flippedPrefix(actualDigest, len(symbol)), false, nil
	}
	if digestPrefix.MatchString(symbol) && strings.HasPrefix(actualDigest, symbol) {
		return symbol, true, nil
	}
	return "", false, errUnmappedHashSymbol
}

// approvalClaim resolves the approve slice's given and sha symbols using the
// event's literal prefix relation. The resulting command argument is then
// checked again by approval.Prepare against bytes read from the rooted
// checkout. The redundant prefixOk field is not consulted.
func approvalClaim(actualDigest, given, shaSymbol string) (string, bool, error) {
	if given == "" {
		return "", false, nil
	}
	if len(actualDigest) != 64 || !digestPrefix.MatchString(actualDigest) {
		return "", false, errors.New("intentapprove: invalid computed approval digest")
	}
	if digestPrefix.MatchString(given) && strings.HasPrefix(shaSymbol, given) {
		return actualDigest[:len(given)], true, nil
	}
	length := len(given)
	if length < 6 {
		length = 8
	}
	if length > 64 {
		length = 64
	}
	return flippedPrefix(actualDigest, length), false, nil
}

func flippedPrefix(digest string, length int) string {
	if length < 1 {
		length = 1
	}
	if length > len(digest) {
		length = len(digest)
	}
	first := byte('0')
	if digest[0] == '0' {
		first = '1'
	}
	return string(first) + digest[1:length]
}
