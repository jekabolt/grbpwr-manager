package shoppay

import (
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"strconv"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/aiprov/keyring"
)

// Everything secret here derives from SHOP_STRIPE_MASTER (HKDF-SHA256, one info per purpose): the
// AES-GCM key that seals a team's secret key and webhook secret, the HMAC key of the Connect state,
// and the HMAC key of the per-team webhook addresses.
const (
	infoSeal    = "shop-stripe-seal"
	infoState   = "shop-stripe-state"
	infoWebhook = "shop-stripe-webhook-path"

	// What a sealed blob holds (its AAD names the team and the field: a blob moved to another team
	// or another column does not open).
	sealSecretKey     = "secret_key"
	sealWebhookSecret = "webhook_secret"

	// webhookTokenLen is the length of a team's webhook address token (base64url of an HMAC).
	webhookTokenLen = 32
)

// errNoMaster: SHOP_STRIPE_MASTER is missing or short.
var errNoMaster = errors.New("shoppay: SHOP_STRIPE_MASTER is not set or shorter than 32 bytes")

type keys struct {
	ring    *keyring.Ring
	state   []byte
	webhook []byte
}

func newKeys(master string) (*keys, error) {
	master = strings.TrimSpace(master)
	if len(master) < MinMasterBytes {
		return nil, errNoMaster
	}
	derive := func(info string) ([]byte, error) { return hkdf.Key(sha256.New, []byte(master), nil, info, 32) }
	seal, err := derive(infoSeal)
	if err != nil {
		return nil, err
	}
	ring, err := keyring.New(base64.StdEncoding.EncodeToString(seal))
	if err != nil {
		return nil, err
	}
	k := &keys{ring: ring}
	if k.state, err = derive(infoState); err != nil {
		return nil, err
	}
	if k.webhook, err = derive(infoWebhook); err != nil {
		return nil, err
	}
	return k, nil
}

func sealAAD(teamID int64, field string) string {
	return "team_stripe:" + strconv.FormatInt(teamID, 10) + ":" + field
}

func (k *keys) seal(teamID int64, field, plain string) ([]byte, error) {
	return k.ring.Seal(plain, sealAAD(teamID, field))
}

func (k *keys) open(teamID int64, field string, blob []byte) (string, error) {
	return k.ring.Open(blob, sealAAD(teamID, field))
}

// webhookToken is the secret path segment of a team's own-keys webhook address.
func (k *keys) webhookToken(teamID int64) string {
	m := hmac.New(sha256.New, k.webhook)
	m.Write([]byte("team:" + strconv.FormatInt(teamID, 10)))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))[:webhookTokenLen]
}

// webhookTokenIs compares in constant time.
func (k *keys) webhookTokenIs(teamID int64, token string) bool {
	return subtle.ConstantTimeCompare([]byte(k.webhookToken(teamID)), []byte(token)) == 1
}

func (k *keys) mac(payload []byte) []byte {
	m := hmac.New(sha256.New, k.state)
	m.Write(payload)
	return m.Sum(nil)
}

// last4 is the last four characters of a key, shown to the owner.
func last4(s string) string { return keyring.Last4(s) }
