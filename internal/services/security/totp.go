package security

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	passwordcrypto "oneinstack/internal/crypto"
	"oneinstack/internal/models"
	"oneinstack/utils"

	"gorm.io/gorm"
)

const (
	totpIssuer            = "Oneinstack Panel"
	totpDigits            = 6
	totpPeriodSeconds     = int64(30)
	recoveryCodeCount     = 10
	recoveryCodeByteCount = 7
)

var (
	ErrMFAUnavailable      = errors.New("multi-factor authentication is not configured")
	ErrMFAAlreadyEnabled   = errors.New("multi-factor authentication is already enabled")
	ErrMFANotEnabled       = errors.New("multi-factor authentication is not enabled")
	ErrInvalidSecondFactor = errors.New("invalid verification or recovery code")
	ErrInvalidPassword     = errors.New("invalid current password")
)

type TOTPManager struct {
	db  *gorm.DB
	now func() time.Time
}

type TOTPSetup struct {
	Secret     string `json:"secret"`
	OTPAuthURI string `json:"otpauthUri"`
}

type SecurityStatus struct {
	TOTPEnabled            bool `json:"totpEnabled"`
	TOTPSetupPending       bool `json:"totpSetupPending"`
	RecoveryCodesRemaining int  `json:"recoveryCodesRemaining"`
	MustChangePassword     bool `json:"mustChangePassword"`
}

func NewTOTPManager(db *gorm.DB) *TOTPManager {
	return &TOTPManager{db: db, now: time.Now}
}

func (m *TOTPManager) Status(userID int64) (SecurityStatus, error) {
	var user models.User
	if err := m.db.Select("id", "must_change_password").First(&user, userID).Error; err != nil {
		return SecurityStatus{}, err
	}
	var record models.UserMFA
	err := m.db.First(&record, "user_id = ?", userID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return SecurityStatus{MustChangePassword: user.MustChangePassword}, nil
	}
	if err != nil {
		return SecurityStatus{}, err
	}
	hashes, err := decodeRecoveryHashes(record.RecoveryCodeHashesJSON)
	if err != nil {
		return SecurityStatus{}, err
	}
	return SecurityStatus{
		TOTPEnabled:            record.Enabled,
		TOTPSetupPending:       record.PendingSecretEncrypted != "",
		RecoveryCodesRemaining: len(hashes),
		MustChangePassword:     user.MustChangePassword,
	}, nil
}

func (m *TOTPManager) IsEnabled(userID int64) (bool, error) {
	var record models.UserMFA
	err := m.db.Select("enabled").First(&record, "user_id = ?", userID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	return record.Enabled, err
}

func (m *TOTPManager) Setup(userID int64, username string) (TOTPSetup, error) {
	var existing models.UserMFA
	err := m.db.First(&existing, "user_id = ?", userID).Error
	if err == nil && existing.Enabled {
		return TOTPSetup{}, ErrMFAAlreadyEnabled
	}
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return TOTPSetup{}, err
	}
	secretBytes := make([]byte, 20)
	if _, err := rand.Read(secretBytes); err != nil {
		return TOTPSetup{}, fmt.Errorf("generate TOTP secret: %w", err)
	}
	secret := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(secretBytes)
	encrypted, err := utils.EncryptCredential(
		secret, credentialPurpose(utils.CredentialPurposeTOTPPending, userID),
	)
	if err != nil {
		return TOTPSetup{}, err
	}
	record := existing
	record.UserID = userID
	record.Enabled = false
	record.PendingSecretEncrypted = encrypted
	if err := m.db.Save(&record).Error; err != nil {
		return TOTPSetup{}, err
	}
	label := url.PathEscape(totpIssuer + ":" + username)
	query := url.Values{}
	query.Set("secret", secret)
	query.Set("issuer", totpIssuer)
	query.Set("algorithm", "SHA1")
	query.Set("digits", strconv.Itoa(totpDigits))
	query.Set("period", strconv.FormatInt(totpPeriodSeconds, 10))
	return TOTPSetup{
		Secret:     secret,
		OTPAuthURI: "otpauth://totp/" + label + "?" + query.Encode(),
	}, nil
}

