package sas4

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"crypto/rand"
	"encoding/base64"
	"errors"
)

// APISecret is the shipped default -- confirmed by decrypting a request
// captured from bss.libonet.ly.
const APISecret = "abcdefghijuklmno0123456789012345"

func evpKDF(passphrase, salt []byte) (key, iv []byte) {
	var salted, dx []byte
	for len(salted) < 48 {
		h := md5.New()
		h.Write(dx)
		h.Write(passphrase)
		h.Write(salt)
		dx = h.Sum(nil)
		salted = append(salted, dx...)
	}
	return salted[:32], salted[32:48]
}

// Encrypt produces the CryptoJS/OpenSSL "Salted__" envelope (AES-256-CBC,
// EVP_BytesToKey with MD5 and one iteration), base64 encoded.
func Encrypt(plaintext, passphrase string) (string, error) {
	salt := make([]byte, 8)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key, iv := evpKDF([]byte(passphrase), salt)
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	data := []byte(plaintext)
	pad := 16 - len(data)%16
	data = append(data, bytes.Repeat([]byte{byte(pad)}, pad)...)
	out := make([]byte, len(data))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(out, data)
	blob := append(append([]byte("Salted__"), salt...), out...)
	return base64.StdEncoding.EncodeToString(blob), nil
}

// Decrypt reverses Encrypt.
func Decrypt(b64, passphrase string) (string, error) {
	blob, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return "", err
	}
	if len(blob) < 32 || string(blob[:8]) != "Salted__" || (len(blob)-16)%16 != 0 {
		return "", errors.New("not an OpenSSL salted payload")
	}
	key, iv := evpKDF([]byte(passphrase), blob[8:16])
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	out := make([]byte, len(blob)-16)
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(out, blob[16:])
	pad := int(out[len(out)-1])
	if pad == 0 || pad > 16 || pad > len(out) {
		return "", errors.New("bad padding")
	}
	return string(out[:len(out)-pad]), nil
}
