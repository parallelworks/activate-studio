package crypt

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"errors"
	"fmt"
	"testing"
)

// buildAES256 constructs a valid R5/R6 /Encrypt config for a known file
// key and empty passwords, mirroring the encryption side of ISO 32000-2
// §7.6.4.4.
func buildAES256(t *testing.T, r int, fileKey []byte) Config {
	return buildAES256Pw(t, r, fileKey, nil, nil)
}

func buildAES256Pw(t *testing.T, r int, fileKey, userPassword, ownerPassword []byte) Config {
	t.Helper()
	uValSalt := bytes.Repeat([]byte{0x11}, 8)
	uKeySalt := bytes.Repeat([]byte{0x22}, 8)
	u := append(append(hash2B(userPassword, uValSalt, nil, r), uValSalt...), uKeySalt...)
	ue := aesNoPadEncrypt(hash2B(userPassword, uKeySalt, nil, r), fileKey)

	oValSalt := bytes.Repeat([]byte{0x33}, 8)
	oKeySalt := bytes.Repeat([]byte{0x44}, 8)
	o := append(append(hash2B(ownerPassword, oValSalt, u[:48], r), oValSalt...), oKeySalt...)
	oe := aesNoPadEncrypt(hash2B(ownerPassword, oKeySalt, u[:48], r), fileKey)

	return Config{
		Filter: "Standard", V: 5, R: r, Length: 256, P: -4, EncryptMeta: true,
		U: u, O: o, UE: ue, OE: oe, ID: testID,
		StmF: "StdCF", StrF: "StdCF",
		CF: map[string]Filter{"StdCF": {CFM: "AESV3", Length: 32}},
	}
}

func TestAES256NonEmptyPasswords(t *testing.T) {
	fileKey := bytes.Repeat([]byte{0x6B}, 32)
	for _, revision := range []int{5, 6} {
		cfg := buildAES256Pw(t, revision, fileKey, []byte("user secret"), []byte("owner secret"))
		for _, password := range []string{"user secret", "owner secret"} {
			decryptor, err := NewWithPassword(cfg, []byte(password))
			if err != nil {
				t.Fatalf("R%d password %q: %v", revision, password, err)
			}
			plain := []byte("protected")
			if got := decryptor.DecryptStreamData(1, 0, aesEncrypt(fileKey, plain)); !bytes.Equal(got, plain) {
				t.Fatalf("R%d password %q decrypted %q", revision, password, got)
			}
		}
		if _, err := NewWithPassword(cfg, []byte("wrong")); !errors.Is(err, ErrPasswordRequired) {
			t.Fatalf("R%d wrong password error = %v", revision, err)
		}
	}
}

func aesNoPadEncrypt(key, data []byte) []byte {
	block, err := aes.NewCipher(key[:32])
	if err != nil {
		panic(err)
	}
	out := make([]byte, len(data))
	cipher.NewCBCEncrypter(block, make([]byte, aes.BlockSize)).CryptBlocks(out, data)
	return out
}

func TestAES256RoundTrip(t *testing.T) {
	fileKey := bytes.Repeat([]byte{0x5A}, 32)
	plain := []byte("AES-256 encrypted stream body with some length to it.")
	for _, r := range []int{5, 6} {
		t.Run(fmt.Sprintf("R%d", r), func(t *testing.T) {
			cfg := buildAES256(t, r, fileKey)
			d, err := New(cfg)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			enc := aesEncrypt(fileKey, plain) // AESV3 uses the file key directly
			if got := d.DecryptStreamData(4, 0, enc); !bytes.Equal(got, plain) {
				t.Errorf("R%d stream: got %q, want %q", r, got, plain)
			}
			if got := d.DecryptString(4, 0, enc); !bytes.Equal(got, plain) {
				t.Errorf("R%d string: got %q", r, got)
			}
		})
	}
}

func TestAES256WrongPassword(t *testing.T) {
	cfg := buildAES256(t, 6, bytes.Repeat([]byte{0x5A}, 32))
	cfg.U = append(bytes.Repeat([]byte{0xFF}, 32), cfg.U[32:]...) // break validation hash
	cfg.O = append(bytes.Repeat([]byte{0xEE}, 32), cfg.O[32:]...)
	if _, err := New(cfg); err == nil {
		t.Error("expected ErrPasswordRequired")
	}
}

func TestHash2BRevision5IsSHA256(t *testing.T) {
	// R5 uses a single SHA-256; R6 iterates. They must differ for the same
	// input, guarding against a revision mix-up.
	pw, salt := []byte("x"), bytes.Repeat([]byte{1}, 8)
	if bytes.Equal(hash2B(pw, salt, nil, 5), hash2B(pw, salt, nil, 6)) {
		t.Error("R5 and R6 hashes must differ")
	}
	if len(hash2B(pw, salt, nil, 6)) != 32 {
		t.Error("hash2B must return 32 bytes")
	}
}
