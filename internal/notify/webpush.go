// SPDX-License-Identifier: AGPL-3.0-only

package notify

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/url"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Web Push message encryption (RFC 8291, aes128gcm from RFC 8188) and
// VAPID signing (RFC 8292), with the standard library and golang-jwt.

const (
	// recordSize is the aes128gcm record size written in the header. The
	// message is a single record, so this only has to be at least as large
	// as the record.
	recordSize = 4096
	// headerLen is salt (16) + rs (4) + idlen (1) + a P-256 public key (65).
	headerLen = 16 + 4 + 1 + 65
	// maxPlaintext is what fits in the 4096 bytes a push service must
	// accept: less the header, the padding delimiter and the GCM tag
	// (RFC 8291 Section 4).
	maxPlaintext = 4096 - headerLen - 1 - 16

	// vapidSubject is the VAPID contact (RFC 8292 Section 2.1): the public
	// project page, so it holds no personal data. Apple's push service is
	// reported to refuse a mailto subject at localhost.
	vapidSubject = "https://github.com/kushalpandya/wargo"
	// vapidTTL is how long a VAPID JWT stays valid (at most 24 h).
	vapidTTL = 12 * time.Hour
)

// encryptPayload encrypts plaintext for the subscription keys (uaPublic is
// the p256dh key, authSecret the auth secret) with a fresh salt and a fresh
// application server key, as RFC 8291 requires for every message.
func encryptPayload(uaPublic, authSecret, plaintext []byte) ([]byte, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	asPriv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	return encrypt(uaPublic, authSecret, plaintext, salt, asPriv)
}

// encrypt is RFC 8291 encryption with a given salt and application server
// key: the aes128gcm header followed by one record holding plaintext and
// the 0x02 last record delimiter, with no further padding.
func encrypt(uaPublic, authSecret, plaintext []byte, salt []byte, asPriv *ecdh.PrivateKey) ([]byte, error) {
	if len(authSecret) != 16 {
		return nil, fmt.Errorf("auth secret is %d bytes, want 16", len(authSecret))
	}
	if len(salt) != 16 {
		return nil, fmt.Errorf("salt is %d bytes, want 16", len(salt))
	}
	if len(plaintext) > maxPlaintext {
		return nil, fmt.Errorf("push message is %d bytes, at most %d fit", len(plaintext), maxPlaintext)
	}
	if asPriv.Curve() != ecdh.P256() {
		return nil, errors.New("application server key is not P-256")
	}
	// NewPublicKey checks that the point is on the curve (RFC 8291
	// Section 7).
	ua, err := ecdh.P256().NewPublicKey(uaPublic)
	if err != nil {
		return nil, fmt.Errorf("subscription key: %w", err)
	}
	secret, err := asPriv.ECDH(ua)
	if err != nil {
		return nil, err
	}
	asPublic := asPriv.PublicKey().Bytes()

	// RFC 8291 Section 3.4: combine the ECDH secret with the auth secret.
	keyInfo := make([]byte, 0, 14+65+65)
	keyInfo = append(keyInfo, "WebPush: info\x00"...)
	keyInfo = append(keyInfo, ua.Bytes()...)
	keyInfo = append(keyInfo, asPublic...)
	ikm, err := hkdf.Key(sha256.New, secret, authSecret, string(keyInfo), 32)
	if err != nil {
		return nil, err
	}
	// RFC 8188 Section 2.2 and 2.3: content encryption key and nonce.
	cek, err := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: aes128gcm\x00", 16)
	if err != nil {
		return nil, err
	}
	nonce, err := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: nonce\x00", 12)
	if err != nil {
		return nil, err
	}
	blk, err := aes.NewCipher(cek)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(blk)
	if err != nil {
		return nil, err
	}

	out := make([]byte, 0, headerLen+len(plaintext)+1+gcm.Overhead())
	out = append(out, salt...)
	out = binary.BigEndian.AppendUint32(out, recordSize)
	out = append(out, byte(len(asPublic)))
	out = append(out, asPublic...)
	record := make([]byte, 0, len(plaintext)+1)
	record = append(append(record, plaintext...), 0x02)
	// The record sequence number is 0, so the nonce is used as derived.
	return gcm.Seal(out, nonce, record, nil), nil
}

// vapidHeader returns the Authorization header value for a push to
// endpoint (RFC 8292 Section 3): a JWT signed with ES256 whose audience is
// the endpoint's origin, and the public key.
func vapidHeader(endpoint, subject string, key *ecdsa.PrivateKey, now time.Time) (string, error) {
	aud, err := origin(endpoint)
	if err != nil {
		return "", err
	}
	tok, err := jwt.NewWithClaims(jwt.SigningMethodES256, jwt.MapClaims{
		"aud": aud,
		"exp": now.Add(vapidTTL).Unix(),
		"sub": subject,
	}).SignedString(key)
	if err != nil {
		return "", err
	}
	pub, err := key.PublicKey.Bytes()
	if err != nil {
		return "", err
	}
	return "vapid t=" + tok + ", k=" + base64.RawURLEncoding.EncodeToString(pub), nil
}

// origin is the Web origin of a URL (RFC 6454): scheme and host, with the
// port only when it is not the scheme's default.
func origin(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	if (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" {
		return "", fmt.Errorf("push endpoint is not an http(s) URL")
	}
	host := u.Hostname()
	if port := u.Port(); port != "" && !(u.Scheme == "https" && port == "443") && !(u.Scheme == "http" && port == "80") {
		host = net.JoinHostPort(host, port)
	} else if net.ParseIP(host) != nil && net.ParseIP(host).To4() == nil {
		host = "[" + host + "]"
	}
	return u.Scheme + "://" + host, nil
}
