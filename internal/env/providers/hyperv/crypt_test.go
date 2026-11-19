package hyperv

import (
	"regexp"
	"testing"
)

func TestSHA512Crypt(t *testing.T) {
	// vectors from glibc / `openssl passwd -6`
	cases := []struct{ pw, salt, want string }{
		{"Hello world!", "saltstring", "$6$saltstring$svn8UoSVapNtMuq1ukKS4tPQd8iKwSMHWjl/O817G3uBnIFNjnQJuesI68u4OTLiBFdcbYEdFCoEOfaS35inz1"},
		{"Agent-1234-Xyz", "abcdefgh", "$6$abcdefgh$zFizaA6ZHi1xElNE7Wv..4716rZ3XlOdLjlk6CNyFA1zjdxe0O9127wBbRufVAKYB/wf6BkcQbadyO72hzLde."},
	}
	for _, c := range cases {
		if got := sha512Crypt(c.pw, c.salt); got != c.want {
			t.Errorf("sha512Crypt(%q,%q)=%s want %s", c.pw, c.salt, got, c.want)
		}
	}
}

func TestGeneratePassword(t *testing.T) {
	re := regexp.MustCompile(`^[A-Z][a-z]{4}-[0-9]{4}-[A-Z][a-z]{3}$`)
	for i := 0; i < 20; i++ {
		if p := GeneratePassword(); !re.MatchString(p) {
			t.Fatalf("bad password %q", p)
		}
	}
}
