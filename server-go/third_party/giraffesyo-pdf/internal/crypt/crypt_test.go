package crypt

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5" //nolint:gosec // exercising the spec's mandated primitive
	"io"
	"testing"
)

var testID = []byte("0123456789abcdef")

// --- RC4 / AES-128 (revisions 2-4) encryption side, mirroring the spec
// algorithms so the decryptor can be round-tripped. ---

func buildRC4(r, keyBits int, v int, aesV2 bool) Config {
	return buildRC4Pw(r, keyBits, v, aesV2, nil)
}

// buildRC4Pw builds an /Encrypt config whose user password is userPw
// (nil = empty) and whose owner password is empty.
func buildRC4Pw(r, keyBits, v int, aesV2 bool, userPw []byte) Config {
	keyLen := 5
	if r >= 3 {
		keyLen = keyBits / 8
	}
	c := Config{Filter: "Standard", V: v, R: r, Length: keyBits, P: -44, EncryptMeta: true, ID: testID}

	// /O: Algorithm 3 with the empty owner password encrypting the padded
	// user password.
	ownerKey := hashN(padPassword(nil), 16)
	if r >= 3 {
		for range 50 {
			ownerKey = hashN(ownerKey, 16)
		}
	}
	ownerKey = ownerKey[:keyLen]
	o := padPassword(userPw)
	if r == 2 {
		o = rc4Crypt(ownerKey, o)
	} else {
		for i := range 20 {
			o = rc4Crypt(xorKey(ownerKey, byte(i)), o)
		}
	}
	c.O = o

	// File key (Algorithm 2) uses the user password.
	fileKey := fileKeyRC4(padPassword(userPw), c, keyLen)

	// /U (Algorithm 4/5).
	if r == 2 {
		c.U = rc4Crypt(fileKey, passwordPad)
	} else {
		h := md5.New() //nolint:gosec // Algorithm 5
		h.Write(passwordPad)
		h.Write(c.ID)
		digest := rc4Crypt(fileKey, h.Sum(nil))
		for i := 1; i <= 19; i++ {
			digest = rc4Crypt(xorKey(fileKey, byte(i)), digest)
		}
		u := make([]byte, 32)
		copy(u, digest[:16])
		c.U = u
	}

	if v == 4 {
		cfm := "V2"
		if aesV2 {
			cfm = "AESV2"
		}
		c.StmF, c.StrF = "StdCF", "StdCF"
		c.CF = map[string]Filter{"StdCF": {CFM: cfm, Length: keyLen}}
	}
	return c
}

// encryptData mirrors DecryptStreamData's inverse for RC4/AESV2/AESV3.
func encryptData(num, gen int, m Method, fileKey, data []byte) []byte {
	switch m {
	case RC4:
		return rc4Crypt(objKey(fileKey, num, gen, false), data)
	case AESV2:
		return aesEncrypt(objKey(fileKey, num, gen, true), data)
	case AESV3:
		return aesEncrypt(fileKey, data)
	default:
		return data
	}
}

// aesEncrypt is the inverse of aesDecrypt: PKCS#7 pad, prepend a fixed IV,
// AES-CBC encrypt.
func aesEncrypt(key, data []byte) []byte {
	block, err := aes.NewCipher(key)
	if err != nil {
		panic(err)
	}
	iv := bytes.Repeat([]byte{0x42}, aes.BlockSize)
	pad := aes.BlockSize - len(data)%aes.BlockSize
	padded := append(append([]byte{}, data...), bytes.Repeat([]byte{byte(pad)}, pad)...)
	ct := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(ct, padded)
	return append(append([]byte{}, iv...), ct...)
}

func TestRC4RoundTrip(t *testing.T) {
	cases := []struct {
		name       string
		r, bits, v int
		aes        bool
		method     Method
	}{
		{"R2-40", 2, 40, 1, false, RC4},
		{"R3-128", 3, 128, 2, false, RC4},
		{"R4-RC4", 4, 128, 4, false, RC4},
		{"R4-AESV2", 4, 128, 4, true, AESV2},
	}
	plain := []byte("The quick brown fox jumps over the lazy dog. 0123456789")
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := buildRC4(c.r, c.bits, c.v, c.aes)
			keyLen := 5
			if c.r >= 3 {
				keyLen = c.bits / 8
			}
			fileKey := fileKeyRC4(passwordPad, cfg, keyLen)

			d, err := New(cfg)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			enc := encryptData(7, 0, c.method, fileKey, plain)
			if got := d.DecryptStreamData(7, 0, enc); !bytes.Equal(got, plain) {
				t.Errorf("stream: got %q, want %q", got, plain)
			}
			// Streaming API must agree with the buffered one.
			r := d.DecryptStream(7, 0, bytes.NewReader(enc))
			if got, _ := io.ReadAll(r); !bytes.Equal(got, plain) {
				t.Errorf("stream reader: got %q", got)
			}
			// A different object number must fail to recover the plaintext.
			if got := d.DecryptStreamData(8, 0, enc); bytes.Equal(got, plain) {
				t.Error("wrong object key decrypted correctly")
			}
		})
	}
}

func TestWrongPasswordFails(t *testing.T) {
	cfg := buildRC4(4, 128, 4, false)
	cfg.U = bytes.Repeat([]byte{0xAB}, 32) // corrupt /U so no empty pw works
	cfg.O = bytes.Repeat([]byte{0xCD}, 32)
	if _, err := New(cfg); err == nil {
		t.Error("expected ErrPasswordRequired for unrecoverable document")
	}
}

func TestOwnerPasswordFallback(t *testing.T) {
	// A file with a non-empty user password but an empty owner password: the
	// empty user password fails Algorithm 6, and the document opens only by
	// recovering the user password from /O via the empty owner password.
	userPw := []byte("s3cret")
	cfg := buildRC4Pw(3, 128, 2, false, userPw)
	fileKey := fileKeyRC4(padPassword(userPw), cfg, 16)
	d, err := New(cfg)
	if err != nil {
		t.Fatalf("owner fallback: %v", err)
	}
	plain := []byte("recovered via owner password")
	enc := encryptData(3, 0, RC4, fileKey, plain)
	if got := d.DecryptStreamData(3, 0, enc); !bytes.Equal(got, plain) {
		t.Errorf("got %q, want %q", got, plain)
	}
}

func TestNonEmptyUserPassword(t *testing.T) {
	userPassword := []byte("s3cret")
	cfg := buildRC4Pw(4, 128, 4, true, userPassword)
	decryptor, err := NewWithPassword(cfg, userPassword)
	if err != nil {
		t.Fatalf("NewWithPassword: %v", err)
	}
	fileKey := fileKeyRC4(padPassword(userPassword), cfg, 16)
	plain := []byte("non-empty user password")
	encrypted := encryptData(9, 0, AESV2, fileKey, plain)
	if got := decryptor.DecryptStreamData(9, 0, encrypted); !bytes.Equal(got, plain) {
		t.Fatalf("decrypted %q", got)
	}
}

func TestUnsupportedHandler(t *testing.T) {
	if _, err := New(Config{Filter: "MyCustomHandler", V: 2, R: 3}); err == nil {
		t.Error("non-Standard filter must be rejected")
	}
}
