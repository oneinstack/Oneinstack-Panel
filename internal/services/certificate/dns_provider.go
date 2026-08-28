package certificate

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"oneinstack/internal/models"
	"oneinstack/utils"

	"github.com/go-acme/lego/v4/challenge"
	"github.com/go-acme/lego/v4/providers/dns/alidns"
	"github.com/go-acme/lego/v4/providers/dns/cloudflare"
	"github.com/go-acme/lego/v4/providers/dns/tencentcloud"
	"gorm.io/gorm"
)

// DNSChallengeProvider is the small provider boundary used by the ACME
// issuer. Credentials are decrypted only while constructing this object and
// are never copied into task or API models.
type DNSChallengeProvider interface {
	challenge.Provider
	challenge.ProviderTimeout
}

func loadDNSChallengeProvider(db *gorm.DB, accountID string) (DNSChallengeProvider, *models.DNSAccount, error) {
	if db == nil {
		return nil, nil, errors.New("certificate database is not initialized")
	}
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return nil, nil, errors.New("DNS account is required")
	}
	var account models.DNSAccount
	if err := db.First(&account, "id = ?", accountID).Error; err != nil {
		return nil, nil, err
	}
	if !account.Enabled {
		return nil, &account, errors.New("DNS account is disabled")
	}
	if !account.CredentialConfigured {
		return nil, &account, errors.New("DNS account credentials are not configured")
	}
	one, err := utils.DecryptCredential(account.CredentialOne, utils.CredentialPurposeCertificateDNS)
	if err != nil {
		return nil, &account, errors.New("DNS account credential is unavailable")
	}
	two, err := utils.DecryptCredential(account.CredentialTwo, utils.CredentialPurposeCertificateDNS)
	if err != nil {
		return nil, &account, errors.New("DNS account credential is unavailable")
	}

	provider, err := newDNSChallengeProvider(account.Provider, one, two)
	if err != nil {
		return nil, &account, err
	}
	return provider, &account, nil
}

func newDNSChallengeProvider(provider, credentialOne, credentialTwo string) (DNSChallengeProvider, error) {
	const (
		providerTTL        = 120
		propagationTimeout = 2 * time.Minute
		pollingInterval    = 2 * time.Second
		httpTimeout        = 30 * time.Second
	)
	client := &http.Client{Timeout: httpTimeout}
	provider = strings.ToLower(strings.TrimSpace(provider))
	credentialOne = strings.TrimSpace(credentialOne)
	credentialTwo = strings.TrimSpace(credentialTwo)

	switch provider {
	case "cloudflare":
		if credentialOne == "" {
			return nil, errors.New("Cloudflare API Token is required")
		}
		config := cloudflare.NewDefaultConfig()
		config.AuthToken = credentialOne
		config.TTL = providerTTL
		config.PropagationTimeout = propagationTimeout
		config.PollingInterval = pollingInterval
		config.HTTPClient = client
		result, err := cloudflare.NewDNSProviderConfig(config)
		if err != nil {
			return nil, fmt.Errorf("create Cloudflare DNS provider: %w", err)
		}
		return result, nil
	case "aliyun":
		if credentialOne == "" || credentialTwo == "" {
			return nil, errors.New("Aliyun AccessKey ID and AccessKey Secret are required")
		}
		config := alidns.NewDefaultConfig()
		config.APIKey = credentialOne
		config.SecretKey = credentialTwo
		config.TTL = providerTTL
		config.PropagationTimeout = propagationTimeout
		config.PollingInterval = pollingInterval
		config.HTTPTimeout = httpTimeout
		result, err := alidns.NewDNSProviderConfig(config)
		if err != nil {
			return nil, fmt.Errorf("create Aliyun DNS provider: %w", err)
		}
		return result, nil
	case "tencentcloud":
		if credentialOne == "" || credentialTwo == "" {
			return nil, errors.New("Tencent Cloud SecretId and SecretKey are required")
		}
		config := tencentcloud.NewDefaultConfig()
		config.SecretID = credentialOne
		config.SecretKey = credentialTwo
		config.TTL = providerTTL
		config.PropagationTimeout = propagationTimeout
		config.PollingInterval = pollingInterval
		config.HTTPTimeout = httpTimeout
		result, err := tencentcloud.NewDNSProviderConfig(config)
		if err != nil {
			return nil, fmt.Errorf("create Tencent Cloud DNS provider: %w", err)
		}
		return result, nil
	default:
		return nil, fmt.Errorf("unsupported DNS provider %q", provider)
	}
}
