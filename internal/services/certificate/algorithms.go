package certificate

// KeyAlgorithm describes a key algorithm supported by certificate generators.
// Keep the value stable because it is persisted in API requests and records.
type KeyAlgorithm struct {
	Value   string `json:"value"`
	Label   string `json:"label"`
	KeyType string `json:"keyType"`
	Bits    int    `json:"bits"`
}

var supportedKeyAlgorithms = []KeyAlgorithm{
	{Value: "ec-256", Label: "EC 256", KeyType: "ec", Bits: 256},
	{Value: "ec-384", Label: "EC 384", KeyType: "ec", Bits: 384},
	{Value: "rsa-2048", Label: "RSA 2048", KeyType: "rsa", Bits: 2048},
	{Value: "rsa-3072", Label: "RSA 3072", KeyType: "rsa", Bits: 3072},
	{Value: "rsa-4096", Label: "RSA 4096", KeyType: "rsa", Bits: 4096},
}

// SupportedKeyAlgorithms returns a copy so callers cannot mutate the registry.
func SupportedKeyAlgorithms() []KeyAlgorithm {
	result := make([]KeyAlgorithm, len(supportedKeyAlgorithms))
	copy(result, supportedKeyAlgorithms)
	return result
}

func IsSupportedKeyAlgorithm(value string) bool {
	for _, algorithm := range supportedKeyAlgorithms {
		if algorithm.Value == value {
			return true
		}
	}
	return false
}
