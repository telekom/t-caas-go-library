// SPDX-FileCopyrightText: 2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package netutil_test

import (
	"errors"
	"fmt"
	"math/big"
	"net/netip"
	"slices"
	"testing"

	"go4.org/netipx"

	"github.com/telekom/t-caas-go-library/pkg/netutil"
)

func TestAdd(t *testing.T) {
	for _, tc := range []struct{ input, offset, want string }{
		{"0.0.0.0", "4294967295", "255.255.255.255"},
		{"255.255.255.255", "-4294967295", "0.0.0.0"},
		{"192.0.2.255", "1", "192.0.3.0"},
		{"2001:db8::", "18446744073709551616", "2001:db8:0:1::"},
		{"::", "340282366920938463463374607431768211455", "ffff:ffff:ffff:ffff:ffff:ffff:ffff:ffff"},
		{"::1", "-1", "::"},
		{"::ffff:192.0.2.1", "1", "::ffff:192.0.2.2"},
		{"::fffe:ffff:ffff", "1", "::ffff:0.0.0.0"},
		{"192.0.2.1", "0", "192.0.2.1"},
	} {
		offset, _ := new(big.Int).SetString(tc.offset, 10)
		got, err := netutil.Add(netip.MustParseAddr(tc.input), offset)
		if err != nil || got != netip.MustParseAddr(tc.want) || offset.String() != tc.offset {
			t.Fatalf("Add(%s, %s) = %s, %v", tc.input, tc.offset, got, err)
		}
	}
	for _, tc := range []struct {
		input  netip.Addr
		offset *big.Int
		err    error
	}{
		{netip.Addr{}, big.NewInt(1), netutil.ErrInvalidAddress},
		{netip.MustParseAddr("fe80::1%eth0"), big.NewInt(1), netutil.ErrInvalidAddress},
		{netip.MustParseAddr("::"), nil, netutil.ErrOutOfRange},
		{netip.MustParseAddr("::"), big.NewInt(-1), netutil.ErrOutOfRange},
		{netip.MustParseAddr("0.0.0.0"), big.NewInt(-1), netutil.ErrOutOfRange},
		{netip.MustParseAddr("255.255.255.255"), big.NewInt(1), netutil.ErrOutOfRange},
		{netip.MustParseAddr("ffff:ffff:ffff:ffff:ffff:ffff:ffff:ffff"), big.NewInt(1), netutil.ErrOutOfRange},
		{netip.MustParseAddr("::"), new(big.Int).Lsh(big.NewInt(1), 128), netutil.ErrOutOfRange},
	} {
		if got, err := netutil.Add(tc.input, tc.offset); got.IsValid() || !errors.Is(err, tc.err) {
			t.Errorf("Add(%s, %v) = %s, %v", tc.input, tc.offset, got, err)
		}
	}
}

func TestPrefixConventions(t *testing.T) {
	for _, tc := range []struct{ input, broadcast, usable string }{
		{"192.0.2.129/24", "192.0.2.255", "192.0.2.1"},
		{"0.0.0.0/0", "255.255.255.255", "0.0.0.1"},
		{"192.0.2.3/31", "192.0.2.3", "192.0.2.2"},
		{"192.0.2.1/32", "192.0.2.1", ""},
		{"2001:db8::1234/64", "", "2001:db8::1"},
		{"::/0", "", "::1"},
		{"2001:db8::3/127", "", "2001:db8::2"},
		{"2001:db8::1/128", "", ""},
		{"::ffff:192.0.2.1/120", "", "::ffff:192.0.2.1"},
	} {
		p := netip.MustParsePrefix(tc.input)
		broadcast, err := netutil.Broadcast(p)
		if tc.broadcast == "" {
			if broadcast.IsValid() || !errors.Is(err, netutil.ErrFamilyMismatch) {
				t.Errorf("IPv6 broadcast accepted: %s, %v", broadcast, err)
			}
		} else if err != nil || broadcast != netip.MustParseAddr(tc.broadcast) {
			t.Errorf("Broadcast(%s) = %s, %v", p, broadcast, err)
		}
		usable, err := netutil.FirstUsable(p)
		if tc.usable == "" {
			if usable.IsValid() || !errors.Is(err, netutil.ErrOutOfRange) {
				t.Errorf("single-host prefix accepted: %s, %v", usable, err)
			}
		} else if err != nil || usable != netip.MustParseAddr(tc.usable) {
			t.Errorf("FirstUsable(%s) = %s, %v", p, usable, err)
		}
	}
	for _, fn := range []func(netip.Prefix) (netip.Addr, error){netutil.Broadcast, netutil.FirstUsable} {
		if got, err := fn(netip.Prefix{}); got.IsValid() || !errors.Is(err, netutil.ErrInvalidPrefix) {
			t.Errorf("invalid prefix accepted: %s, %v", got, err)
		}
	}
}

