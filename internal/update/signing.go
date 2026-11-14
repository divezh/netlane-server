package update

import "strings"

// ReleaseKey is the public half of the key that signs every release bundle (ECDSA P-256,
// SHA-256). The private half stays with the owner, off GitHub: netlane-update installs only a
// bundle whose trusttunnel-dev.tar.gz.sig verifies with this key. docs/SIGNING.md.
const ReleaseKey = `-----BEGIN PUBLIC KEY-----
MFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAEtcoVFvdfswZyFlDqvHPR4ZS7wxQL
q7iwNHtv+nx0GBdMsfRqNxJEEuOmJQhX8K87n59fJ8x5K7SFVPT3EbPWdg==
-----END PUBLIC KEY-----`

// SignatureAsset is the detached signature published next to the bundle.
const SignatureAsset = Bundle + ".sig"

// VerifyCommand is a shell command that checks the bundle in the current directory against
// the release key: SHA-256 first, then the owner's signature.
func VerifyCommand() string {
	lines := strings.Split(ReleaseKey, "\n")
	quoted := make([]string, len(lines))
	for i, line := range lines {
		quoted[i] = "'" + line + "'"
	}
	return "sha256sum -c " + Bundle + ".sha256 && printf '%s\\n' " + strings.Join(quoted, " ") + " >netlane-release.pub && openssl dgst -sha256 -verify netlane-release.pub -signature " + SignatureAsset + " " + Bundle
}
