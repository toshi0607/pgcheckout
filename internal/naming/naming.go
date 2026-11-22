package naming

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"

	"github.com/toshi0607/pgtwig/internal/config"
)

const maxIdentifier = 63

var basePattern = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,39}$`)

func ValidateBase(base string) error {
	if !basePattern.MatchString(base) {
		return config.NewError(config.ConfigInvalid, "base name must match ^[a-z_][a-z0-9_]{0,39}$", "")
	}
	return nil
}

func Slug(branch string) string {
	return slug(branch, maxIdentifier)
}

func BranchDB(base, branch string) (string, error) {
	if err := ValidateBase(base); err != nil {
		return "", err
	}
	if branch == "" {
		return "", config.NewError(config.Usage, "branch name must not be empty", "")
	}
	return base + "__" + slug(branch, maxIdentifier-len(base)-2), nil
}

func slug(branch string, limit int) string {
	name := []byte(strings.ToLower(branch))
	for i, c := range name {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '_' {
			name[i] = '_'
		}
	}
	if string(name) == branch && len(name) <= limit {
		return string(name)
	}
	sum := sha256.Sum256([]byte(branch))
	return string(name[:min(len(name), limit-9)]) + "_" + hex.EncodeToString(sum[:4])
}

func PoolDB(base string) (string, error) { return randomDB(base, "__pool_") }
func TmpDB(base string) (string, error)  { return randomDB(base, "__tmp_") }

func randomDB(base, tag string) (string, error) {
	if len(base)+len(tag)+8 > maxIdentifier {
		return "", config.NewError(config.ConfigInvalid, "base name is too long for a generated database name", "")
	}
	if err := ValidateBase(base); err != nil {
		return "", err
	}
	var suffix [4]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return "", fmt.Errorf("generate database name: %w", err)
	}
	return base + tag + hex.EncodeToString(suffix[:]), nil
}

func IsReservedName(base, db string) bool { return strings.HasPrefix(db, base+"__") }
