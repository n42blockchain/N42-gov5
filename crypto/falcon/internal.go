// Copyright 2026 The N42 Authors. LGPL-3.0-or-later.
package falcon

// Recover h=g/f in Z_q[x]/(x^512+1) for crypto.Signer.Public after importing
// the standard 1281-byte secret key. A direct transform is intentionally used:
// key import is infrequent; correctness matters more than a bespoke fast NTT.
// Signing/verification and key generation remain in the independent library.
func modpow(a, n int64) int64 {
	r := int64(1)
	for n > 0 {
		if n&1 != 0 {
			r = r * a % Q
		}
		a = a * a % Q
		n >>= 1
	}
	return r
}
func publicFromPrivate(raw []byte) (*PublicKey, error) {
	var polys [2][N]int64
	for k := 0; k < 2; k++ {
		var acc uint32
		bits := 0
		pos := 1 + k*384
		for j := 0; j < N; j++ {
			for bits < 6 {
				acc = (acc << 8) | uint32(raw[pos])
				pos++
				bits += 8
			}
			bits -= 6
			v := int64((acc >> bits) & 63)
			if v >= 32 {
				v -= 64
			}
			if v == -32 {
				return nil, ErrInvalidPrivateKey
			}
			polys[k][j] = (v + Q) % Q
		}
	}
	for _, b := range raw[769:] {
		if b == 128 {
			return nil, ErrInvalidPrivateKey
		}
	}
	root := int64(0)
	for x := int64(2); x < Q; x++ {
		r := modpow(x, 12)
		if modpow(r, N) == Q-1 {
			root = r
			break
		}
	}
	if root == 0 {
		return nil, ErrInvalidPrivateKey
	}
	var values [N]int64
	var points [N]int64
	for i := 0; i < N; i++ {
		x := modpow(root, int64(2*i+1))
		points[i] = x
		f, g, p := int64(0), int64(0), int64(1)
		for j := 0; j < N; j++ {
			f = (f + polys[0][j]*p) % Q
			g = (g + polys[1][j]*p) % Q
			p = p * x % Q
		}
		if f == 0 {
			return nil, ErrInvalidPrivateKey
		}
		values[i] = g * modpow(f, Q-2) % Q
	}
	var coeff [N]int64
	invN := modpow(N, Q-2)
	for i := 0; i < N; i++ {
		power := int64(1)
		invX := modpow(points[i], Q-2)
		for j := 0; j < N; j++ {
			coeff[j] = (coeff[j] + values[i]*power) % Q
			power = power * invX % Q
		}
	}
	// Validate the integer NTRU relation before allowing an imported secret
	// into the signer. Modular invertibility alone does not make a valid key.
	var bigF, bigG [N]int64
	for j := 0; j < N; j++ {
		bigF[j] = int64(int8(raw[769+j]))
	}
	for i := 0; i < N; i++ {
		sum := int64(0)
		for j := 0; j < N; j++ {
			k := i - j
			sign := int64(1)
			if k < 0 {
				k += N
				sign = -1
			}
			sum = (sum + sign*(coeff[j]*invN%Q)*bigF[k]) % Q
		}
		sum = (sum + Q) % Q
		if sum > Q/2 {
			sum -= Q
		}
		if sum < -127 || sum > 127 {
			return nil, ErrInvalidPrivateKey
		}
		bigG[i] = sum
	}
	for i := 0; i < N; i++ {
		sum := int64(0)
		for j := 0; j < N; j++ {
			k := i - j
			sign := int64(1)
			if k < 0 {
				k += N
				sign = -1
			}
			f, g := polys[0][j], polys[1][j]
			if f > Q/2 {
				f -= Q
			}
			if g > Q/2 {
				g -= Q
			}
			sum += sign * (f*bigG[k] - g*bigF[k])
		}
		want := int64(0)
		if i == 0 {
			want = Q
		}
		if sum != want {
			return nil, ErrInvalidPrivateKey
		}
	}
	p := &PublicKey{}
	p.packed[0] = 9
	var acc uint32
	bits, pos := 0, 1
	for j := 0; j < N; j++ {
		acc = (acc << 14) | uint32(coeff[j]*invN%Q)
		bits += 14
		for bits >= 8 {
			bits -= 8
			p.packed[pos] = byte(acc >> bits)
			pos++
		}
	}
	return p, nil
}
