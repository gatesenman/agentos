package hyperv

import (
	"crypto/rand"
	"crypto/sha512"
	"math/big"
	"strings"
)

// sha512Crypt implements glibc's SHA-512 crypt ("$6$salt$hash", 5000 rounds),
// used for the Ubuntu autoinstall identity and the Fedora kickstart user.
func sha512Crypt(password, salt string) string {
	if len(salt) > 16 {
		salt = salt[:16]
	}
	p, s := []byte(password), []byte(salt)
	const rounds = 5000

	b := sha512.New()
	b.Write(p)
	b.Write(s)
	b.Write(p)
	bsum := b.Sum(nil)

	a := sha512.New()
	a.Write(p)
	a.Write(s)
	for i := len(p); i > 0; i -= 64 {
		if i > 64 {
			a.Write(bsum)
		} else {
			a.Write(bsum[:i])
		}
	}
	for i := len(p); i > 0; i >>= 1 {
		if i&1 != 0 {
			a.Write(bsum)
		} else {
			a.Write(p)
		}
	}
	asum := a.Sum(nil)

	dp := sha512.New()
	for i := 0; i < len(p); i++ {
		dp.Write(p)
	}
	dpsum := dp.Sum(nil)
	pseq := make([]byte, 0, len(p))
	for i := len(p); i > 0; i -= 64 {
		if i > 64 {
			pseq = append(pseq, dpsum...)
		} else {
			pseq = append(pseq, dpsum[:i]...)
		}
	}

	ds := sha512.New()
	for i := 0; i < 16+int(asum[0]); i++ {
		ds.Write(s)
	}
	dssum := ds.Sum(nil)
	sseq := make([]byte, 0, len(s))
	for i := len(s); i > 0; i -= 64 {
		if i > 64 {
			sseq = append(sseq, dssum...)
		} else {
			sseq = append(sseq, dssum[:i]...)
		}
	}

	c := asum
	for i := 0; i < rounds; i++ {
		h := sha512.New()
		if i&1 != 0 {
			h.Write(pseq)
		} else {
			h.Write(c)
		}
		if i%3 != 0 {
			h.Write(sseq)
		}
		if i%7 != 0 {
			h.Write(pseq)
		}
		if i&1 != 0 {
			h.Write(c)
		} else {
			h.Write(pseq)
		}
		c = h.Sum(nil)
	}

	const itoa = "./0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	var out strings.Builder
	enc := func(b2, b1, b0 byte, n int) {
		w := uint(b2)<<16 | uint(b1)<<8 | uint(b0)
		for ; n > 0; n-- {
			out.WriteByte(itoa[w&0x3f])
			w >>= 6
		}
	}
	order := [][3]int{{0, 21, 42}, {22, 43, 1}, {44, 2, 23}, {3, 24, 45}, {25, 46, 4}, {47, 5, 26}, {6, 27, 48}, {28, 49, 7}, {50, 8, 29}, {9, 30, 51}, {31, 52, 10}, {53, 11, 32}, {12, 33, 54}, {34, 55, 13}, {56, 14, 35}, {15, 36, 57}, {37, 58, 16}, {59, 17, 38}, {18, 39, 60}, {40, 61, 19}, {62, 20, 41}}
	for _, o := range order {
		enc(c[o[0]], c[o[1]], c[o[2]], 4)
	}
	enc(0, 0, c[63], 2)
	return "$6$" + salt + "$" + out.String()
}

// randomString returns n chars from alphabet using crypto/rand.
func randomString(n int, alphabet string) string {
	var b strings.Builder
	max := big.NewInt(int64(len(alphabet)))
	for i := 0; i < n; i++ {
		v, err := rand.Int(rand.Reader, max)
		if err != nil {
			panic(err)
		}
		b.WriteByte(alphabet[v.Int64()])
	}
	return b.String()
}

// GeneratePassword returns a readable password that satisfies Windows
// complexity rules (upper, lower, digit, symbol) and avoids look-alikes.
func GeneratePassword() string {
	const lower = "abcdefghjkmnpqrstuvwxyz"
	const upper = "ABCDEFGHJKLMNPQRSTUVWXYZ"
	const digit = "23456789"
	return randomString(1, upper) + randomString(4, lower) + "-" + randomString(4, digit) + "-" + randomString(1, upper) + randomString(3, lower)
}
