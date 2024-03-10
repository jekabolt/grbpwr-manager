package tron

import (
	"crypto/ecdsa"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"testing"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/mr-tron/base58"
	"github.com/tyler-smith/go-bip39"
)

const (
	// TronAddressPrefix is the prefix for TRON addresses
	TronAddressPrefix   = 0x41
	NileAddressPrefix   = 0xa0
	ShastaAddressPrefix = 0x41
)

func TestGenerateTronAddressFromMnemonic(t *testing.T) {
	// Generate a mnemonic for a new wallet
	entropy, err := bip39.NewEntropy(128) // You can choose entropy size: 128, 160, 192, 224, or 256 bits
	if err != nil {
		log.Fatal(err)
	}

	mnemonic, err := bip39.NewMnemonic(entropy)
	if err != nil {
		log.Fatal(err)
	}

	seed, err := bip39.NewSeedWithErrorChecking(mnemonic, "")
	if err != nil {
		t.Fatal(err)
	}

	// Generate a private key using the seed
	privateKey, err := crypto.ToECDSA(seed[:32]) // Using first 32 bytes of seed as private key
	if err != nil {
		log.Fatal(err)
	}

	// Derive the public key from the private key
	publicKeyECDSA, ok := privateKey.Public().(*ecdsa.PublicKey)
	if !ok {
		log.Fatal("error casting public key to ECDSA")
	}

	// Get the uncompressed public key
	uncompressedPubKey := crypto.FromECDSAPub(publicKeyECDSA)

	// Perform Keccak-256 hashing on the uncompressed public key
	pubKeyHash := crypto.Keccak256(uncompressedPubKey[1:]) // Remove the 0x04 prefix

	// Take the last 20 bytes to create the address
	address := pubKeyHash[len(pubKeyHash)-20:]

	// Add the TRON prefix (0x41) to the address
	tronAddress := append([]byte{ShastaAddressPrefix}, address...)

	// Perform double SHA-256 hashing on the TRON address
	sha256Hash := sha256.Sum256(tronAddress)
	sha256Hash2 := sha256.Sum256(sha256Hash[:])

	// Take the first 4 bytes of the second SHA-256 hash as the checksum
	checksum := sha256Hash2[:4]

	// Add the checksum to the TRON address
	finalAddress := append(tronAddress, checksum...)

	// Convert the TRON address to Base58
	addressBase58 := base58.Encode(finalAddress)

	fmt.Println("Mnemonic:", mnemonic)
	fmt.Println("Private Key:", hex.EncodeToString(crypto.FromECDSA(privateKey)))
	fmt.Println("TRON Address:", addressBase58)
}
