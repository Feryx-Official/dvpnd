// SPDX-License-Identifier: Apache-2.0

package common

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"os"
	"strings"
)

// CertificatePin returns the hex SHA-256 of the first certificate in a PEM
// file: what clients pin, since a node's certificate is self-signed.
func CertificatePin(path string) (string, error) {
	pemBytes, err := os.ReadFile(path) //nolint:gosec // G304: the node's own certificate
	if err != nil {
		return "", err
	}

	block, _ := pem.Decode(pemBytes)
	if block == nil || block.Type != "CERTIFICATE" {
		return "", fmt.Errorf("no certificate found in %s", path)
	}

	sum := sha256.Sum256(block.Bytes)

	return hex.EncodeToString(sum[:]), nil
}

// ColonPin writes a hex pin as colon-separated byte pairs ("8d:06:…"), the
// form Hysteria2 nodes on the network send.
func ColonPin(pin string) string {
	pairs := make([]string, 0, len(pin)/2)
	for i := 0; i+1 < len(pin); i += 2 {
		pairs = append(pairs, pin[i:i+2])
	}

	return strings.Join(pairs, ":")
}
