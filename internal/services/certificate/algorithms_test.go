package certificate

import "testing"

func TestSupportedKeyAlgorithmsAreStableAndDefensive(t *testing.T) {
	algorithms := SupportedKeyAlgorithms()
	if len(algorithms) != 5 {
		t.Fatalf("algorithm count = %d, want 5", len(algorithms))
	}
	if algorithms[0].Value != "ec-256" || algorithms[0].Label != "EC 256" {
		t.Fatalf("first algorithm = %#v", algorithms[0])
	}
	algorithms[0].Value = "changed"
	if SupportedKeyAlgorithms()[0].Value != "ec-256" {
		t.Fatal("algorithm registry was mutated through returned slice")
	}
}

func TestIsSupportedKeyAlgorithm(t *testing.T) {
	for _, value := range []string{"ec-256", "ec-384", "rsa-2048", "rsa-3072", "rsa-4096"} {
		if !IsSupportedKeyAlgorithm(value) {
			t.Fatalf("IsSupportedKeyAlgorithm(%q) = false", value)
		}
	}
	if IsSupportedKeyAlgorithm("rsa-1024") {
		t.Fatal("weak unsupported RSA algorithm was accepted")
	}
}
