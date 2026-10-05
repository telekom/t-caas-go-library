// SPDX-FileCopyrightText: 2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

// Package netutil provides overflow-checked IP arithmetic, bounded prefix
// subdivision, and first-usable-address conventions. Addresses with zones are
// rejected. Numeric operations preserve address families: mapped IPv4 addresses
// are IPv6 unless explicitly converted with netip.Addr.Unmap. Prefixes with host bits
// are masked before use. No operation performs I/O.
package netutil

import (
	"errors"
	"math/big"
	"net/netip"

	"go4.org/netipx"
)

var (
	// ErrInvalidAddress indicates an invalid address or an unsupported zone.
	ErrInvalidAddress = errors.New("invalid or zoned IP address")
	// ErrInvalidPrefix indicates an invalid prefix or subdivision prefix length.
	ErrInvalidPrefix = errors.New("invalid IP prefix")
	// ErrFamilyMismatch indicates incompatible IPv4 and IPv6 address families.
	ErrFamilyMismatch = errors.New("IP address families differ")
	// ErrOutOfRange indicates arithmetic overflow or no usable host.
	ErrOutOfRange = errors.New("value outside address range")
	// ErrLimit indicates an exceeded or nonpositive subdivision limit.
	ErrLimit = errors.New("subdivision limit exceeded or not positive")
)

func validate(addr netip.Addr) error {
	if !addr.IsValid() || addr.Zone() != "" {
		return ErrInvalidAddress
	}
	return nil
}

func number(addr netip.Addr) *big.Int {
	return new(big.Int).SetBytes(addr.AsSlice())
}

// Add adds a signed arbitrary-precision offset without wrapping or saturation.
// Nil offsets, underflow, and overflow return ErrOutOfRange. offset is not mutated.
func Add(addr netip.Addr, offset *big.Int) (netip.Addr, error) {
	if err := validate(addr); err != nil {
		return netip.Addr{}, err
	}
	if offset == nil {
		return netip.Addr{}, ErrOutOfRange
	}
	result := new(big.Int).Add(number(addr), offset)
	if result.Sign() < 0 || result.BitLen() > addr.BitLen() {
		return netip.Addr{}, ErrOutOfRange
	}
	if addr.Is4() {
		var bytes [4]byte
		result.FillBytes(bytes[:])
		return netip.AddrFrom4(bytes), nil
	}
	var bytes [16]byte
	result.FillBytes(bytes[:])
	return netip.AddrFrom16(bytes), nil
}

func canonical(prefix netip.Prefix) (netip.Prefix, error) {
	if !prefix.IsValid() {
		return netip.Prefix{}, ErrInvalidPrefix
	}
	return prefix.Masked(), nil
}

// Broadcast returns the last address of an IPv4 prefix, including /31 and /32.
// It is a numeric endpoint, not a claim that the address is reserved. IPv6
// (including mapped IPv4 prefixes) returns ErrFamilyMismatch.
func Broadcast(prefix netip.Prefix) (netip.Addr, error) {
	prefix, err := canonical(prefix)
	if err != nil {
		return netip.Addr{}, err
	}
	if !prefix.Addr().Is4() {
		return netip.Addr{}, ErrFamilyMismatch
	}
	return netipx.PrefixLastIP(prefix), nil
}

// FirstUsable returns network+1 for ordinary prefixes and the network address
// for point-to-point /31 and /127 prefixes. Single-host /32 and /128 prefixes
// return ErrOutOfRange. This explicit convention is not a gateway assignment.
func FirstUsable(prefix netip.Prefix) (netip.Addr, error) {
	prefix, err := canonical(prefix)
	if err != nil {
		return netip.Addr{}, err
	}
	first := prefix.Addr()
	hostBits := first.BitLen() - prefix.Bits()
	if hostBits == 0 {
		return netip.Addr{}, ErrOutOfRange
	}
	if hostBits == 1 {
		return first, nil
	}
	return first.Next(), nil
}

// Subdivide partitions prefix into ordered, disjoint prefixes with bits bits.
// A positive limit is mandatory. Counts are checked before allocation, including
// IPv6 splits whose counts do not fit machine integers. The caller owns the
// memory budget: choose a small limit for untrusted input.
func Subdivide(prefix netip.Prefix, bits, limit int) ([]netip.Prefix, error) {
	prefix, err := canonical(prefix)
	if err != nil {
		return nil, err
	}
	if bits < prefix.Bits() || bits > prefix.Addr().BitLen() {
		return nil, ErrInvalidPrefix
	}
	if limit <= 0 {
		return nil, ErrLimit
	}
	count := new(big.Int).Lsh(big.NewInt(1), uint(bits-prefix.Bits()))
	if count.Cmp(big.NewInt(int64(limit))) > 0 {
		return nil, ErrLimit
	}
	result := make([]netip.Prefix, int(count.Int64()))
	addr := prefix.Addr()
	for i := range result {
		result[i] = netip.PrefixFrom(addr, bits)
		addr = netipx.PrefixLastIP(result[i]).Next()
	}
	return result, nil
}