func (m *TOTPManager) Confirm(userID int64, password, code string) ([]string, error) {
	var recoveryCodes []string
	err := m.db.Transaction(func(tx *gorm.DB) error {
		user, record, err := loadUserAndMFA(tx, userID)
		if err != nil {
			return err
		}
		if record.Enabled {
			return ErrMFAAlreadyEnabled
		}
		if !passwordcrypto.CheckPasswordHash(password, user.Password) {
			return ErrInvalidPassword
		}
		if record.PendingSecretEncrypted == "" {
			return ErrMFAUnavailable
		}
		secret, err := utils.DecryptCredential(
			record.PendingSecretEncrypted,
			credentialPurpose(utils.CredentialPurposeTOTPPending, userID),
		)
		if err != nil {
			return err
		}
		if !validateTOTP(secret, code, m.now()) {
			return ErrInvalidSecondFactor
		}
		recoveryCodes, err = generateRecoveryCodes()
		if err != nil {
			return err
		}
		hashes, err := hashRecoveryCodes(userID, recoveryCodes)
		if err != nil {
			return err
		}
		encrypted, err := utils.EncryptCredential(
			secret, credentialPurpose(utils.CredentialPurposeTOTPSecret, userID),
		)
		if err != nil {
			return err
		}
		encoded, err := json.Marshal(hashes)
		if err != nil {
			return err
		}
		record.Enabled = true
		record.SecretEncrypted = encrypted
		record.PendingSecretEncrypted = ""
		record.RecoveryCodeHashesJSON = string(encoded)
		record.RecoveryCodesGeneration++
		return tx.Save(record).Error
	})
	return recoveryCodes, err
}

// VerifyLoginCode accepts either a TOTP value or a one-time recovery code.
// Recovery-code removal occurs in the same transaction as verification.
func (m *TOTPManager) VerifyLoginCode(userID int64, code string) error {
	return m.db.Transaction(func(tx *gorm.DB) error {
		var record models.UserMFA
		if err := tx.First(&record, "user_id = ? AND enabled = ?", userID, true).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrMFANotEnabled
			}
			return err
		}
		secret, err := utils.DecryptCredential(
			record.SecretEncrypted,
			credentialPurpose(utils.CredentialPurposeTOTPSecret, userID),
		)
		if err != nil {
			return err
		}
		if counter, valid := matchingTOTPCounter(secret, code, m.now()); valid {
			update := tx.Model(&models.UserMFA{}).
				Where("user_id = ? AND last_totp_counter < ?", userID, counter).
				Update("last_totp_counter", counter)
			if update.Error != nil {
				return update.Error
			}
			if update.RowsAffected == 1 {
				return nil
			}
			return ErrInvalidSecondFactor
		}
		hashes, err := decodeRecoveryHashes(record.RecoveryCodeHashesJSON)
		if err != nil {
			return err
		}
		match, updated, err := consumeRecoveryCode(userID, hashes, code)
		if err != nil {
			return err
		}
		if !match {
			return ErrInvalidSecondFactor
		}
		encoded, err := json.Marshal(updated)
		if err != nil {
			return err
		}
		update := tx.Model(&models.UserMFA{}).
			Where("user_id = ? AND recovery_code_hashes_json = ?",
				userID, record.RecoveryCodeHashesJSON).
			Update("recovery_code_hashes_json", string(encoded))
		if update.Error != nil {
			return update.Error
		}
		if update.RowsAffected != 1 {
			return ErrInvalidSecondFactor
		}
		return nil
	})
}

func (m *TOTPManager) Disable(userID int64, password, code string) error {
	return m.db.Transaction(func(tx *gorm.DB) error {
		user, record, err := loadUserAndMFA(tx, userID)
		if err != nil {
			return err
		}
		if !record.Enabled {
			return ErrMFANotEnabled
		}
		if !passwordcrypto.CheckPasswordHash(password, user.Password) {
			return ErrInvalidPassword
		}
		secret, err := utils.DecryptCredential(
			record.SecretEncrypted,
			credentialPurpose(utils.CredentialPurposeTOTPSecret, userID),
		)
		if err != nil {
			return err
		}
		if !validateTOTP(secret, code, m.now()) {
			return ErrInvalidSecondFactor
		}
		if err := tx.Model(record).Updates(map[string]interface{}{
			"enabled": false, "secret_encrypted": "", "pending_secret_encrypted": "",
			"recovery_code_hashes_json": "",
		}).Error; err != nil {
			return err
		}
		if err := tx.Model(&models.User{}).Where("id = ?", userID).
			Update("security_version", user.EffectiveSecurityVersion()+1).Error; err != nil {
			return err
		}
		now := m.now().UTC()
		return tx.Model(&models.UserSession{}).
			Where("user_id = ? AND revoked_at IS NULL", userID).
			Updates(map[string]interface{}{
				"revoked_at": now, "revocation_reason": "totp_disabled",
			}).Error
	})
}

