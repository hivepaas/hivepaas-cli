// Package selfupdate keeps the CLI up to date: it reads the signed list of the
// CLI's releases, tells the user when a newer one is out, and replaces the
// running binary with one when asked - docs/superpowers/specs/2026-10-08-cli-design.md,
// section 13.
//
// The list is trusted, and nothing else is. It is signed offline with HivePaaS's
// release keys - the keys installations trust release.json from - under a
// context of the CLI's own, and an archive is installed only when its SHA-256 is
// the one the list gives.
package selfupdate

import (
	"crypto/ed25519"
	"crypto/mldsa"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"strings"
)

const (
	// Context is what the list is signed under: hivepaas's tools/releasesign
	// -context cli. A signature under the server's context, release.json's, does
	// not verify under it, nor the reverse.
	Context = "hivepaas-cli-release-v1"

	algEd25519 = "ed25519"
	algMLDSA65 = "ml-dsa-65"

	keyFileSuffix = ".pub.pem"
	maxSignatures = 16
)

// requiredAlgorithms are the algorithms the list needs a valid signature in, as
// the server requires of release.json: one would do against today's attacks,
// both make a forgery need two of them broken.
var requiredAlgorithms = []string{algEd25519, algMLDSA65}

// ErrSignature is a list that is not signed as it must be.
var ErrSignature = errors.New("the list of releases is not validly signed")

// The release keys, as hivepaas_app/pkg/releasesig/releasekeys has them: the
// weekly CI job compares the two.
//
//go:embed keys/*.pub.pem
var embeddedKeys embed.FS

// publicKey is a release key.
type publicKey struct {
	alg     string
	ed25519 ed25519.PublicKey
	mldsa   *mldsa.PublicKey
}

// Keys are the release keys by their ids: a key's file name without .pub.pem.
type Keys map[string]*publicKey

// EmbeddedKeys are the keys this CLI was built with.
func EmbeddedKeys() (Keys, error) {
	sub, err := fs.Sub(embeddedKeys, "keys")
	if err != nil {
		return nil, fmt.Errorf("the release keys: %w", err)
	}
	return LoadKeys(sub)
}

// LoadKeys reads every <key-id>.pub.pem at the top of fsys, and refuses a set
// without a key of each required algorithm: with it, no list could be accepted.
func LoadKeys(fsys fs.FS) (Keys, error) {
	matches, err := fs.Glob(fsys, "*"+keyFileSuffix)
	if err != nil {
		return nil, fmt.Errorf("the release keys: %w", err)
	}
	keys := Keys{}
	covered := map[string]bool{}
	for _, path := range matches {
		content, err := fs.ReadFile(fsys, path)
		if err != nil {
			return nil, fmt.Errorf("the release key %s: %w", path, err)
		}
		key, err := parsePublicKey(content)
		if err != nil {
			return nil, fmt.Errorf("the release key %s: %w", path, err)
		}
		keys[strings.TrimSuffix(path, keyFileSuffix)] = key
		covered[key.alg] = true
	}
	for _, alg := range requiredAlgorithms {
		if !covered[alg] {
			return nil, fmt.Errorf("no %s release key", alg) //nolint:err113
		}
	}
	return keys, nil
}

func parsePublicKey(content []byte) (*publicKey, error) {
	block, _ := pem.Decode(content)
	if block == nil || block.Type != "PUBLIC KEY" {
		return nil, errors.New("no PUBLIC KEY PEM block") //nolint:err113
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, err //nolint:wrapcheck // the caller names the key
	}
	switch key := parsed.(type) {
	case ed25519.PublicKey:
		return &publicKey{alg: algEd25519, ed25519: key}, nil
	case *mldsa.PublicKey:
		if key.Parameters() != mldsa.MLDSA65() {
			return nil, fmt.Errorf("ML-DSA parameter set %s", key.Parameters()) //nolint:err113
		}
		return &publicKey{alg: algMLDSA65, mldsa: key}, nil
	default:
		return nil, fmt.Errorf("unsupported key %T", parsed) //nolint:err113
	}
}

func (k *publicKey) verify(data, sig []byte) bool {
	switch k.alg {
	case algEd25519:
		return ed25519.VerifyWithOptions(k.ed25519, data, sig, &ed25519.Options{Context: Context}) == nil
	case algMLDSA65:
		return mldsa.Verify(k.mldsa, data, sig, &mldsa.Options{Context: Context}) == nil
	}
	return false
}

// envelope is release.signed.json, as hivepaas's tools/releasesign writes it:
// the list, base64, its SHA-256, and the signatures.
type envelope struct {
	Payload    string `json:"payload"`
	SHA256     string `json:"sha256"`
	Signatures []struct {
		KeyID     string `json:"keyId"`
		Algorithm string `json:"alg"`
		Sig       string `json:"sig"`
	} `json:"signatures"`
}

// Open is the list an envelope carries, when it carries, for each required
// algorithm, a valid signature by one of keys - the server's rule for
// release.json. A signature by a key it does not know is passed over, so a list
// signed with a new key as well as an old one still opens; one by a key it knows
// that does not verify fails the envelope.
func Open(keys Keys, content []byte) ([]byte, error) {
	invalid := func(format string, args ...any) error {
		return fmt.Errorf("%w: %s", ErrSignature, fmt.Sprintf(format, args...))
	}
	var env envelope
	if err := json.Unmarshal(content, &env); err != nil {
		return nil, invalid("malformed envelope")
	}
	if len(env.Signatures) > maxSignatures {
		return nil, invalid("too many signatures")
	}
	data, err := base64.StdEncoding.DecodeString(env.Payload)
	if err != nil || len(data) == 0 {
		return nil, invalid("malformed payload")
	}
	sum := sha256.Sum256(data)
	if subtle.ConstantTimeCompare([]byte(hex.EncodeToString(sum[:])), []byte(env.SHA256)) != 1 {
		return nil, invalid("sha256 does not match")
	}
	satisfied := map[string]bool{}
	for _, entry := range env.Signatures {
		key, found := keys[entry.KeyID]
		if !found {
			continue
		}
		if entry.Algorithm != key.alg {
			return nil, invalid("key %q is %s, the signature says %q", entry.KeyID, key.alg, entry.Algorithm)
		}
		sig, err := base64.StdEncoding.DecodeString(entry.Sig)
		if err != nil || !key.verify(data, sig) {
			return nil, invalid("the signature by key %q does not verify", entry.KeyID)
		}
		satisfied[key.alg] = true
	}
	for _, alg := range requiredAlgorithms {
		if !satisfied[alg] {
			return nil, invalid("no valid %s signature by a key this CLI trusts", alg)
		}
	}
	return data, nil
}
