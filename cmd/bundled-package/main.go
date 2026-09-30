package main

import (
	"fmt"
	"os"

	"oneinstack/internal/services/scriptregistry"
	"oneinstack/internal/services/softwarecatalog"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: bundled-package inspect|verify ROOT")
		os.Exit(2)
	}
	switch os.Args[1] {
	case "inspect":
		manifest, digest, err := scriptregistry.BundledPackageDigest(os.Args[2])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Printf("%s\t%s\t%s\n", manifest.Component.ID, manifest.Component.Version, digest)
	case "verify":
		catalog, lock, err := softwarecatalog.VerifyBundledCatalog(os.Args[2])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Printf("verified %d bundled production packages and %d catalog products from %s\n", len(lock.Packages), len(catalog.Products), lock.CenterCommit)
	default:
		fmt.Fprintln(os.Stderr, "usage: bundled-package inspect|verify ROOT")
		os.Exit(2)
	}
}