func (m *TOTPManager) RegenerateRecoveryCodes(userID int64, password, code string) ([]string, error) {
	var recoveryCodes []string
	err := m.db.Transaction(func(tx *gorm.DB) error {
		user, record, err := loadUserAndMFA(tx, userID)
		if err != nil {
			return err
		}
		if !record.Enabled {
			return ErrMFANotEnabled
		}
		if !passwordcrypto.CheckPasswordHash(password, user.Password) {
			return ErrInvalidPassword
		}
		secret, err := utils.DecryptCredential(
			record.SecretEncrypted,
			credentialPurpose(utils.CredentialPurposeTOTPSecret, userID),
		)
		if err != nil {
			return err
		}
		if !validateTOTP(secret, code, m.now()) {
			return ErrInvalidSecondFactor
		}
		recoveryCodes, err = generateRecoveryCodes()
		if err != nil {
			return err
		}
		hashes, err := hashRecoveryCodes(userID, recoveryCodes)
		if err != nil {
			return err
		}
		encoded, err := json.Marshal(hashes)
		if err != nil {
			return err
		}
		record.RecoveryCodeHashesJSON = string(encoded)
		record.RecoveryCodesGeneration++
		return tx.Save(record).Error
	})
	return recoveryCodes, err
}

func loadUserAndMFA(tx *gorm.DB, userID int64) (*models.User, *models.UserMFA, error) {
	var user models.User
	if err := tx.First(&user, userID).Error; err != nil {
		return nil, nil, err
	}
	var record models.UserMFA
	if err := tx.First(&record, "user_id = ?", userID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil, ErrMFAUnavailable
		}
		return nil, nil, err
	}
	return &user, &record, nil
}

func validateTOTP(secret, code string, at time.Time) bool {
	_, valid := matchingTOTPCounter(secret, code, at)
	return valid
}

func matchingTOTPCounter(secret, code string, at time.Time) (int64, bool) {
	code = strings.TrimSpace(code)
	if len(code) != totpDigits {
		return 0, false
	}
	for _, char := range code {
		if char < '0' || char > '9' {
			return 0, false
		}
	}
	counter := at.Unix() / totpPeriodSeconds
	for offset := int64(-1); offset <= 1; offset++ {
		candidateCounter := counter + offset
		expected, err := totpAt(secret, candidateCounter)
		if err == nil && subtle.ConstantTimeCompare([]byte(expected), []byte(code)) == 1 {
			return candidateCounter, true
		}
	}
	return 0, false
}

func totpAt(secret string, counter int64) (string, error) {
	decoded, err := base32.StdEncoding.WithPadding(base32.NoPadding).
		DecodeString(strings.ToUpper(strings.TrimSpace(secret)))
	if err != nil {
		return "", err
	}
	value := make([]byte, 8)
	binary.BigEndian.PutUint64(value, uint64(counter))
	mac := hmac.New(sha1.New, decoded)
	_, _ = mac.Write(value)
	sum := mac.Sum(nil)
	offset := sum[len(sum)-1] & 0x0f
	number := (uint32(sum[offset])&0x7f)<<24 |
		uint32(sum[offset+1])<<16 |
		uint32(sum[offset+2])<<8 |
		uint32(sum[offset+3])
	return fmt.Sprintf("%06d", number%1_000_000), nil
}

func generateRecoveryCodes() ([]string, error) {
	encoding := base32.StdEncoding.WithPadding(base32.NoPadding)
	codes := make([]string, 0, recoveryCodeCount)
	for len(codes) < recoveryCodeCount {
		random := make([]byte, recoveryCodeByteCount)
		if _, err := rand.Read(random); err != nil {
			return nil, err
		}
		value := encoding.EncodeToString(random)
		if len(value) < 10 {
			continue
		}
		codes = append(codes, value[:5]+"-"+value[5:10])
	}
	return codes, nil
}

func hashRecoveryCodes(userID int64, codes []string) ([]string, error) {
	hashes := make([]string, 0, len(codes))
	for _, code := range codes {
		hash, err := recoveryCodeHash(userID, code)
		if err != nil {
			return nil, err
		}
		hashes = append(hashes, hash)
	}
	return hashes, nil
}

func consumeRecoveryCode(userID int64, hashes []string, code string) (bool, []string, error) {
	candidate, err := recoveryCodeHash(userID, code)
	if err != nil {
		return false, nil, err
	}
	for index, hash := range hashes {
		if hmac.Equal([]byte(hash), []byte(candidate)) {
			return true, append(hashes[:index:index], hashes[index+1:]...), nil
		}
	}
	return false, hashes, nil
}

func recoveryCodeHash(userID int64, code string) (string, error) {
	code = strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(code), "-", ""))
	key, err := utils.DeriveCredentialSubkey(
		credentialPurpose(utils.CredentialPurposeRecoveryCode, userID),
	)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(code))
	return fmt.Sprintf("%x", mac.Sum(nil)), nil
}

func credentialPurpose(base string, userID int64) string {
	return fmt.Sprintf("%s:user:%d", base, userID)
}

func decodeRecoveryHashes(value string) ([]string, error) {
	if strings.TrimSpace(value) == "" {
		return nil, nil
	}
	var hashes []string
	if err := json.Unmarshal([]byte(value), &hashes); err != nil {
		return nil, fmt.Errorf("decode recovery code hashes: %w", err)
	}
	return hashes, nil
}