func TestSubdivide(t *testing.T) {
	for _, tc := range []struct {
		input string
		bits  int
		limit int
		want  []string
		err   error
	}{
		{"192.0.2.129/24", 25, 2, []string{"192.0.2.0/25", "192.0.2.128/25"}, nil},
		{"2001:db8::/126", 127, 2, []string{"2001:db8::/127", "2001:db8::2/127"}, nil},
		{"255.255.255.254/31", 32, 2, []string{"255.255.255.254/32", "255.255.255.255/32"}, nil},
		{"ffff:ffff:ffff:ffff:ffff:ffff:ffff:fffe/127", 128, 2,
			[]string{"ffff:ffff:ffff:ffff:ffff:ffff:ffff:fffe/128", "ffff:ffff:ffff:ffff:ffff:ffff:ffff:ffff/128"}, nil},
		{"192.0.2.1/32", 32, 1, []string{"192.0.2.1/32"}, nil},
		{"::ffff:192.0.2.0/127", 128, 2, []string{"::ffff:192.0.2.0/128", "::ffff:192.0.2.1/128"}, nil},
		{"", 24, 1, nil, netutil.ErrInvalidPrefix},
		{"192.0.2.0/24", 23, 2, nil, netutil.ErrInvalidPrefix},
		{"192.0.2.0/24", 33, 2, nil, netutil.ErrInvalidPrefix},
		{"::/0", -1, 2, nil, netutil.ErrInvalidPrefix},
		{"::/0", 129, 2, nil, netutil.ErrInvalidPrefix},
		{"::/0", 128, 1024, nil, netutil.ErrLimit},
		{"::/0", 64, int(^uint(0) >> 1), nil, netutil.ErrLimit},
		{"192.0.2.0/24", 25, 1, nil, netutil.ErrLimit},
		{"192.0.2.0/24", 24, 0, nil, netutil.ErrLimit},
		{"192.0.2.0/24", 24, -1, nil, netutil.ErrLimit},
	} {
		var p netip.Prefix
		if tc.input != "" {
			p = netip.MustParsePrefix(tc.input)
		}
		got, err := netutil.Subdivide(p, tc.bits, tc.limit)
		strings := make([]string, len(got))
		for i, sub := range got {
			strings[i] = sub.String()
		}
		if !slices.Equal(strings, tc.want) || !errors.Is(err, tc.err) {
			t.Errorf("Subdivide(%s,%d,%d) = %v, %v", p, tc.bits, tc.limit, got, err)
		}
	}
}

func ExampleAdd() {
	a, err := netutil.Add(netip.MustParseAddr("2001:db8::"), new(big.Int).Lsh(big.NewInt(1), 64))
	if err != nil {
		panic(err)
	}
	fmt.Println(a)
	// Output: 2001:db8:0:1::
}

func ExampleSubdivide() {
	subnets, err := netutil.Subdivide(netip.MustParsePrefix("2001:db8::/126"), 127, 2)
	if err != nil {
		panic(err)
	}
	for _, subnet := range subnets {
		fmt.Println(subnet)
	}
	// Output:
	// 2001:db8::/127
	// 2001:db8::2/127
}

func FuzzArithmetic(f *testing.F) {
	f.Add([]byte{192, 0, 2, 1}, int64(255))
	f.Add(make([]byte, 16), int64(-1))
	f.Add([]byte{255, 255, 255, 255}, int64(1))
	f.Fuzz(func(t *testing.T, bytes []byte, delta int64) {
		a, ok := netip.AddrFromSlice(bytes)
		if !ok {
			return
		}
		offset := big.NewInt(delta)
		got, err := netutil.Add(a, offset)
		value := new(big.Int).Add(new(big.Int).SetBytes(bytes), offset)
		if value.Sign() < 0 || value.BitLen() > a.BitLen() {
			if !errors.Is(err, netutil.ErrOutOfRange) || got.IsValid() {
				t.Fatalf("overflow accepted: %s, %v", got, err)
			}
			return
		}
		if err != nil || got.BitLen() != a.BitLen() || new(big.Int).SetBytes(got.AsSlice()).Cmp(value) != 0 {
			t.Fatalf("incorrect addition: %s, %v", got, err)
		}
		reverse, err := netutil.Add(got, new(big.Int).Neg(offset))
		if err != nil || reverse != a || offset.Int64() != delta {
			t.Fatalf("round trip failed: %s, %v", reverse, err)
		}
	})
}

func FuzzSubdivide(f *testing.F) {
	f.Add([]byte{192, 0, 2, 129}, uint8(24), uint8(2))
	f.Add(make([]byte, 16), uint8(120), uint8(8))
	f.Fuzz(func(t *testing.T, bytes []byte, rawBits, rawExtra uint8) {
		a, ok := netip.AddrFromSlice(bytes)
		if !ok {
			return
		}
		bits := int(rawBits) % (a.BitLen() + 1)
		extra := min(int(rawExtra)%9, a.BitLen()-bits)
		p := netip.PrefixFrom(a, bits).Masked()
		subnets, err := netutil.Subdivide(p, bits+extra, 256)
		if err != nil || len(subnets) != 1<<extra {
			t.Fatalf("incorrect count: %d, %v", len(subnets), err)
		}
		for i, sub := range subnets {
			start, end := sub.Addr(), netipx.PrefixLastIP(sub)
			if sub.Bits() != bits+extra || sub != sub.Masked() ||
				!p.Contains(start) || !p.Contains(end) || (i == 0 && start != p.Addr()) {
				t.Fatalf("invalid subdivision %s", sub)
			}
			if i > 0 && netipx.PrefixLastIP(subnets[i-1]).Next() != start {
				t.Fatalf("gap or overlap at %d", i)
			}
			if i == len(subnets)-1 && end != netipx.PrefixLastIP(p) {
				t.Fatal("last endpoint not preserved")
			}
		}
		if _, err := netutil.Subdivide(p, bits+extra, len(subnets)-1); !errors.Is(err, netutil.ErrLimit) {
			t.Fatalf("limit not enforced: %v", err)
		}
	})
}
